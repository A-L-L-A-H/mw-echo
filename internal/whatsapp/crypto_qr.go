package whatsapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// GenerateQRPNG encodes a WhatsApp pairing string into a 256x256 PNG image.
func GenerateQRPNG(code string) ([]byte, error) {
	trimmed := strings.TrimSpace(code)
	if trimmed == "" {
		return nil, errors.New("qr payload is empty")
	}
	pngBytes, err := qrcode.Encode(trimmed, qrcode.Medium, 256)
	if err != nil {
		return nil, fmt.Errorf("encode qr png: %w", err)
	}
	return pngBytes, nil
}

// SignWebhookPayload computes the X-Hub-Signature-256 header value for a webhook body.
func SignWebhookPayload(secret []byte, payload []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	return fmt.Sprintf("sha256=%x", mac.Sum(nil))
}

// VerifyWebhookSignature performs constant-time validation of an X-Hub-Signature-256 header.
func VerifyWebhookSignature(secret []byte, payload []byte, signatureHeader string) bool {
	trimmed := strings.TrimSpace(signatureHeader)
	if len(secret) == 0 || !strings.HasPrefix(trimmed, "sha256=") {
		return false
	}
	providedHex := strings.TrimPrefix(trimmed, "sha256=")
	providedMAC, err := hex.DecodeString(providedHex)
	if err != nil || len(providedMAC) != sha256.Size {
		return false
	}
	expected := hmac.New(sha256.New, secret)
	expected.Write(payload)
	return hmac.Equal(providedMAC, expected.Sum(nil))
}

// InteractiveButton defines a quick-reply or URL action button in an interactive message envelope.
type InteractiveButton struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// InteractiveEnvelope represents interactive message metadata (header, body, footer, buttons).
type InteractiveEnvelope struct {
	Header  string              `json:"header,omitempty"`
	Body    string              `json:"body"`
	Footer  string              `json:"footer,omitempty"`
	Buttons []InteractiveButton `json:"buttons,omitempty"`
}

// SendMessageEnvelope is the unified v1 message send contract supporting text, media, and interactive payloads.
type SendMessageEnvelope struct {
	DeviceID       string               `json:"device_id,omitempty"`
	DeviceIDCamel  string               `json:"deviceId,omitempty"`
	Phone          string               `json:"phone"`
	Type           string               `json:"type,omitempty"`
	Message        string               `json:"message,omitempty"`
	Caption        string               `json:"caption,omitempty"`
	MediaBase64    string               `json:"media_base64,omitempty"`
	MediaID        string               `json:"media_id,omitempty"`
	Filename       string               `json:"filename,omitempty"`
	MimeType       string               `json:"mime_type,omitempty"`
	PTT            bool                 `json:"ptt,omitempty"`
	ReplyMessageID string               `json:"reply_message_id,omitempty"`
	ReplyToID      string               `json:"replyToId,omitempty"`
	Mentions       []string             `json:"mentions,omitempty"`
	Interactive    *InteractiveEnvelope `json:"interactive,omitempty"`
}

// Validate checks required fields and normalizes defaults for a SendMessageEnvelope.
func (e *SendMessageEnvelope) Validate() error {
	if strings.TrimSpace(e.Phone) == "" {
		return errors.New("phone is required")
	}
	kind := strings.ToLower(strings.TrimSpace(e.Type))
	if kind == "" {
		if e.Interactive != nil {
			kind = "interactive"
		} else if strings.TrimSpace(e.MediaBase64) != "" || strings.TrimSpace(e.MediaID) != "" {
			kind = "document"
		} else {
			kind = "text"
		}
		e.Type = kind
	}

	switch kind {
	case "text":
		if strings.TrimSpace(e.Message) == "" {
			return errors.New("message text is required")
		}
	case "interactive":
		if e.Interactive == nil || (strings.TrimSpace(e.Interactive.Body) == "" && strings.TrimSpace(e.Message) == "") {
			return errors.New("interactive body or message text is required")
		}
	case "image", "video", "audio", "document", "file":
		if strings.TrimSpace(e.MediaBase64) == "" && strings.TrimSpace(e.MediaID) == "" {
			return errors.New("media_base64 or media_id is required for media messages")
		}
	default:
		return fmt.Errorf("unsupported message type %q", e.Type)
	}
	return nil
}

// RenderInteractiveText formats an interactive envelope into a canonical WhatsApp text representation.
func RenderInteractiveText(env *InteractiveEnvelope) string {
	if env == nil {
		return ""
	}
	var lines []string
	if h := strings.TrimSpace(env.Header); h != "" {
		lines = append(lines, "*"+h+"*")
	}
	if b := strings.TrimSpace(env.Body); b != "" {
		lines = append(lines, b)
	}
	for idx, btn := range env.Buttons {
		title := strings.TrimSpace(btn.Title)
		if title == "" {
			title = strings.TrimSpace(btn.ID)
		}
		if title != "" {
			lines = append(lines, fmt.Sprintf("[%d] %s", idx+1, title))
		}
	}
	if f := strings.TrimSpace(env.Footer); f != "" {
		lines = append(lines, "_"+f+"_")
	}
	return strings.Join(lines, "\n")
}
