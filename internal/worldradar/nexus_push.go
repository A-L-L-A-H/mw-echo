package worldradar

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// NexusEventPayload is the JSON envelope pushed to MW_NEXUS_URL/api/radar/ingest.
type NexusEventPayload struct {
	EventID       string   `json:"eventId"`
	Title         string   `json:"title"`
	Summary       string   `json:"summary"`
	PrimaryDomain string   `json:"primaryDomain"`
	ClusterKey    string   `json:"clusterKey"`
	SourceURI     string   `json:"sourceUri"`
	OccurredAt    string   `json:"occurredAt"`
	Domains       []string `json:"domains"`
}

func (e *Engine) pushEventToNexusAsync(payload NexusEventPayload) {
	baseURL := strings.TrimRight(strings.TrimSpace(e.nexusURL), "/")
	if baseURL == "" {
		return
	}
	endpoint := baseURL + "/api/radar/ingest"
	client := e.httpClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}

	go func(targetEndpoint string, envelope NexusEventPayload) {
		bodyBytes, err := json.Marshal(envelope)
		if err != nil {
			return
		}
		pushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		httpReq, err := http.NewRequestWithContext(pushCtx, http.MethodPost, targetEndpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")

		httpResp, err := client.Do(httpReq)
		if err != nil {
			return
		}
		_ = httpResp.Body.Close()
	}(endpoint, payload)
}
