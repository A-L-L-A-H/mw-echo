package desktop

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// RiskTier classifies the execution risk level of a desktop action from R0 (read-only) to R4 (prohibited).
type RiskTier string

const (
	RiskTierR0 RiskTier = "R0"
	RiskTierR1 RiskTier = "R1"
	RiskTierR2 RiskTier = "R2"
	RiskTierR3 RiskTier = "R3"
	RiskTierR4 RiskTier = "R4"

	ModeWorkspace = "workspace"
	ModeDeveloper = "developer"

	settingExperienceModeKey = "experience_mode"
)

// ErrValidation is returned when desktop API inputs fail validation.
var ErrValidation = errors.New("desktop validation failed")

var riskTierOrder = map[RiskTier]int{
	RiskTierR0: 0,
	RiskTierR1: 1,
	RiskTierR2: 2,
	RiskTierR3: 3,
	RiskTierR4: 4,
}

var baseActionTiers = map[string]RiskTier{
	"repo.read":           RiskTierR0,
	"repo.search":         RiskTierR0,
	"git.status":          RiskTierR0,
	"git.diff":            RiskTierR0,
	"sidecar.read":        RiskTierR0,
	"mcp.read":            RiskTierR0,
	"worktree.create":     RiskTierR1,
	"worktree.write":      RiskTierR1,
	"worktree.test":       RiskTierR1,
	"pty.exec_local":      RiskTierR1,
	"mcp.local_write":     RiskTierR1,
	"git.commit_local":    RiskTierR2,
	"sidecar.sync_local":  RiskTierR2,
	"service.local_start": RiskTierR2,
	"git.push_remote":     RiskTierR3,
	"network.egress":      RiskTierR3,
	"cloud.provider_call": RiskTierR3,
	"upstream.sync_push":  RiskTierR3,
	"secret.read":         RiskTierR4,
	"system.destructive":  RiskTierR4,
	"worktree.escape":     RiskTierR4,
	"privilege.escalate":  RiskTierR4,
}

var workspaceRestrictedCapabilities = []string{
	"pty.*",
	"worktree.*",
	"mcp.invoke",
	"mesh.bootstrap",
	"projects.import_taskos",
}

var sensitivePathPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:^|[/\\])\.env(?:\.[^/\\]+)?$`),
	regexp.MustCompile(`(?i)(?:^|[/\\])\.git(?:[/\\]|$)`),
	regexp.MustCompile(`(?i)(?:^|[/\\])\.ssh(?:[/\\]|$)`),
	regexp.MustCompile(`(?i)(?:^|[/\\])\.aws(?:[/\\]|$)`),
	regexp.MustCompile(`(?i)(?:^|[/\\])(?:\.npmrc|\.netrc|id_rsa|id_ed25519|credentials\.json)$`),
	regexp.MustCompile(`(?i)\.(?:pem|key|p12|pfx)$`),
}

var destructiveCommandPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\brm\s+-[a-z]*r[a-z]*f[a-z]*\b`),
	regexp.MustCompile(`(?i)\brm\s+-[a-z]*f[a-z]*r[a-z]*\b`),
	regexp.MustCompile(`(?i)\brm\s+.*--no-preserve-root\b`),
	regexp.MustCompile(`(?i)\b(?:mkfs|dd|fdisk|format|del\s+/[sq]|rmdir\s+/s)\b`),
	regexp.MustCompile(`(?i)\b(?:sudo|su|runas|chmod\s+777)\b`),
	regexp.MustCompile(`(?i)\bgit\s+push\s+(?:--force|-f)\b`),
	regexp.MustCompile(`(?i)(?:^|[\s'"/\\(])\.env(?:\.[a-z0-9_.-]+)?(?:$|[\s'"/)])`),
	regexp.MustCompile(`(?i)(?:^|[;&|]\s*)(?:printenv|env)(?:\s|$)`),
}

var remoteEgressCommandPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bgit\s+push\b`),
	regexp.MustCompile(`(?i)\bgit\s+remote\s+(?:add|set-url)\b`),
	regexp.MustCompile(`(?i)\b(?:curl|wget|Invoke-WebRequest|iwr|ssh|scp|rsync)\b`),
}

var (
	authSchemePattern  = regexp.MustCompile(`(?i)\b(Bearer|Basic|Token)\s+[A-Za-z0-9._~+/=-]{6,}`)
	providerKeyPattern = regexp.MustCompile(`\b(?:sk-|ghp_|gho_|ghu_|ghs_|ghr_|glpat-|xoxb-|xoxp-)[A-Za-z0-9_-]{8,}\b`)
	jwtPattern         = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)
	urlUserPassPattern = regexp.MustCompile(`(?i)(https?://)([^:@/\s]+):([^@/\s]+)@`)
	urlUserOnlyPattern = regexp.MustCompile(`(?i)(https?://)([^:@/\s]+)@`)
	kvSecretPattern    = regexp.MustCompile(`(?i)\b(password|passwd|secret|token|credential|api_key|apikey|authorization|x-api-key)\s*[:=]\s*(?:"[^"]*"|'[^']*'|[^\s,;'"}\]]+)`)
)

// RiskEvaluation describes the computed risk tier and authorization decision for a desktop action.
type RiskEvaluation struct {
	Action                string   `json:"action"`
	RequestedTier         RiskTier `json:"requestedTier"`
	ResolvedTier          RiskTier `json:"resolvedTier"`
	Allowed               bool     `json:"allowed"`
	RequiresHumanApproval bool     `json:"requiresHumanApproval"`
	ReasonCode            string   `json:"reasonCode"`
	Reason                string   `json:"reason"`
	Signals               []string `json:"signals,omitempty"`
}

// DesktopModeState describes the persisted desktop experience mode and restricted capabilities.
type DesktopModeState struct {
	Mode                   string   `json:"mode"`
	RestrictedCapabilities []string `json:"restrictedCapabilities"`
	UpdatedAt              string   `json:"updatedAt,omitempty"`
}

// IsSensitivePath reports whether targetPath touches secrets, key material, or traverses parent directories.
func IsSensitivePath(targetPath string) bool {
	normalized := strings.TrimSpace(strings.ReplaceAll(targetPath, `\`, `/`))
	if normalized == "" {
		return false
	}
	for _, segment := range strings.Split(normalized, "/") {
		if segment == ".." {
			return true
		}
	}
	for _, pattern := range sensitivePathPatterns {
		if pattern.MatchString(normalized) {
			return true
		}
	}
	return false
}

// EvaluateAction computes the effective risk tier (R0..R4) from action kind, target path, command, and URL.
func EvaluateAction(action, targetPath, targetCommand, targetEndpointURL string) RiskEvaluation {
	trimmedAction := strings.TrimSpace(action)
	requestedTier := RiskTierR0
	if trimmedAction != "" {
		if baseTier, found := baseActionTiers[trimmedAction]; found {
			requestedTier = baseTier
		} else {
			requestedTier = RiskTierR2
		}
	}

	resolvedTier := requestedTier
	var signals []string
	escalateTo := func(tier RiskTier, signal string) {
		if signal != "" {
			signals = append(signals, signal)
		}
		if riskTierOrder[tier] > riskTierOrder[resolvedTier] {
			resolvedTier = tier
		}
	}

	if IsSensitivePath(targetPath) {
		escalateTo(RiskTierR4, "sensitive_path")
	}

	if strings.TrimSpace(targetCommand) != "" {
		matchedDestructive := false
		for _, pattern := range destructiveCommandPatterns {
			if pattern.MatchString(targetCommand) {
				matchedDestructive = true
				break
			}
		}
		if matchedDestructive {
			escalateTo(RiskTierR4, "destructive_command")
		} else {
			for _, pattern := range remoteEgressCommandPatterns {
				if pattern.MatchString(targetCommand) {
					escalateTo(RiskTierR3, "remote_egress_command")
					break
				}
			}
		}
	}

	trimmedEndpoint := strings.TrimSpace(targetEndpointURL)
	if trimmedEndpoint != "" {
		parsedURL, parseErr := url.Parse(trimmedEndpoint)
		if parseErr != nil || !IsLoopbackEndpointURL(trimmedEndpoint) {
			if parsedURL == nil || parsedURL.User != nil {
				escalateTo(RiskTierR4, "malformed_or_credentialed_endpoint")
			} else {
				escalateTo(RiskTierR3, "non_loopback_endpoint")
			}
		}
	}

	allowed := true
	requiresApproval := false
	reasonCode := "ALLOWED"
	reason := fmt.Sprintf("Action %q authorized at risk tier %s", displayActionLabel(trimmedAction), resolvedTier)

	switch resolvedTier {
	case RiskTierR4:
		allowed = false
		reasonCode = "RISK_R4_PROHIBITED"
		reason = fmt.Sprintf("Action %q resolved to prohibited risk tier R4", displayActionLabel(trimmedAction))
	case RiskTierR3:
		allowed = false
		requiresApproval = true
		reasonCode = "HUMAN_APPROVAL_REQUIRED"
		reason = fmt.Sprintf("Action %q resolved to risk tier R3 and requires human approval",
			displayActionLabel(trimmedAction))
	}

	return RiskEvaluation{
		Action:                trimmedAction,
		RequestedTier:         requestedTier,
		ResolvedTier:          resolvedTier,
		Allowed:               allowed,
		RequiresHumanApproval: requiresApproval,
		ReasonCode:            reasonCode,
		Reason:                reason,
		Signals:               signals,
	}
}

func displayActionLabel(action string) string {
	if action == "" {
		return "unnamed"
	}
	return action
}

// RedactSensitiveText scrubs authorization headers, API keys, JWTs, URL credentials, key-value secrets, and knownSecrets.
func RedactSensitiveText(input string, knownSecrets ...string) (string, bool) {
	output := input
	for _, secret := range knownSecrets {
		trimmed := strings.TrimSpace(secret)
		if len(trimmed) >= 4 {
			output = strings.ReplaceAll(output, trimmed, "[REDACTED]")
		}
	}

	output = authSchemePattern.ReplaceAllString(output, "$1 [REDACTED]")
	output = providerKeyPattern.ReplaceAllString(output, "[REDACTED_API_KEY]")
	output = jwtPattern.ReplaceAllString(output, "[REDACTED_JWT]")
	output = urlUserPassPattern.ReplaceAllString(output, "${1}[REDACTED]:[REDACTED]@")
	output = urlUserOnlyPattern.ReplaceAllString(output, "${1}[REDACTED]@")
	output = kvSecretPattern.ReplaceAllStringFunc(output, func(matched string) string {
		sepIdx := strings.IndexAny(matched, ":=")
		if sepIdx < 0 {
			return matched
		}
		keyPart := strings.TrimSpace(matched[:sepIdx])
		valPart := strings.TrimSpace(matched[sepIdx+1:])
		if valPart == "[REDACTED]" {
			return matched
		}
		if strings.EqualFold(keyPart, "authorization") {
			lowerVal := strings.ToLower(valPart)
			if lowerVal == "bearer" || lowerVal == "basic" || lowerVal == "token" {
				return matched
			}
		}
		return keyPart + "=[REDACTED]"
	})

	return output, output != input
}

func normalizeDesktopMode(raw string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	switch normalized {
	case ModeWorkspace, ModeDeveloper:
		return normalized, true
	default:
		return "", false
	}
}

func buildModeState(mode string, updatedAt string) DesktopModeState {
	restricted := []string{}
	if mode == ModeWorkspace {
		restricted = append(restricted, workspaceRestrictedCapabilities...)
	}
	return DesktopModeState{
		Mode:                   mode,
		RestrictedCapabilities: restricted,
		UpdatedAt:              updatedAt,
	}
}

// GetMode reads the persisted Desktop Experience Mode from desktop_settings, defaulting to developer mode.
func (m *Manager) GetMode(ctx context.Context) (DesktopModeState, error) {
	var storedValue string
	var updatedAt string
	queryErr := m.db.QueryRowContext(
		ctx,
		`SELECT value, updated_at FROM desktop_settings WHERE key = ?`,
		settingExperienceModeKey,
	).Scan(&storedValue, &updatedAt)
	if errors.Is(queryErr, sql.ErrNoRows) {
		return buildModeState(m.defaultMode, ""), nil
	}
	if queryErr != nil {
		return DesktopModeState{}, fmt.Errorf("read desktop mode: %w", queryErr)
	}
	if normalized, ok := normalizeDesktopMode(storedValue); ok {
		return buildModeState(normalized, updatedAt), nil
	}
	return buildModeState(m.defaultMode, updatedAt), nil
}

// SetMode validates and persists the Desktop Experience Mode ("workspace" or "developer") in desktop_settings.
func (m *Manager) SetMode(ctx context.Context, mode string) (DesktopModeState, error) {
	normalized, ok := normalizeDesktopMode(mode)
	if !ok {
		return DesktopModeState{}, fmt.Errorf("%w: mode must be 'workspace' or 'developer'", ErrValidation)
	}

	nowText := time.Now().UTC().Format(time.RFC3339Nano)
	if _, execErr := m.db.ExecContext(ctx, `
INSERT INTO desktop_settings (key, value, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET
  value = excluded.value,
  updated_at = excluded.updated_at
`, settingExperienceModeKey, normalized, nowText); execErr != nil {
		return DesktopModeState{}, fmt.Errorf("persist desktop mode: %w", execErr)
	}

	return buildModeState(normalized, nowText), nil
}
