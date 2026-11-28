package whatsapp

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

const defaultAutoReplySystemPrompt = "You are a helpful WhatsApp auto-reply assistant for mw-echo. Answer briefly and clearly."

// AIGatewayAdapter routes incoming WhatsApp messages through an OpenAI-compatible AI Gateway endpoint.
type AIGatewayAdapter struct {
	endpoint     string
	systemPrompt string
	httpClient   *http.Client
}

// NewAIGatewayAdapter builds an AI Gateway auto-reply adapter targeting gatewayURL (base URL or /v1/chat/completions).
func NewAIGatewayAdapter(gatewayURL string, client *http.Client, systemPrompt string) (*AIGatewayAdapter, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(gatewayURL), "/")
	if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
		return nil, errors.New("ai gateway URL must start with http:// or https://")
	}
	if !strings.HasSuffix(trimmed, "/v1/chat/completions") {
		trimmed += "/v1/chat/completions"
	}
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	prompt := strings.TrimSpace(systemPrompt)
	if prompt == "" {
		prompt = defaultAutoReplySystemPrompt
	}
	return &AIGatewayAdapter{
		endpoint:     trimmed,
		systemPrompt: prompt,
		httpClient:   client,
	}, nil
}

// NewAIGatewayAutoReply creates an AutoReplyFunc backed by NewAIGatewayAdapter.
func NewAIGatewayAutoReply(gatewayURL string, client *http.Client, systemPrompt string) (AutoReplyFunc, error) {
	adapter, err := NewAIGatewayAdapter(gatewayURL, client, systemPrompt)
	if err != nil {
		return nil, err
	}
	return adapter.Reply, nil
}

// Reply sends the incoming sender text to the AI Gateway and returns the generated reply text.
func (a *AIGatewayAdapter) Reply(ctx context.Context, agentID, sender, text string) (string, error) {
	if a == nil {
		return "", errors.New("ai gateway adapter is nil")
	}
	trimmedText := strings.TrimSpace(text)
	if trimmedText == "" {
		return "", errors.New("incoming text is empty")
	}

	payload := map[string]any{
		"messages": []map[string]string{
			{"role": "system", "content": a.systemPrompt},
			{"role": "user", "content": fmt.Sprintf("From %s: %s", strings.TrimSpace(sender), trimmedText)},
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint, bytes.NewReader(encoded))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if cleanAgent := strings.TrimSpace(agentID); cleanAgent != "" {
		req.Header.Set("X-MW-Agent", cleanAgent)
	}

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("ai gateway request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return "", fmt.Errorf("ai gateway HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(rawBody)))
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rawBody, &parsed); err != nil {
		return "", fmt.Errorf("decode ai gateway response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", errors.New("empty ai gateway choice")
	}
	reply := strings.TrimSpace(parsed.Choices[0].Message.Content)
	if reply == "" {
		return "", errors.New("empty ai gateway reply content")
	}
	return reply, nil
}
