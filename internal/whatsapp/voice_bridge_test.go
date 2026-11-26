package whatsapp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestVoiceBridgeAllowlistFailClosed(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"text":"  Restart   The Gateway "}`))
	}))
	defer srv.Close()

	bridge, err := NewVoiceBridge(VoiceBridgeConfig{GatewayURL: srv.URL, AllowedSenders: []string{"+6281234"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.Transcribe(context.Background(), "999@s.whatsapp.net", "", "", []byte("a")); !errors.Is(err, ErrVoiceSenderNotAllowed) {
		t.Fatalf("expected denial, got %v", err)
	}
	if hits.Load() != 0 {
		t.Fatal("gateway called for non-allowlisted sender")
	}
	env, err := bridge.Transcribe(context.Background(), "6281234:7@s.whatsapp.net", "", "", []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	if env.Command != "restart the gateway" || env.Sender != "6281234" || env.Source != "whatsapp-voice" {
		t.Fatalf("unexpected envelope %+v", env)
	}
}

func TestVoiceBridgeEmptyAllowlistAndGatewayErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	empty, _ := NewVoiceBridge(VoiceBridgeConfig{GatewayURL: srv.URL})
	if _, err := empty.Transcribe(context.Background(), "1", "", "", []byte("a")); !errors.Is(err, ErrVoiceSenderNotAllowed) {
		t.Fatalf("empty allowlist must deny, got %v", err)
	}
	open, _ := NewVoiceBridge(VoiceBridgeConfig{GatewayURL: srv.URL, AllowedSenders: []string{"1"}})
	if _, err := open.Transcribe(context.Background(), "1", "", "", []byte("a")); err == nil {
		t.Fatal("expected gateway status error")
	}
	if _, err := NewVoiceBridge(VoiceBridgeConfig{GatewayURL: "ftp://x"}); err == nil {
		t.Fatal("expected URL error")
	}
}
