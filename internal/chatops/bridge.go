package chatops

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
)

// RiskTier identifies the ChatOps execution tier.
type RiskTier string

const (
	RiskR1ReadOnly     RiskTier = "R1"
	RiskR3OTPConfirmed RiskTier = "R3"
)

// ErrOTPRequired is returned when an R3 operation is attempted without an OTP token.
var ErrOTPRequired = errors.New("R3 operation requires OTP confirmation (fail-closed)")

// ErrOTPInvalid is returned when the provided OTP fails verification.
var ErrOTPInvalid = errors.New("R3 operation rejected: invalid or expired OTP (fail-closed)")

// ErrUnknownCommand is returned when the ChatOps command does not match any registered R1 or R3 action.
var ErrUnknownCommand = errors.New("unknown chatops command")

// OTPVerifier validates a one-time password for a sender and action.
type OTPVerifier interface {
	VerifyOTP(ctx context.Context, sender, action, otp string) bool
}

// StaticOTPVerifier provides a constant-time in-memory OTP verifier for tests and static PIN/TOTP hooks.
type StaticOTPVerifier struct {
	ExpectedBySender map[string]string
	GlobalOTP        string
}

// VerifyOTP returns true only when otp is non-empty and matches the sender's expected OTP in constant time.
func (v *StaticOTPVerifier) VerifyOTP(_ context.Context, sender, _, otp string) bool {
	if v == nil {
		return false
	}
	cleanOTP := strings.TrimSpace(otp)
	if cleanOTP == "" {
		return false
	}
	if len(v.ExpectedBySender) > 0 {
		if expected, ok := v.ExpectedBySender[strings.TrimSpace(sender)]; ok && expected != "" {
			return subtle.ConstantTimeCompare([]byte(cleanOTP), []byte(expected)) == 1
		}
	}
	if v.GlobalOTP != "" {
		return subtle.ConstantTimeCompare([]byte(cleanOTP), []byte(v.GlobalOTP)) == 1
	}
	return false
}

// CommandHandler executes an authorized R1 or R3 ChatOps action.
type CommandHandler func(ctx context.Context, sender string, args []string) (string, error)

// BridgeConfig configures the ChatOps bridge connecting WhatsApp commands to R1/R3 handlers.
type BridgeConfig struct {
	OTPVerifier OTPVerifier
	R1Handlers  map[string]CommandHandler
	R3Handlers  map[string]CommandHandler
}

// CommandOutcome captures the execution result and authorization audit trail of a ChatOps command.
type CommandOutcome struct {
	Sender      string   `json:"sender"`
	Action      string   `json:"action"`
	RiskTier    RiskTier `json:"riskTier"`
	Authorized  bool     `json:"authorized"`
	OTPVerified bool     `json:"otpVerified"`
	Output      string   `json:"output,omitempty"`
}

// Bridge routes inbound WhatsApp ChatOps commands to R1 read-only status handlers
// or R3 OTP-confirmed operational handlers (strictly fail-closed without a valid OTP).
type Bridge struct {
	otpVerifier OTPVerifier
	r1Handlers  map[string]CommandHandler
	r3Handlers  map[string]CommandHandler
}

// NewBridge initializes a ChatOps bridge with built-in R1/R3 command aliases and custom handlers.
func NewBridge(cfg BridgeConfig) *Bridge {
	r1 := map[string]CommandHandler{
		"status": func(_ context.Context, _ string, _ []string) (string, error) {
			return "R1 STATUS: ALL SYSTEMS NOMINAL", nil
		},
		"nas": func(_ context.Context, _ string, _ []string) (string, error) {
			return "R1 NAS HEALTH: ONLINE", nil
		},
		"mrr": func(_ context.Context, _ string, _ []string) (string, error) {
			return "R1 BILLING MRR: ACTIVE", nil
		},
	}
	for k, fn := range cfg.R1Handlers {
		if fn != nil {
			r1[strings.ToLower(strings.TrimSpace(k))] = fn
		}
	}

	r3 := map[string]CommandHandler{
		"restart": func(_ context.Context, _ string, args []string) (string, error) {
			target := "default-service"
			if len(args) > 0 {
				target = args[0]
			}
			return fmt.Sprintf("R3 EXECUTED: restarted %s", target), nil
		},
		"deploy": func(_ context.Context, _ string, args []string) (string, error) {
			target := "production"
			if len(args) > 0 {
				target = args[0]
			}
			return fmt.Sprintf("R3 EXECUTED: deployed %s", target), nil
		},
		"reboot-nas": func(_ context.Context, _ string, args []string) (string, error) {
			target := "nas-core-01"
			if len(args) > 0 {
				target = args[0]
			}
			return fmt.Sprintf("R3 EXECUTED: rebooted %s", target), nil
		},
	}
	for k, fn := range cfg.R3Handlers {
		if fn != nil {
			r3[strings.ToLower(strings.TrimSpace(k))] = fn
		}
	}

	return &Bridge{
		otpVerifier: cfg.OTPVerifier,
		r1Handlers:  r1,
		r3Handlers:  r3,
	}
}

// Execute parses a WhatsApp command line (e.g. "/status" or "/restart mw-api --otp 123456" or "restart mw-api otp:123456"),
// allows R1 read-only queries immediately, and enforces fail-closed OTP verification on all R3 operations.
func (b *Bridge) Execute(ctx context.Context, sender, rawCommand string) (CommandOutcome, error) {
	action, args, otp := parseChatOpsCommand(rawCommand)
	if action == "" {
		return CommandOutcome{Sender: sender}, ErrUnknownCommand
	}

	if handler, isR1 := b.r1Handlers[action]; isR1 {
		out, err := handler(ctx, sender, args)
		if err != nil {
			return CommandOutcome{
				Sender:     sender,
				Action:     action,
				RiskTier:   RiskR1ReadOnly,
				Authorized: true,
			}, err
		}
		return CommandOutcome{
			Sender:     sender,
			Action:     action,
			RiskTier:   RiskR1ReadOnly,
			Authorized: true,
			Output:     out,
		}, nil
	}

	if handler, isR3 := b.r3Handlers[action]; isR3 {
		// Fail-closed: R3 commands must never execute without a configured OTPVerifier and a valid OTP.
		if strings.TrimSpace(otp) == "" {
			return CommandOutcome{
				Sender:     sender,
				Action:     action,
				RiskTier:   RiskR3OTPConfirmed,
				Authorized: false,
			}, ErrOTPRequired
		}
		if b.otpVerifier == nil || !b.otpVerifier.VerifyOTP(ctx, sender, action, otp) {
			return CommandOutcome{
				Sender:     sender,
				Action:     action,
				RiskTier:   RiskR3OTPConfirmed,
				Authorized: false,
			}, ErrOTPInvalid
		}
		out, err := handler(ctx, sender, args)
		if err != nil {
			return CommandOutcome{
				Sender:      sender,
				Action:      action,
				RiskTier:    RiskR3OTPConfirmed,
				Authorized:  true,
				OTPVerified: true,
			}, err
		}
		return CommandOutcome{
			Sender:      sender,
			Action:      action,
			RiskTier:    RiskR3OTPConfirmed,
			Authorized:  true,
			OTPVerified: true,
			Output:      out,
		}, nil
	}

	return CommandOutcome{Sender: sender, Action: action}, ErrUnknownCommand
}

func parseChatOpsCommand(raw string) (action string, args []string, otp string) {
	fields := strings.Fields(strings.TrimSpace(raw))
	if len(fields) == 0 {
		return "", nil, ""
	}
	action = strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	rest := fields[1:]

	for i := 0; i < len(rest); i++ {
		tok := rest[i]
		lowerTok := strings.ToLower(tok)
		switch {
		case (lowerTok == "--otp" || lowerTok == "-otp" || lowerTok == "otp") && i+1 < len(rest):
			otp = strings.TrimSpace(rest[i+1])
			i++
		case strings.HasPrefix(lowerTok, "otp:"):
			otp = strings.TrimSpace(tok[4:])
		case strings.HasPrefix(lowerTok, "otp="):
			otp = strings.TrimSpace(tok[4:])
		case strings.HasPrefix(lowerTok, "--otp="):
			otp = strings.TrimSpace(tok[6:])
		default:
			args = append(args, tok)
		}
	}
	return action, args, otp
}
