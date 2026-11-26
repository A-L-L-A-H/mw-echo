package comms

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func ntfyPriorityFromTier(priorityTier string) string {
	switch strings.ToUpper(strings.TrimSpace(priorityTier)) {
	case "P0", "CRITICAL":
		return "urgent"
	case "P1", "HIGH":
		return "high"
	case "P2":
		return "default"
	default:
		return "default"
	}
}

func parseResendTarget(targetRef, defaultFrom string) (string, string, error) {
	cleaned := strings.TrimSpace(targetRef)
	if cleaned == "" {
		return "", "", errors.New("resend recipient targetRef is required")
	}
	fromAddr := strings.TrimSpace(defaultFrom)
	if fromAddr == "" {
		fromAddr = defaultResendFrom
	}
	recipient := cleaned
	if parts := strings.SplitN(cleaned, "|", 2); len(parts) == 2 {
		if strings.TrimSpace(parts[0]) != "" {
			fromAddr = strings.TrimSpace(parts[0])
		}
		recipient = strings.TrimSpace(parts[1])
	}
	if !strings.Contains(recipient, "@") || strings.ContainsAny(recipient, "\r\n") {
		return "", "", fmt.Errorf("invalid resend recipient email %q", recipient)
	}
	return fromAddr, recipient, nil
}

func (m *Manager) sendNtfy(ctx context.Context, bearerToken, topicURL, title, body, priorityTier string) error {
	if err := validateHTTPURL(topicURL); err != nil {
		return fmt.Errorf("invalid ntfy topic url: %w", err)
	}
	msgBody := body
	if msgBody == "" {
		msgBody = title
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, topicURL, strings.NewReader(msgBody))
	if err != nil {
		return err
	}
	if title != "" {
		httpReq.Header.Set("Title", title)
	}
	httpReq.Header.Set("Priority", ntfyPriorityFromTier(priorityTier))
	if bearerToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+bearerToken)
	}

	httpResp, err := m.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("ntfy request failed: %w", err)
	}
	defer httpResp.Body.Close()

	responseBytes, _ := io.ReadAll(io.LimitReader(httpResp.Body, 16*1024))
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return fmt.Errorf("ntfy HTTP %d: %s", httpResp.StatusCode, strings.TrimSpace(string(responseBytes)))
	}
	return nil
}

func (m *Manager) sendResend(ctx context.Context, apiKey, targetRef, title, body string) error {
	if strings.TrimSpace(apiKey) == "" {
		return errors.New("resend API key is empty")
	}
	fromAddr, recipient, err := parseResendTarget(targetRef, m.resendFrom)
	if err != nil {
		return err
	}
	subject := title
	if subject == "" {
		subject = "MoonWitness Notification"
	}
	textBody := body
	if textBody == "" {
		textBody = subject
	}

	payload := struct {
		From    string   `json:"from"`
		To      []string `json:"to"`
		Subject string   `json:"subject"`
		Text    string   `json:"text"`
	}{
		From:    fromAddr,
		To:      []string{recipient},
		Subject: subject,
		Text:    textBody,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	endpoint := m.resendAPIBase + "/emails"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payloadBytes))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	httpResp, err := m.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("resend request failed: %w", err)
	}
	defer httpResp.Body.Close()

	responseBytes, _ := io.ReadAll(io.LimitReader(httpResp.Body, 16*1024))
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return fmt.Errorf("resend HTTP %d: %s", httpResp.StatusCode, strings.TrimSpace(string(responseBytes)))
	}
	return nil
}
