package chatops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultStudioTTSPath = "/v1/audio/speech"
	oggOpusMimeType      = "audio/ogg; codecs=opus"
	maxTTSAudioBytes     = 16 << 20
)

// WhatsAppVoiceTransport abstracts sending OGG Opus voice-note PTT and text fallback over WhatsApp.
type WhatsAppVoiceTransport interface {
	SendVoicePTT(ctx context.Context, recipient, mimeType string, audio []byte) (string, error)
	SendTextFallback(ctx context.Context, recipient, text string) (string, error)
}

// VoiceTTSConfig configures the mw-studio OGG Opus PTT voice-note responder client.
type VoiceTTSConfig struct {
	StudioBaseURL string
	StudioAPIKey  string
	VoiceID       string
	Transport     WhatsAppVoiceTransport
	HTTPClient    *http.Client
}

// VoiceReplyOutcome describes whether the reply was delivered as an OGG Opus PTT voice note or fell back to text.
type VoiceReplyOutcome struct {
	Recipient    string `json:"recipient"`
	Modality     string `json:"modality"` // "voice_ptt" or "text_fallback"
	MimeType     string `json:"mimeType,omitempty"`
	AudioBytes   int    `json:"audioBytes,omitempty"`
	MessageID    string `json:"messageId"`
	FallbackUsed bool   `json:"fallbackUsed"`
	FallbackErr  string `json:"fallbackErr,omitempty"`
}

// VoiceTTSResponder synthesizes speech via mw-studio (OGG Opus PTT) and automatically falls back to text delivery.
type VoiceTTSResponder struct {
	studioEndpoint string
	studioAPIKey   string
	voiceID        string
	transport      WhatsAppVoiceTransport
	httpClient     *http.Client
}

// NewVoiceTTSResponder initializes a VoiceTTSResponder connected to mw-studio and a WhatsApp transport.
func NewVoiceTTSResponder(cfg VoiceTTSConfig) (*VoiceTTSResponder, error) {
	if cfg.Transport == nil {
		return nil, errors.New("whatsapp transport is required")
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.StudioBaseURL), "/")
	endpoint := ""
	if base != "" {
		endpoint = base + defaultStudioTTSPath
	}
	voice := strings.TrimSpace(cfg.VoiceID)
	if voice == "" {
		voice = "id-ID-ArdiNeural"
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &VoiceTTSResponder{
		studioEndpoint: endpoint,
		studioAPIKey:   strings.TrimSpace(cfg.StudioAPIKey),
		voiceID:        voice,
		transport:      cfg.Transport,
		httpClient:     client,
	}, nil
}

// Respond synthesizes OGG Opus audio via mw-studio and sends it as a WhatsApp PTT voice note.
// If mw-studio is unreachable, returns an error, or PTT upload fails, it automatically falls back to text.
func (r *VoiceTTSResponder) Respond(ctx context.Context, recipient, replyText string) (VoiceReplyOutcome, error) {
	cleanRecipient := strings.TrimSpace(recipient)
	cleanText := strings.TrimSpace(replyText)
	if cleanRecipient == "" {
		return VoiceReplyOutcome{}, errors.New("recipient is required")
	}
	if cleanText == "" {
		return VoiceReplyOutcome{}, errors.New("replyText is required")
	}

	audio, synthErr := r.synthesizeOggOpus(ctx, cleanText)
	if synthErr == nil && len(audio) > 0 {
		msgID, pttErr := r.transport.SendVoicePTT(ctx, cleanRecipient, oggOpusMimeType, audio)
		if pttErr == nil {
			return VoiceReplyOutcome{
				Recipient:    cleanRecipient,
				Modality:     "voice_ptt",
				MimeType:     oggOpusMimeType,
				AudioBytes:   len(audio),
				MessageID:    msgID,
				FallbackUsed: false,
			}, nil
		}
		synthErr = fmt.Errorf("send voice ptt: %w", pttErr)
	}

	// Automatic text fallback when TTS synthesis or PTT delivery fails.
	fallbackMsgID, textErr := r.transport.SendTextFallback(ctx, cleanRecipient, cleanText)
	if textErr != nil {
		return VoiceReplyOutcome{
			Recipient:    cleanRecipient,
			FallbackUsed: true,
			FallbackErr:  errString(synthErr),
		}, fmt.Errorf("voice TTS failed (%v) and text fallback failed: %w", synthErr, textErr)
	}
	return VoiceReplyOutcome{
		Recipient:    cleanRecipient,
		Modality:     "text_fallback",
		MessageID:    fallbackMsgID,
		FallbackUsed: true,
		FallbackErr:  errString(synthErr),
	}, nil
}

func (r *VoiceTTSResponder) synthesizeOggOpus(ctx context.Context, text string) ([]byte, error) {
	if r.studioEndpoint == "" {
		return nil, errors.New("mw-studio TTS endpoint not configured")
	}
	reqBody, err := json.Marshal(map[string]any{
		"input":           text,
		"voice":           r.voiceID,
		"response_format": "opus",
		"container":       "ogg",
		"ptt":             true,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.studioEndpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", oggOpusMimeType)
	if r.studioAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.studioAPIKey)
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mw-studio request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxTTSAudioBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("mw-studio HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if len(data) == 0 {
		return nil, errors.New("mw-studio returned empty audio payload")
	}
	return data, nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
