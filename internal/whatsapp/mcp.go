package whatsapp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// MCPToolDefinition describes an MCP tool exposed by the WhatsApp GOWA package.
type MCPToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// MCPToolDefinitions returns the canonical MCP tool schemas for WhatsApp multi-device control.
func MCPToolDefinitions() []MCPToolDefinition {
	return []MCPToolDefinition{
		{
			Name:        "echo_wa_devices",
			Description: "List connected WhatsApp GOWA multi-device instances.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "echo_wa_send_message",
			Description: "Send a WhatsApp message via native Go GOWA.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"phone", "message"},
				"properties": map[string]any{
					"deviceId": map[string]string{"type": "string"},
					"phone":    map[string]string{"type": "string"},
					"message":  map[string]string{"type": "string"},
				},
			},
		},
		{
			Name:        "echo_wa_list_chats",
			Description: "List recent WhatsApp conversations stored in mw-echo.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"deviceId": map[string]string{"type": "string"},
					"query":    map[string]string{"type": "string"},
					"limit":    map[string]string{"type": "integer"},
				},
			},
		},
		{
			Name:        "echo_wa_get_messages",
			Description: "Read messages for a WhatsApp chat JID.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"chatJid"},
				"properties": map[string]any{
					"deviceId": map[string]string{"type": "string"},
					"chatJid":  map[string]string{"type": "string"},
					"limit":    map[string]string{"type": "integer"},
				},
			},
		},
	}
}

type mcpRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type mcpRPCResponse struct {
	JSONRPC string       `json:"jsonrpc"`
	ID      any          `json:"id"`
	Result  any          `json:"result,omitempty"`
	Error   *mcpRPCError `json:"error,omitempty"`
}

type mcpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// HandleMCPToolCall executes a single WhatsApp MCP tool call against Manager.
func (m *Manager) HandleMCPToolCall(ctx context.Context, name string, args map[string]any) (any, error) {
	strArg := func(key string) string {
		if args == nil {
			return ""
		}
		if v, ok := args[key].(string); ok {
			return v
		}
		return ""
	}
	intArg := func(key string, fallback int) int {
		if args == nil {
			return fallback
		}
		switch v := args[key].(type) {
		case float64:
			return int(v)
		case int:
			return v
		default:
			return fallback
		}
	}

	switch strings.TrimSpace(name) {
	case "echo_wa_devices":
		return m.ListDevices(ctx)
	case "echo_wa_send_message":
		deviceID := firstNonEmpty(strArg("deviceId"), strArg("device_id"), "default")
		return m.SendText(ctx, deviceID, strArg("phone"), strArg("message"), strArg("replyToId"), nil)
	case "echo_wa_list_chats":
		return m.ListChats(ctx, firstNonEmpty(strArg("deviceId"), strArg("device_id")), strArg("query"), intArg("limit", 50))
	case "echo_wa_get_messages":
		deviceID := firstNonEmpty(strArg("deviceId"), strArg("device_id"), "default")
		return m.ListMessages(ctx, deviceID, firstNonEmpty(strArg("chatJid"), strArg("chat_jid")), intArg("limit", 50))
	default:
		return nil, fmt.Errorf("unknown whatsapp MCP tool %q", name)
	}
}

// HandleMCPJSONRPC processes a single MCP JSON-RPC 2.0 request payload for offline HTTP or stdio transport.
func (m *Manager) HandleMCPJSONRPC(ctx context.Context, rawReq []byte) ([]byte, int) {
	var rpc mcpRPCRequest
	if err := json.Unmarshal(rawReq, &rpc); err != nil {
		resp, _ := json.Marshal(mcpRPCResponse{
			JSONRPC: "2.0",
			ID:      nil,
			Error:   &mcpRPCError{Code: -32700, Message: "invalid JSON"},
		})
		return resp, http.StatusBadRequest
	}

	reply := func(result any) ([]byte, int) {
		resp, _ := json.Marshal(mcpRPCResponse{
			JSONRPC: "2.0",
			ID:      rpc.ID,
			Result:  result,
		})
		return resp, http.StatusOK
	}
	fail := func(code int, msg string) ([]byte, int) {
		resp, _ := json.Marshal(mcpRPCResponse{
			JSONRPC: "2.0",
			ID:      rpc.ID,
			Error:   &mcpRPCError{Code: code, Message: msg},
		})
		return resp, http.StatusOK
	}

	switch rpc.Method {
	case "initialize":
		return reply(map[string]any{
			"protocolVersion": "2024-11-05",
			"serverInfo":      map[string]string{"name": "mw-echo-gowa", "version": "1.0.0"},
			"capabilities":    map[string]any{"tools": map[string]any{}},
		})
	case "tools/list":
		return reply(map[string]any{"tools": MCPToolDefinitions()})
	case "tools/call":
		var call struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(rpc.Params, &call); err != nil {
			return fail(-32602, "invalid tool params")
		}
		out, err := m.HandleMCPToolCall(ctx, call.Name, call.Arguments)
		if err != nil {
			return fail(-32000, err.Error())
		}
		encoded, _ := json.Marshal(out)
		return reply(map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": string(encoded)},
			},
			"structuredContent": out,
		})
	default:
		return fail(-32601, fmt.Sprintf("method %q not found", rpc.Method))
	}
}

// ServeMCPStdio processes newline-delimited JSON-RPC 2.0 MCP messages over an io.Reader/io.Writer pair.
func (m *Manager) ServeMCPStdio(ctx context.Context, in io.Reader, out io.Writer) error {
	if in == nil || out == nil {
		return errors.New("stdio reader and writer are required")
	}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		respBytes, _ := m.HandleMCPJSONRPC(ctx, []byte(line))
		if _, err := out.Write(append(respBytes, '\n')); err != nil {
			return err
		}
	}
	return scanner.Err()
}
