package whatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"
)

const (
	voiceTranscriptionPath = "/v1/audio/transcriptions"
	voiceMaxAudioBytes     = 16 << 20
	voiceMaxResponseBytes  = 1 << 20
	voiceDefaultModel      = "whisper-1"
)

// ErrVoiceSenderNotAllowed is returned for senders outside the owner allowlist.
var ErrVoiceSenderNotAllowed = errors.New("voice sender is not allowlisted")

// VoiceCommandEnvelope is the normalized result of a transcribed voice note.
type VoiceCommandEnvelope struct {
	Sender     string `json:"sender"`
	Command    string `json:"command"`
	Transcript string `json:"transcript"`
	Source     string `json:"source"`
}

// VoiceBridgeConfig configures the voice-note transcriber.
type VoiceBridgeConfig struct {
	GatewayURL     string
	AllowedSenders []string
	Model          string
	HTTPClient     *http.Client
}

// VoiceBridge transcribes owner voice notes through mw-rocksoul-gateway.
type VoiceBridge struct {
	endpoint string
	model    string
	allowed  map[string]struct{}
	client   *http.Client
}

// NewVoiceBridge validates config; an empty allowlist denies every sender.
func NewVoiceBridge(cfg VoiceBridgeConfig) (*VoiceBridge, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.GatewayURL), "/")
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		return nil, errors.New("voice gateway URL must be http(s)")
	}
	allowed := make(map[string]struct{}, len(cfg.AllowedSenders))
	for _, sender := range cfg.AllowedSenders {
		if normalized := normalizeVoiceSender(sender); normalized != "" {
			allowed[normalized] = struct{}{}
		}
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = voiceDefaultModel
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &VoiceBridge{endpoint: base + voiceTranscriptionPath, model: model, allowed: allowed, client: client}, nil
}

func normalizeVoiceSender(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if at := strings.Index(trimmed, "@"); at >= 0 {
		trimmed = trimmed[:at]
	}
	if colon := strings.Index(trimmed, ":"); colon >= 0 {
		trimmed = trimmed[:colon]
	}
	return strings.TrimPrefix(trimmed, "+")
}

// IsAllowed reports whether sender is on the owner allowlist.
func (b *VoiceBridge) IsAllowed(sender string) bool {
	normalized := normalizeVoiceSender(sender)
	if b == nil || normalized == "" {
		return false
	}
	_, ok := b.allowed[normalized]
	return ok
}

// Transcribe returns a command envelope; non-allowlisted senders fail closed before any network call.
func (b *VoiceBridge) Transcribe(ctx context.Context, sender, filename, mimeType string, audio []byte) (VoiceCommandEnvelope, error) {
	if !b.IsAllowed(sender) {
		return VoiceCommandEnvelope{}, ErrVoiceSenderNotAllowed
	}
	if len(audio) == 0 || len(audio) > voiceMaxAudioBytes {
		return VoiceCommandEnvelope{}, errors.New("voice audio size out of bounds")
	}
	if filename == "" {
		filename = "voice.ogg"
	}
	if mimeType == "" {
		mimeType = "audio/ogg"
	}
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	if err := writer.WriteField("model", b.model); err != nil {
		return VoiceCommandEnvelope{}, err
	}
	partHeader := textproto.MIMEHeader{}
	partHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	partHeader.Set("Content-Type", mimeType)
	part, err := writer.CreatePart(partHeader)
	if err != nil {
		return VoiceCommandEnvelope{}, err
	}
	if _, err := part.Write(audio); err != nil {
		return VoiceCommandEnvelope{}, err
	}
	if err := writer.Close(); err != nil {
		return VoiceCommandEnvelope{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint, &form)
	if err != nil {
		return VoiceCommandEnvelope{}, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := b.client.Do(req)
	if err != nil {
		return VoiceCommandEnvelope{}, fmt.Errorf("voice gateway request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, voiceMaxResponseBytes))
	if err != nil {
		return VoiceCommandEnvelope{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return VoiceCommandEnvelope{}, fmt.Errorf("voice gateway status %d", resp.StatusCode)
	}
	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(rawBody, &parsed); err != nil {
		return VoiceCommandEnvelope{}, fmt.Errorf("voice gateway response: %w", err)
	}
	transcript := strings.TrimSpace(parsed.Text)
	if transcript == "" {
		return VoiceCommandEnvelope{}, errors.New("empty transcript")
	}
	return VoiceCommandEnvelope{
		Sender:     normalizeVoiceSender(sender),
		Command:    strings.ToLower(strings.Join(strings.Fields(transcript), " ")),
		Transcript: transcript,
		Source:     "whatsapp-voice",
	}, nil
}
