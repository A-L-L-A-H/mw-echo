package worldradar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/store"
)

func TestNexusPushHookDeliversEventJSON(t *testing.T) {
	receivedCh := make(chan NexusEventPayload, 2)
	nexusServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/radar/ingest" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var payload NexusEventPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		receivedCh <- payload
		w.WriteHeader(http.StatusAccepted)
	}))
	defer nexusServer.Close()

	rssServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel><title>Test Feed</title>
<item>
  <title>Quantum Battery Milestone</title>
  <link>https://news.example.com/quantum-battery</link>
  <description>New solid-state cell doubles energy density.</description>
  <pubDate>Thu, 26 Nov 2026 10:00:00 GMT</pubDate>
</item>
</channel></rss>`))
	}))
	defer rssServer.Close()

	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub: %v", err)
	}
	t.Cleanup(func() { _ = hub.Close() })

	eng, err := NewEngine(hub, Options{NexusURL: nexusServer.URL})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	ctx := context.Background()
	packs := AllPacks()
	if len(packs) == 0 {
		t.Fatalf("expected at least 1 built-in radar pack")
	}
	inst, err := eng.InstallPack(ctx, packs[0].ID, "Nexus Push Test", "monitor")
	if err != nil {
		t.Fatalf("install pack: %v", err)
	}
	_, err = eng.AddWatchTarget(ctx, inst.ID, WatchTargetInput{
		TargetType:    "feed",
		CanonicalName: "Test RSS",
		ConnectorID:   "rss",
		SourceURI:     rssServer.URL,
	})
	if err != nil {
		t.Fatalf("add target: %v", err)
	}

	summary, err := eng.RunInstance(ctx, inst.ID)
	if err != nil {
		t.Fatalf("run instance: %v", err)
	}
	if summary.EventsTouched == 0 {
		t.Fatalf("expected at least 1 event touched, got %+v", summary)
	}

	select {
	case delivered := <-receivedCh:
		if delivered.EventID == "" || delivered.Title != "Quantum Battery Milestone" || delivered.ClusterKey == "" || len(delivered.Domains) == 0 {
			t.Fatalf("unexpected nexus payload: %+v", delivered)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for async nexus ingest POST")
	}
}
