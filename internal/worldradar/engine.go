package worldradar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/store"
)

var (
	// ErrInstancePaused is returned when collection is attempted on a paused or disabled instance.
	ErrInstancePaused = errors.New("radar instance is paused")
	// ErrNotFound is returned when a requested radar object does not exist.
	ErrNotFound = errors.New("radar resource not found")
	// ErrValidation is returned when input or report grounding validation fails.
	ErrValidation = errors.New("radar validation failed")
)

// Options configures the World Radar Engine.
type Options struct {
	GatewayURL    string
	HTTPClient    *http.Client
	GitHubAPIBase string
	NexusURL      string
}

// Engine executes the R!OTS World Radar Network intelligence cycle on top of HubStore.
type Engine struct {
	hub           *store.HubStore
	gatewayURL    string
	httpClient    *http.Client
	githubAPIBase string
	nexusURL      string
	connectors    *ConnectorRegistry

	schedMu      sync.Mutex
	schedCtx     context.Context
	schedCancel  context.CancelFunc
	schedWG      sync.WaitGroup
	schedStarted bool
	closed       bool
	closeOnce    sync.Once
}

// RadarInstance represents a configured radar monitoring instance.
type RadarInstance struct {
	ID          string   `json:"id"`
	PackID      string   `json:"packId"`
	PackVersion string   `json:"packVersion"`
	Name        string   `json:"name"`
	Mode        string   `json:"mode"`
	Status      string   `json:"status"`
	Domains     []string `json:"domains"`
	Lenses      []string `json:"lenses"`
	ConfigJSON  string   `json:"configJson"`
	CreatedAt   string   `json:"createdAt"`
	UpdatedAt   string   `json:"updatedAt"`
}

// CreateInstanceInput defines fields for creating a radar instance directly.
type CreateInstanceInput struct {
	PackID  string   `json:"packId"`
	Name    string   `json:"name"`
	Mode    string   `json:"mode"`
	Domains []string `json:"domains,omitempty"`
	Lenses  []string `json:"lenses,omitempty"`
}

// UpdateInstanceInput defines mutable instance fields.
type UpdateInstanceInput struct {
	Name   *string `json:"name,omitempty"`
	Mode   *string `json:"mode,omitempty"`
	Status *string `json:"status,omitempty"`
}

// WatchTarget represents a source endpoint monitored by a RadarInstance.
type WatchTarget struct {
	ID                string `json:"id"`
	InstanceID        string `json:"instanceId"`
	TargetType        string `json:"targetType"`
	CanonicalName     string `json:"canonicalName"`
	ConnectorID       string `json:"connectorId"`
	SourceURI         string `json:"sourceUri"`
	IndependenceGroup string `json:"independenceGroup"`
	ETag              string `json:"etag"`
	Cursor            string `json:"cursor"`
	Enabled           bool   `json:"enabled"`
	CreatedAt         string `json:"createdAt"`
}

// WatchTargetInput specifies fields to attach a watch target to an instance.
type WatchTargetInput struct {
	TargetType        string `json:"targetType"`
	CanonicalName     string `json:"canonicalName"`
	ConnectorID       string `json:"connectorId"`
	SourceURI         string `json:"sourceUri"`
	IndependenceGroup string `json:"independenceGroup,omitempty"`
	Cursor            string `json:"cursor,omitempty"`
	Enabled           *bool  `json:"enabled,omitempty"`
}

// TargetRunReport summarizes collection execution for a single watch target.
type TargetRunReport struct {
	TargetID        string `json:"targetId"`
	ConnectorID     string `json:"connectorId"`
	Status          string `json:"status"`
	RecordsAcquired int    `json:"recordsAcquired"`
	ErrorMessage    string `json:"errorMessage,omitempty"`
}

// RunSummary summarizes a full RunInstance execution.
type RunSummary struct {
	InstanceID    string            `json:"instanceId"`
	Status        string            `json:"status"`
	TargetsRun    int               `json:"targetsRun"`
	RecordsIngest int               `json:"recordsIngested"`
	EventsTouched int               `json:"eventsTouched"`
	AlertsEmitted int               `json:"alertsEmitted"`
	TargetReports []TargetRunReport `json:"targetReports"`
	StartedAt     string            `json:"startedAt"`
	FinishedAt    string            `json:"finishedAt"`
}

// RadarSignal represents a detected lens signal on an instance.
type RadarSignal struct {
	ID             string `json:"id"`
	InstanceID     string `json:"instanceId"`
	LensID         string `json:"lensId"`
	Title          string `json:"title"`
	Summary        string `json:"summary"`
	State          string `json:"state"`
	BaselineStatus string `json:"baselineStatus"`
	DetectedAt     string `json:"detectedAt"`
}

// RadarEvent represents a clustered multi-source world event.
type RadarEvent struct {
	ID            string   `json:"id"`
	ClusterKey    string   `json:"clusterKey"`
	Title         string   `json:"title"`
	Summary       string   `json:"summary"`
	PrimaryDomain string   `json:"primaryDomain"`
	Domains       []string `json:"domains"`
	OccurredAt    string   `json:"occurredAt"`
	PublishedAt   string   `json:"publishedAt"`
	ObservedAt    string   `json:"observedAt"`
	IngestedAt    string   `json:"ingestedAt"`
	TimePrecision string   `json:"timePrecision"`
	Status        string   `json:"status"`
	CreatedAt     string   `json:"createdAt"`
	UpdatedAt     string   `json:"updatedAt"`
}

// EvidenceArtifact joins an immutable raw artifact with its event evidence role.
type EvidenceArtifact struct {
	ArtifactID        string `json:"artifactId"`
	ObservationID     string `json:"observationId"`
	Role              string `json:"role"`
	IndependenceGroup string `json:"independenceGroup"`
	SourceURI         string `json:"sourceUri"`
	ContentHash       string `json:"contentHash"`
	Title             string `json:"title"`
	Language          string `json:"language"`
	MimeType          string `json:"mimeType"`
	PayloadExcerpt    string `json:"payloadExcerpt"`
	CapturedAt        string `json:"capturedAt"`
}

// RadarEntity represents a strictly resolved named entity.
type RadarEntity struct {
	ID               string   `json:"id"`
	EntityType       string   `json:"entityType"`
	CanonicalName    string   `json:"canonicalName"`
	StrongIdentifier string   `json:"strongIdentifier"`
	Aliases          []string `json:"aliases"`
	ReviewState      string   `json:"reviewState"`
	CreatedAt        string   `json:"createdAt"`
}

// RadarRelationship links two entities or an entity to an event backed by evidence.
type RadarRelationship struct {
	ID                 string  `json:"id"`
	SubjectEntityID    string  `json:"subjectEntityId"`
	Predicate          string  `json:"predicate"`
	ObjectEntityID     string  `json:"objectEntityId"`
	Confidence         float64 `json:"confidence"`
	EvidenceArtifactID string  `json:"evidenceArtifactId"`
	CreatedAt          string  `json:"createdAt"`
}

// RadarAssessment separates priority scoring from source confidence labeling.
type RadarAssessment struct {
	ID                 string   `json:"id"`
	EventID            string   `json:"eventId"`
	Impact             float64  `json:"impact"`
	Urgency            float64  `json:"urgency"`
	Relevance          float64  `json:"relevance"`
	Velocity           float64  `json:"velocity"`
	Novelty            float64  `json:"novelty"`
	Actionability      float64  `json:"actionability"`
	Exposure           float64  `json:"exposure"`
	PriorityScore      float64  `json:"priorityScore"`
	PriorityTier       string   `json:"priorityTier"`
	ConfidenceLabel    string   `json:"confidenceLabel"`
	IndependentSources int      `json:"independentSources"`
	UncertaintyFlags   []string `json:"uncertaintyFlags"`
	Rationale          string   `json:"rationale"`
	AssessedAt         string   `json:"assessedAt"`
}

// EventDetail bundles a clustered event with its supporting evidence, assessment, and entities.
type EventDetail struct {
	Event      RadarEvent         `json:"event"`
	Evidence   []EvidenceArtifact `json:"evidence"`
	Assessment *RadarAssessment   `json:"assessment,omitempty"`
	Entities   []RadarEntity      `json:"entities"`
}

// RadarAlert represents a deduplicated actionable alert or verification request.
type RadarAlert struct {
	ID              string `json:"id"`
	InstanceID      string `json:"instanceId"`
	EventID         string `json:"eventId"`
	AssessmentID    string `json:"assessmentId"`
	DedupeKey       string `json:"dedupeKey"`
	PriorityTier    string `json:"priorityTier"`
	ConfidenceLabel string `json:"confidenceLabel"`
	AlertKind       string `json:"alertKind"`
	Title           string `json:"title"`
	Status          string `json:"status"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
}

// ReportFact is an observed factual statement strictly tied to persisted evidence IDs.
type ReportFact struct {
	Statement    string   `json:"statement"`
	EvidenceRefs []string `json:"evidenceRefs"`
}

// RadarReport implements Section 11.1 epistemic separation (ObservedFacts vs Claims vs Inferences vs Unknowns).
type RadarReport struct {
	ID             string       `json:"id"`
	InstanceID     string       `json:"instanceId"`
	Title          string       `json:"title"`
	Scope          string       `json:"scope"`
	ObservedFacts  []ReportFact `json:"observedFacts"`
	ReportedClaims []string     `json:"reportedClaims"`
	Inferences     []string     `json:"inferences"`
	Unknowns       []string     `json:"unknowns"`
	EvidenceRefs   []string     `json:"evidenceRefs"`
	Model          string       `json:"model"`
	CreatedAt      string       `json:"createdAt"`
}

// ConnectorHealth tracks operational health and rate-limit state per connector.
type ConnectorHealth struct {
	ConnectorID      string `json:"connectorId"`
	LastAttemptAt    string `json:"lastAttemptAt"`
	LastSuccessAt    string `json:"lastSuccessAt"`
	LastStatus       string `json:"lastStatus"`
	LastError        string `json:"lastError"`
	TotalRuns        int    `json:"totalRuns"`
	FailedRuns       int    `json:"failedRuns"`
	RateLimitedUntil string `json:"rateLimitedUntil"`
}

// FeedbackInput captures human review votes on signals, events, entities, or alerts.
type FeedbackInput struct {
	TargetKind string `json:"targetKind"`
	TargetID   string `json:"targetId"`
	Vote       string `json:"vote"`
	Reason     string `json:"reason"`
}

// FeedbackRecord represents persisted operator feedback.
type FeedbackRecord struct {
	ID         string `json:"id"`
	TargetKind string `json:"targetKind"`
	TargetID   string `json:"targetId"`
	Vote       string `json:"vote"`
	Reason     string `json:"reason"`
	CreatedAt  string `json:"createdAt"`
}

// NewEngine initializes the World Radar schema inside hub.DB() and wires default connectors.
func NewEngine(hub *store.HubStore, opts Options) (*Engine, error) {
	if hub == nil || hub.DB() == nil {
		return nil, fmt.Errorf("worldradar: non-nil HubStore required")
	}
	if err := MigrateSchema(hub.DB()); err != nil {
		return nil, err
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	githubBase := strings.TrimSpace(opts.GitHubAPIBase)
	if githubBase == "" {
		githubBase = "https://api.github.com"
	}
	nexusURL := strings.TrimSpace(opts.NexusURL)
	if nexusURL == "" {
		nexusURL = strings.TrimSpace(os.Getenv("MW_NEXUS_URL"))
	}
	schedCtx, schedCancel := context.WithCancel(context.Background())
	eng := &Engine{
		hub:           hub,
		gatewayURL:    strings.TrimSpace(opts.GatewayURL),
		httpClient:    client,
		githubAPIBase: githubBase,
		nexusURL:      nexusURL,
		connectors:    NewConnectorRegistry(),
		schedCtx:      schedCtx,
		schedCancel:   schedCancel,
	}
	hub.RegisterCloser(eng.Close)
	return eng, nil
}

// InstallPack validates an Alpha Radar Pack and creates an active RadarInstance.
func (e *Engine) InstallPack(ctx context.Context, packID, name, mode string) (RadarInstance, error) {
	pack, ok := GetPack(packID)
	if !ok {
		return RadarInstance{}, fmt.Errorf("%w: unknown pack %q", ErrValidation, packID)
	}
	if err := ValidatePack(pack); err != nil {
		return RadarInstance{}, fmt.Errorf("%w: %v", ErrValidation, err)
	}
	resolvedName := firstNonEmpty(strings.TrimSpace(name), pack.Name)
	resolvedMode := firstNonEmpty(strings.TrimSpace(mode), pack.DefaultMode, "monitor")
	return e.createValidatedInstance(ctx, pack, resolvedName, resolvedMode)
}

// CreateInstance creates a RadarInstance from a pack ID or custom validated domain/lens specification.
func (e *Engine) CreateInstance(ctx context.Context, input CreateInstanceInput) (RadarInstance, error) {
	packID := strings.TrimSpace(input.PackID)
	if pack, ok := GetPack(packID); ok {
		if len(input.Domains) > 0 {
			pack.Domains = input.Domains
		}
		if len(input.Lenses) > 0 {
			pack.Lenses = input.Lenses
		}
		if err := ValidatePack(pack); err != nil {
			return RadarInstance{}, fmt.Errorf("%w: %v", ErrValidation, err)
		}
		return e.createValidatedInstance(ctx, pack, firstNonEmpty(input.Name, pack.Name), firstNonEmpty(input.Mode,
			pack.DefaultMode, "monitor"))
	}

	custom := PackManifest{
		ID:          packID,
		Version:     TaxonomyVersion,
		Name:        firstNonEmpty(strings.TrimSpace(input.Name), packID),
		Domains:     input.Domains,
		Lenses:      input.Lenses,
		DefaultMode: firstNonEmpty(strings.TrimSpace(input.Mode), "monitor"),
	}
	if err := ValidatePack(custom); err != nil {
		return RadarInstance{}, fmt.Errorf("%w: %v", ErrValidation, err)
	}
	return e.createValidatedInstance(ctx, custom, custom.Name, custom.DefaultMode)
}

func (e *Engine) createValidatedInstance(ctx context.Context, pack PackManifest, name, mode string) (RadarInstance,
	error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	instID := makeID("inst", pack.ID, name, now)
	configPayload, _ := json.Marshal(map[string]any{
		"domains": pack.Domains,
		"lenses":  pack.Lenses,
	})
	_, err := e.hub.DB().ExecContext(ctx, `
INSERT INTO radar_instances (id, pack_id, pack_version, name, mode, status, config_json, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, 'active', ?, ?, ?)`,
		instID, pack.ID, firstNonEmpty(pack.Version, TaxonomyVersion), name, mode, string(configPayload), now, now,
	)
	if err != nil {
		return RadarInstance{}, fmt.Errorf("insert radar instance: %w", err)
	}
	e.writeAudit(ctx, "system", "install_pack", "radar_instance", instID, "ok")
	return e.GetInstance(ctx, instID)
}

// GetInstance fetches a RadarInstance by ID.
func (e *Engine) GetInstance(ctx context.Context, id string) (RadarInstance, error) {
	row := e.hub.DB().QueryRowContext(ctx, `
SELECT id, pack_id, pack_version, name, mode, status, config_json, created_at, updated_at
FROM radar_instances WHERE id = ?`, strings.TrimSpace(id))
	inst, err := scanInstance(row)
	if errors.Is(err, sql.ErrNoRows) {
		return RadarInstance{}, fmt.Errorf("%w: instance %q", ErrNotFound, id)
	}
	return inst, err
}

// ListInstances returns all RadarInstances ordered by most recently updated.
func (e *Engine) ListInstances(ctx context.Context) ([]RadarInstance, error) {
	rows, err := e.hub.DB().QueryContext(ctx, `
SELECT id, pack_id, pack_version, name, mode, status, config_json, created_at, updated_at
FROM radar_instances ORDER BY updated_at DESC, created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	instances := make([]RadarInstance, 0)
	for rows.Next() {
		inst, scanErr := scanInstance(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		instances = append(instances, inst)
	}
	return instances, rows.Err()
}

// UpdateInstance updates mutable metadata or lifecycle status on a RadarInstance.
func (e *Engine) UpdateInstance(ctx context.Context, id string, patch UpdateInstanceInput) (RadarInstance, error) {
	existing, err := e.GetInstance(ctx, id)
	if err != nil {
		return RadarInstance{}, err
	}
	if patch.Name != nil && strings.TrimSpace(*patch.Name) != "" {
		existing.Name = strings.TrimSpace(*patch.Name)
	}
	if patch.Mode != nil && strings.TrimSpace(*patch.Mode) != "" {
		existing.Mode = strings.TrimSpace(*patch.Mode)
	}
	if patch.Status != nil {
		normStatus := strings.ToLower(strings.TrimSpace(*patch.Status))
		switch normStatus {
		case "active", "paused", "disabled":
			existing.Status = normStatus
		default:
			return RadarInstance{}, fmt.Errorf("%w: invalid status %q", ErrValidation, *patch.Status)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = e.hub.DB().ExecContext(ctx, `
UPDATE radar_instances SET name = ?, mode = ?, status = ?, updated_at = ? WHERE id = ?`,
		existing.Name, existing.Mode, existing.Status, now, existing.ID,
	)
	if err != nil {
		return RadarInstance{}, err
	}
	e.writeAudit(ctx, "operator", "update_instance", "radar_instance", existing.ID, existing.Status)
	return e.GetInstance(ctx, existing.ID)
}

// PauseInstance pauses collection for an instance while preserving all historical evidence.
func (e *Engine) PauseInstance(ctx context.Context, id string) (RadarInstance, error) {
	paused := "paused"
	return e.UpdateInstance(ctx, id, UpdateInstanceInput{Status: &paused})
}

// ResumeInstance reactivates a paused instance.
func (e *Engine) ResumeInstance(ctx context.Context, id string) (RadarInstance, error) {
	active := "active"
	return e.UpdateInstance(ctx, id, UpdateInstanceInput{Status: &active})
}

// AddWatchTarget attaches a permitted source watch target to an existing instance.
func (e *Engine) AddWatchTarget(ctx context.Context, instanceID string, input WatchTargetInput) (WatchTarget, error) {
	if _, err := e.GetInstance(ctx, instanceID); err != nil {
		return WatchTarget{}, err
	}
	connectorID := strings.ToLower(strings.TrimSpace(input.ConnectorID))
	if _, ok := e.connectors.Get(connectorID); !ok {
		return WatchTarget{}, fmt.Errorf("%w: unknown connector %q", ErrValidation, input.ConnectorID)
	}
	sourceURI := strings.TrimSpace(input.SourceURI)
	if sourceURI == "" {
		return WatchTarget{}, fmt.Errorf("%w: sourceUri is required", ErrValidation)
	}
	indGroup := strings.ToLower(strings.TrimSpace(input.IndependenceGroup))
	if indGroup == "" {
		indGroup = deriveIndependenceGroup(sourceURI, connectorID)
	}
	targetType := firstNonEmpty(strings.TrimSpace(input.TargetType), "feed")
	canonicalName := firstNonEmpty(strings.TrimSpace(input.CanonicalName), sourceURI)
	enabled := 1
	if input.Enabled != nil && !*input.Enabled {
		enabled = 0
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	targetID := makeID("tgt", instanceID, connectorID, sourceURI, now)
	_, err := e.hub.DB().ExecContext(ctx, `
INSERT INTO radar_watch_targets
  (id, instance_id, target_type, canonical_name, connector_id, source_uri, independence_group, etag, cursor, enabled,
  	created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?)`,
		targetID, instanceID, targetType, canonicalName, connectorID, sourceURI, indGroup, strings.TrimSpace(input.Cursor), enabled, now,
	)
	if err != nil {
		return WatchTarget{}, fmt.Errorf("insert watch target: %w", err)
	}
	e.writeAudit(ctx, "operator", "add_watch_target", "radar_watch_target", targetID, "ok")
	return e.getWatchTarget(ctx, targetID)
}

// ListWatchTargets returns all watch targets registered on an instance.
func (e *Engine) ListWatchTargets(ctx context.Context, instanceID string) ([]WatchTarget, error) {
	rows, err := e.hub.DB().QueryContext(ctx, `
SELECT id, instance_id, target_type, canonical_name, connector_id, source_uri, independence_group, etag, cursor, enabled, created_at
FROM radar_watch_targets WHERE instance_id = ? ORDER BY created_at ASC`, strings.TrimSpace(instanceID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	targets := make([]WatchTarget, 0)
	for rows.Next() {
		tgt, scanErr := scanWatchTarget(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		targets = append(targets, tgt)
	}
	return targets, rows.Err()
}

func (e *Engine) getWatchTarget(ctx context.Context, targetID string) (WatchTarget, error) {
	row := e.hub.DB().QueryRowContext(ctx, `
SELECT id, instance_id, target_type, canonical_name, connector_id, source_uri, independence_group, etag, cursor, enabled, created_at
FROM radar_watch_targets WHERE id = ?`, targetID)
	tgt, err := scanWatchTarget(row)
	if errors.Is(err, sql.ErrNoRows) {
		return WatchTarget{}, fmt.Errorf("%w: watch target %q", ErrNotFound, targetID)
	}
	return tgt, err
}

// RunInstance executes the end-to-end collection, normalization, resolution, clustering,
// assessment, and alert routing cycle for an active RadarInstance.
func (e *Engine) RunInstance(ctx context.Context, instanceID string) (RunSummary, error) {
	inst, err := e.GetInstance(ctx, instanceID)
	if err != nil {
		return RunSummary{}, err
	}
	started := time.Now().UTC()
	startedStr := started.Format(time.RFC3339Nano)

	if inst.Status == "paused" || inst.Status == "disabled" {
		e.recordCollectionRun(ctx, inst.ID, "*", RunStatusPaused, 0, "instance is "+inst.Status, startedStr, startedStr)
		e.writeAudit(ctx, "system", "run_instance", "radar_instance", inst.ID, RunStatusPaused)
		return RunSummary{
			InstanceID: inst.ID,
			Status:     RunStatusPaused,
			StartedAt:  startedStr,
			FinishedAt: startedStr,
		}, ErrInstancePaused
	}

	targets, err := e.ListWatchTargets(ctx, inst.ID)
	if err != nil {
		return RunSummary{}, err
	}

	summary := RunSummary{
		InstanceID:    inst.ID,
		Status:        RunStatusNoNewData,
		TargetReports: make([]TargetRunReport, 0, len(targets)),
		StartedAt:     startedStr,
	}

	touchedEvents := make(map[string]bool)
	emittedAlerts := make(map[string]bool)

	for _, target := range targets {
		if !target.Enabled {
			continue
		}
		summary.TargetsRun++
		tgtStart := time.Now().UTC()

		if limitedUntil, isLimited := e.isConnectorRateLimited(ctx, target.ConnectorID, tgtStart); isLimited {
			msg := "rate-limit backoff active until " + limitedUntil
			e.recordCollectionRun(ctx, inst.ID, target.ID, RunStatusRateLimited, 0, msg, tgtStart.Format(time.RFC3339Nano),
				time.Now().UTC().Format(time.RFC3339Nano))
			summary.TargetReports = append(summary.TargetReports, TargetRunReport{
				TargetID:     target.ID,
				ConnectorID:  target.ConnectorID,
				Status:       RunStatusRateLimited,
				ErrorMessage: msg,
			})
			continue
		}

		connector, ok := e.connectors.Get(target.ConnectorID)
		if !ok {
			msg := "missing connector " + target.ConnectorID
			e.recordCollectionRun(ctx, inst.ID, target.ID, RunStatusConnectorFailed, 0, msg, tgtStart.Format(time.RFC3339Nano),
				time.Now().UTC().Format(time.RFC3339Nano))
			summary.TargetReports = append(summary.TargetReports, TargetRunReport{
				TargetID:     target.ID,
				ConnectorID:  target.ConnectorID,
				Status:       RunStatusConnectorFailed,
				ErrorMessage: msg,
			})
			continue
		}

		collectResult := connector.Collect(ctx, CollectRequest{
			Target:        target,
			HTTPClient:    e.httpClient,
			GitHubAPIBase: e.githubAPIBase,
			Now:           tgtStart,
		})
		tgtFinish := time.Now().UTC()
		e.updateConnectorHealth(ctx, target.ConnectorID, collectResult, tgtFinish)

		if collectResult.Status == RunStatusOK || collectResult.Status == RunStatusNoNewData {
			_, _ = e.hub.DB().ExecContext(ctx, `
UPDATE radar_watch_targets SET etag = ?, cursor = ? WHERE id = ?`,
				collectResult.ETag, collectResult.NextCursor, target.ID,
			)
		}

		e.recordCollectionRun(ctx, inst.ID, target.ID, collectResult.Status, len(collectResult.Records),
			collectResult.ErrorMessage, tgtStart.Format(time.RFC3339Nano), tgtFinish.Format(time.RFC3339Nano))

		summary.TargetReports = append(summary.TargetReports, TargetRunReport{
			TargetID:        target.ID,
			ConnectorID:     target.ConnectorID,
			Status:          collectResult.Status,
			RecordsAcquired: len(collectResult.Records),
			ErrorMessage:    collectResult.ErrorMessage,
		})

		if collectResult.Status != RunStatusOK {
			continue
		}

		summary.Status = RunStatusOK
		for _, rec := range collectResult.Records {
			eventID, alertID, ingestErr := e.ingestRecord(ctx, inst, target, rec, tgtFinish)
			if ingestErr != nil {
				return summary, ingestErr
			}
			summary.RecordsIngest++
			if eventID != "" {
				touchedEvents[eventID] = true
			}
			if alertID != "" {
				emittedAlerts[alertID] = true
			}
		}
	}

	summary.EventsTouched = len(touchedEvents)
	summary.AlertsEmitted = len(emittedAlerts)
	summary.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	e.writeAudit(ctx, "system", "run_instance", "radar_instance", inst.ID, summary.Status)
	return summary, nil
}

func (e *Engine) ingestRecord(ctx context.Context, inst RadarInstance, target WatchTarget, rec CollectedRecord,
	observedTime time.Time) (string, string, error) {
	ingestedTime := time.Now().UTC()
	observedStr := observedTime.UTC().Format(time.RFC3339Nano)
	ingestedStr := ingestedTime.Format(time.RFC3339Nano)
	occurredStr := firstNonEmpty(rec.OccurredAt, rec.PublishedAt, observedStr)
	publishedStr := firstNonEmpty(rec.PublishedAt, observedStr)
	precision := firstNonEmpty(rec.TimePrecision, "second")

	artifactID, err := e.persistArtifact(ctx, target, rec, ingestedStr)
	if err != nil {
		return "", "", err
	}

	observationID, err := e.persistObservation(ctx, inst.ID, target, artifactID, rec, occurredStr, publishedStr,
		observedStr, ingestedStr, precision)
	if err != nil {
		return "", "", err
	}

	eventID, err := e.clusterIntoEvent(ctx, inst, target, artifactID, observationID, rec, occurredStr, publishedStr,
		observedStr, ingestedStr, precision)
	if err != nil {
		return "", "", err
	}

	if strings.TrimSpace(rec.EntityName) != "" {
		entityID, entityErr := e.resolveEntityStrict(ctx, rec, artifactID, ingestedStr)
		if entityErr != nil {
			return "", "", entityErr
		}
		if relErr := e.ensureEntityEventRelationship(ctx, entityID, eventID, artifactID, ingestedStr); relErr != nil {
			return "", "", relErr
		}
	}

	if sigErr := e.detectSignal(ctx, inst, rec, ingestedStr); sigErr != nil {
		return "", "", sigErr
	}

	assessment, err := e.assessEvent(ctx, inst, eventID, rec, ingestedStr)
	if err != nil {
		return "", "", err
	}

	alertID, err := e.routeAlert(ctx, inst.ID, eventID, assessment, ingestedStr)
	if err != nil {
		return "", "", err
	}
	return eventID, alertID, nil
}

func (e *Engine) persistArtifact(ctx context.Context, target WatchTarget, rec CollectedRecord,
	capturedAt string) (string, error) {
	contentHash := hashStrings(
		normalizeClusterText(rec.Title),
		normalizeClusterText(rec.Summary),
		strings.TrimSpace(rec.RawExcerpt),
	)
	candidateURI := firstNonEmpty(strings.TrimSpace(rec.SourceURI), target.SourceURI)

	// If another target already captured this exact (source_uri, content_hash), check whether
	// this target already owns an observation for it. On re-runs of the same target we reuse
	// the existing artifact (zero duplicates). On a distinct syndicated target sharing the same
	// item link, we qualify with the target URI so each syndicated feed preserves its own evidence link.
	var existingID string
	err := e.hub.DB().QueryRowContext(ctx, `
SELECT id FROM radar_raw_artifacts WHERE source_uri = ? AND content_hash = ?`,
		candidateURI, contentHash,
	).Scan(&existingID)
	if err == nil {
		var obsCountForTarget int
		_ = e.hub.DB().QueryRowContext(ctx, `
SELECT COUNT(*) FROM radar_observations WHERE target_id = ? AND artifact_id = ?`,
			target.ID, existingID,
		).Scan(&obsCountForTarget)
		if obsCountForTarget > 0 {
			return existingID, nil
		}
		var obsCountOtherTarget int
		_ = e.hub.DB().QueryRowContext(ctx, `
SELECT COUNT(*) FROM radar_observations WHERE target_id != ? AND artifact_id = ?`,
			target.ID, existingID,
		).Scan(&obsCountOtherTarget)
		if obsCountOtherTarget == 0 {
			return existingID, nil
		}
		candidateURI = target.SourceURI + "#" + url.QueryEscape(firstNonEmpty(rec.ExternalID, contentHash[:12]))
		var scopedID string
		if scopedErr := e.hub.DB().QueryRowContext(ctx, `
SELECT id FROM radar_raw_artifacts WHERE source_uri = ? AND content_hash = ?`,
			candidateURI, contentHash,
		).Scan(&scopedID); scopedErr == nil {
			return scopedID, nil
		}
	}

	artifactID := makeID("art", candidateURI, contentHash)
	_, err = e.hub.DB().ExecContext(ctx, `
INSERT OR IGNORE INTO radar_raw_artifacts
  (id, source_uri, content_hash, independence_group, title, language, mime_type, payload_excerpt, captured_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		artifactID, candidateURI, contentHash, target.IndependenceGroup,
		rec.Title, firstNonEmpty(rec.Language, "en"), firstNonEmpty(rec.MimeType, "text/plain"),
		truncateText(firstNonEmpty(rec.RawExcerpt, rec.Summary, rec.Title), 2000), capturedAt,
	)
	if err != nil {
		return "", fmt.Errorf("insert raw artifact: %w", err)
	}
	var resolvedID string
	if selErr := e.hub.DB().QueryRowContext(ctx, `
SELECT id FROM radar_raw_artifacts WHERE source_uri = ? AND content_hash = ?`,
		candidateURI, contentHash,
	).Scan(&resolvedID); selErr == nil {
		return resolvedID, nil
	}
	return artifactID, nil
}

func (e *Engine) persistObservation(ctx context.Context, instanceID string, target WatchTarget, artifactID string,
	rec CollectedRecord, occurredAt, publishedAt, observedAt, ingestedAt, precision string) (string, error) {
	var existingID string
	err := e.hub.DB().QueryRowContext(ctx, `
SELECT id FROM radar_observations WHERE instance_id = ? AND target_id = ? AND artifact_id = ? LIMIT 1`,
		instanceID, target.ID, artifactID,
	).Scan(&existingID)
	if err == nil && existingID != "" {
		return existingID, nil
	}

	obsID := makeID("obs", instanceID, target.ID, artifactID, ingestedAt)
	payload := firstNonEmpty(rec.PayloadJSON, "{}")
	_, err = e.hub.DB().ExecContext(ctx, `
INSERT INTO radar_observations
  (id, instance_id, target_id, artifact_id, source_type, external_id, title, summary, occurred_at, published_at,
  	observed_at, ingested_at, time_precision, payload_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		obsID, instanceID, target.ID, artifactID, target.ConnectorID,
		firstNonEmpty(rec.ExternalID, artifactID), rec.Title, rec.Summary,
		occurredAt, publishedAt, observedAt, ingestedAt, precision, payload,
	)
	if err != nil {
		return "", fmt.Errorf("insert observation: %w", err)
	}
	return obsID, nil
}

func (e *Engine) resolveEntityStrict(ctx context.Context, rec CollectedRecord, artifactID, createdAt string) (string,
	error) {
	canonicalName := strings.TrimSpace(rec.EntityName)
	normName := normalizeEntityName(canonicalName)
	strongID := strings.TrimSpace(rec.StrongID)
	entityType := firstNonEmpty(strings.TrimSpace(rec.EntityType), "organization")

	// Strict rule: ONLY merge when BOTH normalized canonical name AND non-empty strong_identifier match.
	if strongID != "" {
		var matchedID string
		err := e.hub.DB().QueryRowContext(ctx, `
SELECT id FROM radar_entities
WHERE LOWER(TRIM(canonical_name)) = ? AND strong_identifier = ?
LIMIT 1`, normName, strongID,
		).Scan(&matchedID)
		if err == nil && matchedID != "" {
			return matchedID, nil
		}
	} else {
		// If missing strong_identifier, do NOT merge across different artifacts; reuse only if
		// this exact artifact already created a relationship to a matching unverified entity.
		var sameArtifactEntityID string
		err := e.hub.DB().QueryRowContext(ctx, `
SELECT ent.id
FROM radar_entities ent
JOIN radar_relationships rel ON rel.subject_entity_id = ent.id
WHERE rel.evidence_artifact_id = ? AND LOWER(TRIM(ent.canonical_name)) = ? AND ent.strong_identifier = ''
LIMIT 1`, artifactID, normName,
		).Scan(&sameArtifactEntityID)
		if err == nil && sameArtifactEntityID != "" {
			return sameArtifactEntityID, nil
		}
	}

	var nameCollisionCount int
	_ = e.hub.DB().QueryRowContext(ctx, `
SELECT COUNT(*) FROM radar_entities WHERE LOWER(TRIM(canonical_name)) = ?`, normName,
	).Scan(&nameCollisionCount)

	reviewState := "resolved"
	if strongID == "" || nameCollisionCount > 0 {
		reviewState = "review_required"
	}

	entityID := makeID("ent", normName, strongID, artifactID, createdAt)
	aliasesJSON, _ := json.Marshal([]string{canonicalName})
	_, err := e.hub.DB().ExecContext(ctx, `
INSERT INTO radar_entities (id, entity_type, canonical_name, strong_identifier, aliases_json, review_state, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		entityID, entityType, canonicalName, strongID, string(aliasesJSON), reviewState, createdAt,
	)
	if err != nil {
		return "", fmt.Errorf("insert entity: %w", err)
	}
	return entityID, nil
}

func (e *Engine) ensureEntityEventRelationship(ctx context.Context, entityID, eventID, artifactID,
	createdAt string) error {
	var existingID string
	err := e.hub.DB().QueryRowContext(ctx, `
SELECT id FROM radar_relationships
WHERE subject_entity_id = ? AND predicate = 'involved_in' AND object_entity_id = ? AND evidence_artifact_id = ?
LIMIT 1`, entityID, eventID, artifactID,
	).Scan(&existingID)
	if err == nil && existingID != "" {
		return nil
	}
	relID := makeID("rel", entityID, eventID, artifactID)
	_, err = e.hub.DB().ExecContext(ctx, `
INSERT INTO radar_relationships (id, subject_entity_id, predicate, object_entity_id, confidence, evidence_artifact_id,
	created_at)
VALUES (?, ?, 'involved_in', ?, 0.85, ?, ?)`,
		relID, entityID, eventID, artifactID, createdAt,
	)
	return err
}

func (e *Engine) clusterIntoEvent(ctx context.Context, inst RadarInstance, target WatchTarget, artifactID,
	observationID string, rec CollectedRecord, occurredAt, publishedAt, observedAt, ingestedAt, precision string) (string,
	error) {
	clusterKey := hashStrings(normalizeClusterText(rec.Title), normalizeClusterText(rec.Summary))
	domains := mergeDomains(rec.Domains, inst.Domains)
	if len(domains) == 0 {
		domains = []string{"D01"}
	}
	primaryDomain := domains[0]

	var existingID, existingDomainsJSON, existingOccurred string
	err := e.hub.DB().QueryRowContext(ctx, `
SELECT id, domains_json, occurred_at FROM radar_events WHERE cluster_key = ?`, clusterKey,
	).Scan(&existingID, &existingDomainsJSON, &existingOccurred)

	if errors.Is(err, sql.ErrNoRows) {
		eventID := makeID("evt", clusterKey)
		domainsJSON, _ := json.Marshal(domains)
		_, insErr := e.hub.DB().ExecContext(ctx, `
INSERT INTO radar_events
  (id, cluster_key, title, summary, primary_domain, domains_json, occurred_at, published_at, observed_at, ingested_at,
  	time_precision, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?)`,
			eventID, clusterKey, rec.Title, rec.Summary, primaryDomain, string(domainsJSON),
			occurredAt, publishedAt, observedAt, ingestedAt, precision, ingestedAt, ingestedAt,
		)
		if insErr != nil {
			return "", fmt.Errorf("insert radar event: %w", insErr)
		}
		existingID = eventID
	} else if err != nil {
		return "", fmt.Errorf("lookup radar event: %w", err)
	} else {
		var existingDomains []string
		_ = json.Unmarshal([]byte(existingDomainsJSON), &existingDomains)
		mergedDomains := mergeDomains(existingDomains, domains)
		domainsJSON, _ := json.Marshal(mergedDomains)
		preservedOccurred := earliestRFC3339(existingOccurred, occurredAt)
		_, updErr := e.hub.DB().ExecContext(ctx, `
UPDATE radar_events
SET domains_json = ?, occurred_at = ?, published_at = ?, observed_at = ?, ingested_at = ?, updated_at = ?
WHERE id = ?`,
			string(domainsJSON), preservedOccurred, publishedAt, observedAt, ingestedAt, ingestedAt, existingID,
		)
		if updErr != nil {
			return "", fmt.Errorf("update radar event: %w", updErr)
		}
	}

	role := "supporting"
	if target.ConnectorID == "github" || strings.EqualFold(target.TargetType,
		"primary_official") || strings.EqualFold(target.TargetType, "official") {
		role = "primary"
	}
	_, linkErr := e.hub.DB().ExecContext(ctx, `
INSERT OR IGNORE INTO radar_event_evidence (event_id, artifact_id, observation_id, role, independence_group)
VALUES (?, ?, ?, ?, ?)`,
		existingID, artifactID, observationID, role, target.IndependenceGroup,
	)
	if linkErr != nil {
		return "", fmt.Errorf("link event evidence: %w", linkErr)
	}

	e.pushEventToNexusAsync(NexusEventPayload{
		EventID:       existingID,
		Title:         rec.Title,
		Summary:       rec.Summary,
		PrimaryDomain: primaryDomain,
		ClusterKey:    clusterKey,
		SourceURI:     firstNonEmpty(strings.TrimSpace(rec.SourceURI), target.SourceURI),
		OccurredAt:    occurredAt,
		Domains:       domains,
	})
	return existingID, nil
}

func (e *Engine) detectSignal(ctx context.Context, inst RadarInstance, rec CollectedRecord, detectedAt string) error {
	lensID := inferLensForRecord(inst.Lenses, rec)
	signalID := makeID("sig", inst.ID, lensID, normalizeClusterText(rec.Title), detectedAt)
	_, err := e.hub.DB().ExecContext(ctx, `
INSERT INTO radar_signals (id, instance_id, lens_id, title, summary, state, baseline_status, detected_at)
VALUES (?, ?, ?, ?, ?, 'active', 'ready', ?)`,
		signalID, inst.ID, lensID, rec.Title, truncateText(firstNonEmpty(rec.Summary, rec.Title), 500), detectedAt,
	)
	if err != nil {
		return fmt.Errorf("insert signal: %w", err)
	}
	return nil
}

func (e *Engine) assessEvent(ctx context.Context, inst RadarInstance, eventID string, rec CollectedRecord,
	assessedAt string) (RadarAssessment, error) {
	var independentSources int
	if err := e.hub.DB().QueryRowContext(ctx, `
SELECT COUNT(DISTINCT independence_group) FROM radar_event_evidence WHERE event_id = ?`, eventID,
	).Scan(&independentSources); err != nil {
		return RadarAssessment{}, err
	}

	var evidenceCount int
	_ = e.hub.DB().QueryRowContext(ctx, `
SELECT COUNT(*) FROM radar_event_evidence WHERE event_id = ?`, eventID,
	).Scan(&evidenceCount)

	var primaryCount int
	_ = e.hub.DB().QueryRowContext(ctx, `
SELECT COUNT(*)
FROM radar_event_evidence ev
JOIN radar_observations obs ON obs.id = ev.observation_id
JOIN radar_watch_targets tgt ON tgt.id = obs.target_id
WHERE ev.event_id = ? AND (obs.source_type = 'github' OR LOWER(tgt.target_type) IN ('primary_official', 'official'))`, eventID,
	).Scan(&primaryCount)

	confidenceLabel := "unverified"
	if independentSources >= 2 {
		confidenceLabel = "corroborated"
	} else if primaryCount > 0 {
		confidenceLabel = "verified_primary"
	} else if independentSources == 1 {
		confidenceLabel = "single_source"
	}

	impact, urgency, relevance, velocity, novelty, actionability, exposure := scorePriorityAxes(rec, evidenceCount,
		independentSources)
	priorityScore := math.Round((0.22*impact+0.18*urgency+0.16*relevance+0.14*velocity+0.12*novelty+0.10*actionability+0.08*exposure)*100) / 100
	priorityTier := classifyPriorityTier(priorityScore)

	var uncertaintyFlags []string
	if confidenceLabel == "single_source" || confidenceLabel == "unverified" {
		uncertaintyFlags = append(uncertaintyFlags, "single_independence_group")
	}
	if rec.TimePrecision == "unknown" {
		uncertaintyFlags = append(uncertaintyFlags, "imprecise_event_timestamp")
	}
	flagsJSON, _ := json.Marshal(uncertaintyFlags)

	rationale := fmt.Sprintf("pack=%s priority=%.1f (%s) independent_sources=%d evidence_count=%d confidence=%s",
		inst.PackID, priorityScore, priorityTier, independentSources, evidenceCount, confidenceLabel)

	assessment := RadarAssessment{
		ID:                 makeID("asm", eventID, assessedAt),
		EventID:            eventID,
		Impact:             impact,
		Urgency:            urgency,
		Relevance:          relevance,
		Velocity:           velocity,
		Novelty:            novelty,
		Actionability:      actionability,
		Exposure:           exposure,
		PriorityScore:      priorityScore,
		PriorityTier:       priorityTier,
		ConfidenceLabel:    confidenceLabel,
		IndependentSources: independentSources,
		UncertaintyFlags:   uncertaintyFlags,
		Rationale:          rationale,
		AssessedAt:         assessedAt,
	}

	_, err := e.hub.DB().ExecContext(ctx, `
INSERT INTO radar_assessments
  (id, event_id, impact, urgency, relevance, velocity, novelty, actionability, exposure, priority_score, priority_tier,
  	confidence_label, independent_sources, uncertainty_flags_json, rationale, assessed_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		assessment.ID, assessment.EventID, assessment.Impact, assessment.Urgency, assessment.Relevance,
		assessment.Velocity, assessment.Novelty, assessment.Actionability, assessment.Exposure,
		assessment.PriorityScore, assessment.PriorityTier, assessment.ConfidenceLabel,
		assessment.IndependentSources, string(flagsJSON), assessment.Rationale, assessment.AssessedAt,
	)
	if err != nil {
		return RadarAssessment{}, fmt.Errorf("insert assessment: %w", err)
	}
	return assessment, nil
}

func (e *Engine) routeAlert(ctx context.Context, instanceID, eventID string, assessment RadarAssessment,
	now string) (string, error) {
	alertKind := ""
	if assessment.PriorityScore >= 70 {
		if assessment.ConfidenceLabel == "single_source" || assessment.ConfidenceLabel == "unverified" {
			alertKind = "verification_required"
		} else {
			alertKind = "confirmed_alert"
		}
	} else if assessment.PriorityScore >= 45 {
		alertKind = "signal_notice"
	} else {
		return "", nil
	}

	var eventTitle string
	_ = e.hub.DB().QueryRowContext(ctx, `SELECT title FROM radar_events WHERE id = ?`, eventID).Scan(&eventTitle)
	dedupeKey := "alert:" + instanceID + ":" + eventID
	alertID := makeID("alt", dedupeKey)

	_, err := e.hub.DB().ExecContext(ctx, `
INSERT INTO radar_alerts (id, instance_id, event_id, assessment_id, dedupe_key, priority_tier, confidence_label,
	alert_kind, title, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'open', ?, ?)
ON CONFLICT(dedupe_key) DO UPDATE SET
  assessment_id = excluded.assessment_id,
  priority_tier = excluded.priority_tier,
  confidence_label = excluded.confidence_label,
  alert_kind = excluded.alert_kind,
  title = excluded.title,
  updated_at = excluded.updated_at`,
		alertID, instanceID, eventID, assessment.ID, dedupeKey,
		assessment.PriorityTier, assessment.ConfidenceLabel, alertKind, eventTitle, now, now,
	)
	if err != nil {
		return "", fmt.Errorf("upsert alert: %w", err)
	}
	var persistedID string
	_ = e.hub.DB().QueryRowContext(ctx, `SELECT id FROM radar_alerts WHERE dedupe_key = ?`, dedupeKey).Scan(&persistedID)
	return firstNonEmpty(persistedID, alertID), nil
}

// ListSignals returns recent signals optionally filtered by instanceID.
func (e *Engine) ListSignals(ctx context.Context, instanceID string, limit int) ([]RadarSignal, error) {
	limit = normalizeLimit(limit, 50, 200)
	query := `SELECT id, instance_id, lens_id, title, summary, state, baseline_status, detected_at FROM radar_signals`
	var args []any
	if strings.TrimSpace(instanceID) != "" {
		query += ` WHERE instance_id = ?`
		args = append(args, strings.TrimSpace(instanceID))
	}
	query += ` ORDER BY detected_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := e.hub.DB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	signals := make([]RadarSignal, 0)
	for rows.Next() {
		var sig RadarSignal
		if scanErr := rows.Scan(&sig.ID, &sig.InstanceID, &sig.LensID, &sig.Title, &sig.Summary, &sig.State,
			&sig.BaselineStatus, &sig.DetectedAt); scanErr != nil {
			return nil, scanErr
		}
		signals = append(signals, sig)
	}
	return signals, rows.Err()
}

// ListEvents returns clustered events optionally filtered by domain ID (e.g., "D06").
func (e *Engine) ListEvents(ctx context.Context, domain string, limit int) ([]RadarEvent, error) {
	limit = normalizeLimit(limit, 50, 200)
	normDomain := strings.ToUpper(strings.TrimSpace(domain))
	query := `
SELECT id, cluster_key, title, summary, primary_domain, domains_json, occurred_at, published_at, observed_at, ingested_at, time_precision, status, created_at, updated_at
FROM radar_events`
	var args []any
	if normDomain != "" {
		query += ` WHERE primary_domain = ? OR domains_json LIKE ?`
		args = append(args, normDomain, `%"`+normDomain+`"%`)
	}
	query += ` ORDER BY updated_at DESC, created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := e.hub.DB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]RadarEvent, 0)
	for rows.Next() {
		ev, scanErr := scanEvent(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		events = append(events, ev)
	}
	return events, rows.Err()
}

// GetEventDetail returns an event together with all linked evidence artifacts, latest assessment, and resolved entities.
func (e *Engine) GetEventDetail(ctx context.Context, eventID string) (EventDetail, error) {
	row := e.hub.DB().QueryRowContext(ctx, `
SELECT id, cluster_key, title, summary, primary_domain, domains_json, occurred_at, published_at, observed_at, ingested_at, time_precision, status, created_at, updated_at
FROM radar_events WHERE id = ?`, strings.TrimSpace(eventID))
	ev, err := scanEvent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return EventDetail{}, fmt.Errorf("%w: event %q", ErrNotFound, eventID)
	}
	if err != nil {
		return EventDetail{}, err
	}

	evRows, err := e.hub.DB().QueryContext(ctx, `
SELECT art.id, link.observation_id, link.role, link.independence_group, art.source_uri, art.content_hash, art.title, art.language, art.mime_type, art.payload_excerpt, art.captured_at
FROM radar_event_evidence link
JOIN radar_raw_artifacts art ON art.id = link.artifact_id
WHERE link.event_id = ?
ORDER BY art.captured_at ASC`, ev.ID)
	if err != nil {
		return EventDetail{}, err
	}
	defer evRows.Close()

	evidence := make([]EvidenceArtifact, 0)
	for evRows.Next() {
		var art EvidenceArtifact
		if scanErr := evRows.Scan(
			&art.ArtifactID, &art.ObservationID, &art.Role, &art.IndependenceGroup,
			&art.SourceURI, &art.ContentHash, &art.Title, &art.Language, &art.MimeType, &art.PayloadExcerpt, &art.CapturedAt,
		); scanErr != nil {
			return EventDetail{}, scanErr
		}
		evidence = append(evidence, art)
	}
	if err := evRows.Err(); err != nil {
		return EventDetail{}, err
	}

	var latestAssessment *RadarAssessment
	asmRow := e.hub.DB().QueryRowContext(ctx, `
SELECT id, event_id, impact, urgency, relevance, velocity, novelty, actionability, exposure, priority_score, priority_tier, confidence_label, independent_sources, uncertainty_flags_json, rationale, assessed_at
FROM radar_assessments WHERE event_id = ? ORDER BY assessed_at DESC LIMIT 1`, ev.ID)
	var asm RadarAssessment
	var flagsJSON string
	asmErr := asmRow.Scan(
		&asm.ID, &asm.EventID, &asm.Impact, &asm.Urgency, &asm.Relevance, &asm.Velocity,
		&asm.Novelty, &asm.Actionability, &asm.Exposure, &asm.PriorityScore, &asm.PriorityTier,
		&asm.ConfidenceLabel, &asm.IndependentSources, &flagsJSON, &asm.Rationale, &asm.AssessedAt,
	)
	if asmErr == nil {
		_ = json.Unmarshal([]byte(flagsJSON), &asm.UncertaintyFlags)
		latestAssessment = &asm
	} else if !errors.Is(asmErr, sql.ErrNoRows) {
		return EventDetail{}, asmErr
	}

	entRows, err := e.hub.DB().QueryContext(ctx, `
SELECT DISTINCT ent.id, ent.entity_type, ent.canonical_name, ent.strong_identifier, ent.aliases_json, ent.review_state, ent.created_at
FROM radar_relationships rel
JOIN radar_entities ent ON ent.id = rel.subject_entity_id
WHERE rel.object_entity_id = ?
ORDER BY ent.created_at ASC`, ev.ID)
	if err != nil {
		return EventDetail{}, err
	}
	defer entRows.Close()

	entities := make([]RadarEntity, 0)
	for entRows.Next() {
		ent, scanErr := scanEntity(entRows)
		if scanErr != nil {
			return EventDetail{}, scanErr
		}
		entities = append(entities, ent)
	}
	if err := entRows.Err(); err != nil {
		return EventDetail{}, err
	}

	return EventDetail{
		Event:      ev,
		Evidence:   evidence,
		Assessment: latestAssessment,
		Entities:   entities,
	}, nil
}

// ListEntities lists resolved entities optionally filtered by reviewState.
func (e *Engine) ListEntities(ctx context.Context, reviewState string, limit int) ([]RadarEntity, error) {
	limit = normalizeLimit(limit, 100, 300)
	query := `SELECT id, entity_type, canonical_name, strong_identifier, aliases_json, review_state, created_at FROM radar_entities`
	var args []any
	if strings.TrimSpace(reviewState) != "" {
		query += ` WHERE review_state = ?`
		args = append(args, strings.TrimSpace(reviewState))
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := e.hub.DB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entities := make([]RadarEntity, 0)
	for rows.Next() {
		ent, scanErr := scanEntity(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		entities = append(entities, ent)
	}
	return entities, rows.Err()
}

// ListEntityRelationships returns relationships where the given entity is subject or object.
func (e *Engine) ListEntityRelationships(ctx context.Context, entityID string) ([]RadarRelationship, error) {
	cleanID := strings.TrimSpace(entityID)
	rows, err := e.hub.DB().QueryContext(ctx, `
SELECT id, subject_entity_id, predicate, object_entity_id, confidence, evidence_artifact_id, created_at
FROM radar_relationships
WHERE subject_entity_id = ? OR object_entity_id = ?
ORDER BY created_at DESC`, cleanID, cleanID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	rels := make([]RadarRelationship, 0)
	for rows.Next() {
		var rel RadarRelationship
		if scanErr := rows.Scan(&rel.ID, &rel.SubjectEntityID, &rel.Predicate, &rel.ObjectEntityID, &rel.Confidence,
			&rel.EvidenceArtifactID, &rel.CreatedAt); scanErr != nil {
			return nil, scanErr
		}
		rels = append(rels, rel)
	}
	return rels, rows.Err()
}

// ListAlerts returns alerts optionally filtered by status.
func (e *Engine) ListAlerts(ctx context.Context, status string, limit int) ([]RadarAlert, error) {
	limit = normalizeLimit(limit, 50, 200)
	query := `SELECT id, instance_id, event_id, assessment_id, dedupe_key, priority_tier, confidence_label, alert_kind, title, status, created_at, updated_at FROM radar_alerts`
	var args []any
	if strings.TrimSpace(status) != "" {
		query += ` WHERE status = ?`
		args = append(args, strings.TrimSpace(status))
	}
	query += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := e.hub.DB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	alerts := make([]RadarAlert, 0)
	for rows.Next() {
		var alt RadarAlert
		if scanErr := rows.Scan(&alt.ID, &alt.InstanceID, &alt.EventID, &alt.AssessmentID, &alt.DedupeKey, &alt.PriorityTier,
			&alt.ConfidenceLabel, &alt.AlertKind, &alt.Title, &alt.Status, &alt.CreatedAt, &alt.UpdatedAt); scanErr != nil {
			return nil, scanErr
		}
		alerts = append(alerts, alt)
	}
	return alerts, rows.Err()
}

// AcknowledgeAlert transitions an alert to acknowledged status and logs an audit entry.
func (e *Engine) AcknowledgeAlert(ctx context.Context, alertID, actor string) (RadarAlert, error) {
	cleanID := strings.TrimSpace(alertID)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	execResult, err := e.hub.DB().ExecContext(ctx, `
UPDATE radar_alerts SET status = 'acknowledged', updated_at = ? WHERE id = ?`, now, cleanID)
	if err != nil {
		return RadarAlert{}, err
	}
	affected, _ := execResult.RowsAffected()
	if affected == 0 {
		return RadarAlert{}, fmt.Errorf("%w: alert %q", ErrNotFound, cleanID)
	}
	e.writeAudit(ctx, firstNonEmpty(actor, "operator"), "acknowledge_alert", "radar_alert", cleanID, "acknowledged")

	var alt RadarAlert
	row := e.hub.DB().QueryRowContext(ctx, `
SELECT id, instance_id, event_id, assessment_id, dedupe_key, priority_tier, confidence_label, alert_kind, title, status, created_at, updated_at
FROM radar_alerts WHERE id = ?`, cleanID)
	if scanErr := row.Scan(&alt.ID, &alt.InstanceID, &alt.EventID, &alt.AssessmentID, &alt.DedupeKey, &alt.PriorityTier,
		&alt.ConfidenceLabel, &alt.AlertKind, &alt.Title, &alt.Status, &alt.CreatedAt, &alt.UpdatedAt); scanErr != nil {
		return RadarAlert{}, scanErr
	}
	return alt, nil
}

// ListReports returns compiled intelligence reports optionally filtered by instanceID.
func (e *Engine) ListReports(ctx context.Context, instanceID string, limit int) ([]RadarReport, error) {
	limit = normalizeLimit(limit, 25, 100)
	query := `SELECT id, instance_id, title, scope, observed_facts_json, reported_claims_json, inferences_json, unknowns_json, evidence_refs_json, model, created_at FROM radar_reports`
	var args []any
	if strings.TrimSpace(instanceID) != "" {
		query += ` WHERE instance_id = ?`
		args = append(args, strings.TrimSpace(instanceID))
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := e.hub.DB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	reports := make([]RadarReport, 0)
	for rows.Next() {
		rep, scanErr := scanReport(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		reports = append(reports, rep)
	}
	return reports, rows.Err()
}

// GenerateReport compiles a Section 11.1 evidence-grounded report for an instance,
// optionally synthesizing via GatewayURL with strict validation and deterministic fallback.
func (e *Engine) GenerateReport(ctx context.Context, instanceID, title, scope string) (RadarReport, error) {
	inst, err := e.GetInstance(ctx, instanceID)
	if err != nil {
		return RadarReport{}, err
	}

	rows, err := e.hub.DB().QueryContext(ctx, `
SELECT DISTINCT ev.id, ev.title, ev.summary, ev.occurred_at, art.id, art.source_uri, art.independence_group,
  COALESCE(asm.confidence_label, 'unverified'), COALESCE(asm.priority_score, 0)
FROM radar_observations obs
JOIN radar_event_evidence link ON link.observation_id = obs.id
JOIN radar_events ev ON ev.id = link.event_id
JOIN radar_raw_artifacts art ON art.id = link.artifact_id
LEFT JOIN radar_assessments asm ON asm.event_id = ev.id
WHERE obs.instance_id = ?
ORDER BY ev.updated_at DESC, art.captured_at ASC
LIMIT 30`, inst.ID)
	if err != nil {
		return RadarReport{}, err
	}
	defer rows.Close()

	type groundedRow struct {
		eventID    string
		title      string
		summary    string
		occurredAt string
		artifactID string
		sourceURI  string
		indGroup   string
		confidence string
		priority   float64
	}

	var evidenceRows []groundedRow
	seenArtifacts := make(map[string]bool)
	var evidenceRefs []string
	for rows.Next() {
		var row groundedRow
		if scanErr := rows.Scan(&row.eventID, &row.title, &row.summary, &row.occurredAt, &row.artifactID, &row.sourceURI,
			&row.indGroup, &row.confidence, &row.priority); scanErr != nil {
			return RadarReport{}, scanErr
		}
		evidenceRows = append(evidenceRows, row)
		if !seenArtifacts[row.artifactID] {
			seenArtifacts[row.artifactID] = true
			evidenceRefs = append(evidenceRefs, row.artifactID)
		}
	}
	if err := rows.Err(); err != nil {
		return RadarReport{}, err
	}

	resolvedTitle := firstNonEmpty(strings.TrimSpace(title), inst.Name+" Intelligence Brief")
	resolvedScope := firstNonEmpty(strings.TrimSpace(scope), strings.Join(inst.Domains, ","))
	now := time.Now().UTC().Format(time.RFC3339Nano)

	report := RadarReport{
		ID:           makeID("rep", inst.ID, resolvedTitle, now),
		InstanceID:   inst.ID,
		Title:        resolvedTitle,
		Scope:        resolvedScope,
		EvidenceRefs: evidenceRefs,
		Model:        "deterministic-evidence-compiler",
		CreatedAt:    now,
	}

	if e.gatewayURL != "" && len(evidenceRows) > 0 {
		gatewayReport, usedGateway, gwErr := e.tryGatewayReport(ctx, inst, resolvedTitle, resolvedScope, evidenceRows,
			evidenceRefs)
		if gwErr != nil {
			return RadarReport{}, gwErr
		}
		if usedGateway {
			gatewayReport.ID = report.ID
			gatewayReport.InstanceID = inst.ID
			gatewayReport.Title = resolvedTitle
			gatewayReport.Scope = resolvedScope
			gatewayReport.CreatedAt = now
			if len(gatewayReport.EvidenceRefs) == 0 {
				gatewayReport.EvidenceRefs = evidenceRefs
			}
			if valErr := e.ValidateReport(ctx, gatewayReport); valErr != nil {
				return RadarReport{}, valErr
			}
			if saveErr := e.persistReport(ctx, gatewayReport); saveErr != nil {
				return RadarReport{}, saveErr
			}
			return gatewayReport, nil
		}
	}

	seenStatements := make(map[string]bool)
	for _, row := range evidenceRows {
		stmt := fmt.Sprintf("[%s] %s (occurred_at=%s, group=%s)", row.artifactID, row.title, row.occurredAt, row.indGroup)
		if !seenStatements[row.artifactID] {
			seenStatements[row.artifactID] = true
			report.ObservedFacts = append(report.ObservedFacts, ReportFact{
				Statement:    stmt,
				EvidenceRefs: []string{row.artifactID},
			})
		}
		if row.confidence == "single_source" || row.confidence == "unverified" {
			report.ReportedClaims = append(report.ReportedClaims, fmt.Sprintf("Unconfirmed single-group claim (%s): %s",
				row.confidence, row.title))
			report.Unknowns = append(report.Unknowns, fmt.Sprintf("Independent corroboration missing for event %s (%s)",
				row.eventID, row.title))
		} else {
			report.Inferences = append(report.Inferences, fmt.Sprintf("Corroborated (%s, priority=%.1f): %s", row.confidence,
				row.priority, row.title))
		}
	}

	if len(report.ObservedFacts) == 0 {
		report.Unknowns = append(report.Unknowns, "No verified evidence artifacts collected yet for instance "+inst.ID)
	}

	if err := e.ValidateReport(ctx, report); err != nil {
		return RadarReport{}, err
	}
	if err := e.persistReport(ctx, report); err != nil {
		return RadarReport{}, err
	}
	return report, nil
}

// ValidateReport enforces Section 11.1 grounding: every item in ObservedFacts MUST cite
// at least one real persisted evidence artifact ID that also appears in EvidenceRefs.
func (e *Engine) ValidateReport(ctx context.Context, report RadarReport) error {
	refIndex := make(map[string]bool, len(report.EvidenceRefs))
	for _, ref := range report.EvidenceRefs {
		clean := strings.TrimSpace(ref)
		if clean != "" {
			refIndex[clean] = true
		}
	}

	for idx, fact := range report.ObservedFacts {
		if strings.TrimSpace(fact.Statement) == "" {
			return fmt.Errorf("%w: observedFacts[%d] statement is empty", ErrValidation, idx)
		}
		if len(fact.EvidenceRefs) == 0 {
			return fmt.Errorf("%w: observedFacts[%d] (%q) has no evidenceRefs", ErrValidation, idx, fact.Statement)
		}
		for _, ref := range fact.EvidenceRefs {
			cleanRef := strings.TrimSpace(ref)
			if cleanRef == "" {
				return fmt.Errorf("%w: observedFacts[%d] contains empty evidenceRef", ErrValidation, idx)
			}
			if !refIndex[cleanRef] {
				return fmt.Errorf("%w: observedFacts[%d] references %q absent from report evidenceRefs", ErrValidation, idx,
					cleanRef)
			}
			var count int
			if err := e.hub.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM radar_raw_artifacts WHERE id = ?`, cleanRef).Scan(&count); err != nil {
				return fmt.Errorf("verify evidence ref %q: %w", cleanRef, err)
			}
			if count == 0 {
				return fmt.Errorf("%w: observedFacts[%d] references non-existent artifact %q", ErrValidation, idx, cleanRef)
			}
		}
	}
	return nil
}

func (e *Engine) tryGatewayReport(ctx context.Context, inst RadarInstance, title, scope string,
	evidenceRows interface{}, evidenceRefs []string) (RadarReport, bool, error) {
	evidenceJSON, _ := json.Marshal(evidenceRows)
	reqBody, _ := json.Marshal(map[string]any{
		"model": "default",
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": "Compile a JSON report with keys observedFacts ([{statement,evidenceRefs}]), reportedClaims ([]string), inferences ([]string), unknowns ([]string), evidenceRefs ([]string). Every observedFact must cite valid artifact IDs.",
			},
			{
				"role": "user",
				"content": fmt.Sprintf("Instance=%s Title=%s Scope=%s ValidEvidenceIDs=%v Evidence=%s", inst.ID, title, scope,
					evidenceRefs, string(evidenceJSON)),
			},
		},
	})

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, e.gatewayURL, bytes.NewReader(reqBody))
	if err != nil {
		return RadarReport{}, false, nil
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := e.httpClient.Do(httpReq)
	if err != nil {
		return RadarReport{}, false, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusNotFound {
		return RadarReport{}, false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return RadarReport{}, false, nil
	}

	rawResp, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if readErr != nil {
		return RadarReport{}, false, nil
	}

	var completion struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rawResp, &completion); err != nil || len(completion.Choices) == 0 {
		return RadarReport{}, false, nil
	}

	var parsed RadarReport
	if err := json.Unmarshal([]byte(strings.TrimSpace(completion.Choices[0].Message.Content)), &parsed); err != nil {
		return RadarReport{}, false, fmt.Errorf("%w: gateway returned invalid report JSON: %v", ErrValidation, err)
	}
	parsed.Model = firstNonEmpty(completion.Model, "gateway")
	return parsed, true, nil
}

func (e *Engine) persistReport(ctx context.Context, report RadarReport) error {
	factsJSON, _ := json.Marshal(report.ObservedFacts)
	claimsJSON, _ := json.Marshal(report.ReportedClaims)
	infJSON, _ := json.Marshal(report.Inferences)
	unkJSON, _ := json.Marshal(report.Unknowns)
	refsJSON, _ := json.Marshal(report.EvidenceRefs)

	_, err := e.hub.DB().ExecContext(ctx, `
INSERT INTO radar_reports
  (id, instance_id, title, scope, observed_facts_json, reported_claims_json, inferences_json, unknowns_json,
  	evidence_refs_json, model, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		report.ID, report.InstanceID, report.Title, report.Scope,
		string(factsJSON), string(claimsJSON), string(infJSON), string(unkJSON), string(refsJSON),
		report.Model, report.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert report: %w", err)
	}
	e.writeAudit(ctx, "system", "generate_report", "radar_report", report.ID, "ok")
	return nil
}

// ListConnectorHealth returns operational health records for all connectors.
func (e *Engine) ListConnectorHealth(ctx context.Context) ([]ConnectorHealth, error) {
	rows, err := e.hub.DB().QueryContext(ctx, `
SELECT connector_id, last_attempt_at, last_success_at, last_status, last_error, total_runs, failed_runs, rate_limited_until
FROM radar_connector_health ORDER BY connector_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	healthList := make([]ConnectorHealth, 0)
	for rows.Next() {
		var h ConnectorHealth
		if scanErr := rows.Scan(&h.ConnectorID, &h.LastAttemptAt, &h.LastSuccessAt, &h.LastStatus, &h.LastError, &h.TotalRuns,
			&h.FailedRuns, &h.RateLimitedUntil); scanErr != nil {
			return nil, scanErr
		}
		healthList = append(healthList, h)
	}
	return healthList, rows.Err()
}

// SubmitFeedback persists operator feedback on a radar target object.
func (e *Engine) SubmitFeedback(ctx context.Context, input FeedbackInput) (FeedbackRecord, error) {
	targetKind := strings.TrimSpace(input.TargetKind)
	targetID := strings.TrimSpace(input.TargetID)
	vote := strings.ToLower(strings.TrimSpace(input.Vote))
	if targetKind == "" || targetID == "" || vote == "" {
		return FeedbackRecord{}, fmt.Errorf("%w: targetKind, targetId, and vote are required", ErrValidation)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rec := FeedbackRecord{
		ID:         makeID("fb", targetKind, targetID, vote, now),
		TargetKind: targetKind,
		TargetID:   targetID,
		Vote:       vote,
		Reason:     strings.TrimSpace(input.Reason),
		CreatedAt:  now,
	}
	_, err := e.hub.DB().ExecContext(ctx, `
INSERT INTO radar_feedback (id, target_kind, target_id, vote, reason, created_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.TargetKind, rec.TargetID, rec.Vote, rec.Reason, rec.CreatedAt,
	)
	if err != nil {
		return FeedbackRecord{}, err
	}
	e.writeAudit(ctx, "operator", "submit_feedback", targetKind, targetID, vote)
	return rec, nil
}

func (e *Engine) isConnectorRateLimited(ctx context.Context, connectorID string, now time.Time) (string, bool) {
	var limitedUntil string
	err := e.hub.DB().QueryRowContext(ctx, `
SELECT rate_limited_until FROM radar_connector_health WHERE connector_id = ?`, connectorID,
	).Scan(&limitedUntil)
	if err != nil || strings.TrimSpace(limitedUntil) == "" {
		return "", false
	}
	parsed, parseErr := time.Parse(time.RFC3339Nano, limitedUntil)
	if parseErr != nil {
		return "", false
	}
	if parsed.After(now) {
		return limitedUntil, true
	}
	return "", false
}

func (e *Engine) updateConnectorHealth(ctx context.Context, connectorID string, collectResult CollectResult,
	now time.Time) {
	nowStr := now.UTC().Format(time.RFC3339Nano)
	successAt := ""
	failedDelta := 0
	rateLimitedUntil := ""

	switch collectResult.Status {
	case RunStatusOK, RunStatusNoNewData:
		successAt = nowStr
	case RunStatusRateLimited:
		failedDelta = 1
		retry := collectResult.RetryAfter
		if retry <= 0 {
			retry = 60 * time.Second
		}
		rateLimitedUntil = now.UTC().Add(retry).Format(time.RFC3339Nano)
	default:
		failedDelta = 1
	}

	_, _ = e.hub.DB().ExecContext(ctx, `
INSERT INTO radar_connector_health
  (connector_id, last_attempt_at, last_success_at, last_status, last_error, total_runs, failed_runs, rate_limited_until)
VALUES (?, ?, ?, ?, ?, 1, ?, ?)
ON CONFLICT(connector_id) DO UPDATE SET
  last_attempt_at = excluded.last_attempt_at,
  last_success_at = CASE WHEN excluded.last_success_at != '' THEN excluded.last_success_at ELSE radar_connector_health.last_success_at END,
  last_status = excluded.last_status,
  last_error = excluded.last_error,
  total_runs = radar_connector_health.total_runs + 1,
  failed_runs = radar_connector_health.failed_runs + excluded.failed_runs,
  rate_limited_until = excluded.rate_limited_until`,
		connectorID, nowStr, successAt, collectResult.Status, collectResult.ErrorMessage, failedDelta, rateLimitedUntil,
	)
}

func (e *Engine) recordCollectionRun(ctx context.Context, instanceID, targetID, status string, recordsAcquired int,
	errMsg, startedAt, finishedAt string) {
	runID := makeID("run", instanceID, targetID, startedAt)
	_, _ = e.hub.DB().ExecContext(ctx, `
INSERT INTO radar_collection_runs (id, instance_id, target_id, status, records_acquired, error_message, started_at,
	finished_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		runID, instanceID, targetID, status, recordsAcquired, errMsg, startedAt, finishedAt,
	)
}

func (e *Engine) writeAudit(ctx context.Context, actor, action, objectType, objectID, outcome string) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	auditID := makeID("aud", actor, action, objectID, now)
	_, _ = e.hub.DB().ExecContext(ctx, `
INSERT INTO radar_audit_logs (id, actor, action, object_type, object_id, outcome, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		auditID, actor, action, objectType, objectID, outcome, now,
	)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanInstance(scanner rowScanner) (RadarInstance, error) {
	var inst RadarInstance
	err := scanner.Scan(
		&inst.ID, &inst.PackID, &inst.PackVersion, &inst.Name,
		&inst.Mode, &inst.Status, &inst.ConfigJSON, &inst.CreatedAt, &inst.UpdatedAt,
	)
	if err != nil {
		return RadarInstance{}, err
	}
	var cfg struct {
		Domains []string `json:"domains"`
		Lenses  []string `json:"lenses"`
	}
	_ = json.Unmarshal([]byte(inst.ConfigJSON), &cfg)
	inst.Domains = cfg.Domains
	inst.Lenses = cfg.Lenses
	return inst, nil
}

func scanWatchTarget(scanner rowScanner) (WatchTarget, error) {
	var tgt WatchTarget
	var enabledInt int
	err := scanner.Scan(
		&tgt.ID, &tgt.InstanceID, &tgt.TargetType, &tgt.CanonicalName,
		&tgt.ConnectorID, &tgt.SourceURI, &tgt.IndependenceGroup,
		&tgt.ETag, &tgt.Cursor, &enabledInt, &tgt.CreatedAt,
	)
	if err != nil {
		return WatchTarget{}, err
	}
	tgt.Enabled = enabledInt != 0
	return tgt, nil
}

func scanEvent(scanner rowScanner) (RadarEvent, error) {
	var ev RadarEvent
	var domainsJSON string
	err := scanner.Scan(
		&ev.ID, &ev.ClusterKey, &ev.Title, &ev.Summary, &ev.PrimaryDomain, &domainsJSON,
		&ev.OccurredAt, &ev.PublishedAt, &ev.ObservedAt, &ev.IngestedAt,
		&ev.TimePrecision, &ev.Status, &ev.CreatedAt, &ev.UpdatedAt,
	)
	if err != nil {
		return RadarEvent{}, err
	}
	_ = json.Unmarshal([]byte(domainsJSON), &ev.Domains)
	return ev, nil
}

func scanEntity(scanner rowScanner) (RadarEntity, error) {
	var ent RadarEntity
	var aliasesJSON string
	err := scanner.Scan(
		&ent.ID, &ent.EntityType, &ent.CanonicalName, &ent.StrongIdentifier,
		&aliasesJSON, &ent.ReviewState, &ent.CreatedAt,
	)
	if err != nil {
		return RadarEntity{}, err
	}
	_ = json.Unmarshal([]byte(aliasesJSON), &ent.Aliases)
	return ent, nil
}

func scanReport(scanner rowScanner) (RadarReport, error) {
	var rep RadarReport
	var factsJSON, claimsJSON, infJSON, unkJSON, refsJSON string
	err := scanner.Scan(
		&rep.ID, &rep.InstanceID, &rep.Title, &rep.Scope,
		&factsJSON, &claimsJSON, &infJSON, &unkJSON, &refsJSON, &rep.Model, &rep.CreatedAt,
	)
	if err != nil {
		return RadarReport{}, err
	}
	_ = json.Unmarshal([]byte(factsJSON), &rep.ObservedFacts)
	_ = json.Unmarshal([]byte(claimsJSON), &rep.ReportedClaims)
	_ = json.Unmarshal([]byte(infJSON), &rep.Inferences)
	_ = json.Unmarshal([]byte(unkJSON), &rep.Unknowns)
	_ = json.Unmarshal([]byte(refsJSON), &rep.EvidenceRefs)
	return rep, nil
}

func deriveIndependenceGroup(sourceURI, connectorID string) string {
	parsed, err := url.Parse(strings.TrimSpace(sourceURI))
	if err == nil && parsed.Host != "" {
		return strings.ToLower(strings.TrimPrefix(parsed.Host, "www."))
	}
	return firstNonEmpty(connectorID, "unknown-group")
}

func normalizeClusterText(raw string) string {
	lowered := strings.ToLower(strings.TrimSpace(raw))
	fields := strings.Fields(lowered)
	return strings.Join(fields, " ")
}

func normalizeEntityName(raw string) string {
	return normalizeClusterText(raw)
}

func mergeDomains(primaryList, secondaryList []string) []string {
	merged := make([]string, 0, len(primaryList)+len(secondaryList))
	seen := make(map[string]bool)
	for _, list := range [][]string{primaryList, secondaryList} {
		for _, dom := range list {
			norm := strings.ToUpper(strings.TrimSpace(dom))
			if _, ok := GetDomain(norm); ok && !seen[norm] {
				seen[norm] = true
				merged = append(merged, norm)
			}
		}
	}
	return merged
}

func earliestRFC3339(first, second string) string {
	t1, err1 := time.Parse(time.RFC3339Nano, strings.TrimSpace(first))
	t2, err2 := time.Parse(time.RFC3339Nano, strings.TrimSpace(second))
	if err1 != nil && err2 != nil {
		return firstNonEmpty(first, second)
	}
	if err1 != nil {
		return second
	}
	if err2 != nil {
		return first
	}
	if t2.Before(t1) {
		return second
	}
	return first
}

func inferLensForRecord(packLenses []string, rec CollectedRecord) string {
	text := strings.ToLower(rec.Title + " " + rec.Summary)
	candidates := []struct {
		lensID   string
		keywords []string
	}{
		{"L14", []string{"outage", "disruption", "down", "offline", "halt"}},
		{"L17", []string{"cve", "vulnerability", "exploit", "zero-day", "breach"}},
		{"L18", []string{"release", "v1.", "v2.", "launched", "milestone"}},
		{"L12", []string{"fine", "enforcement", "mandate", "ruling", "regulation", "sanction"}},
		{"L04", []string{"breaking", "deprecated", "shift", "change"}},
		{"L05", []string{"risk", "threat", "critical", "attack", "warning"}},
	}
	packSet := make(map[string]bool, len(packLenses))
	for _, lens := range packLenses {
		packSet[lens] = true
	}
	for _, cand := range candidates {
		if len(packSet) > 0 && !packSet[cand.lensID] {
			continue
		}
		for _, kw := range cand.keywords {
			if strings.Contains(text, kw) {
				return cand.lensID
			}
		}
	}
	if len(packLenses) > 0 {
		return packLenses[0]
	}
	return "L02"
}

func scorePriorityAxes(rec CollectedRecord, evidenceCount, independentSources int) (impact, urgency, relevance,
	velocity, novelty, actionability, exposure float64) {
	text := strings.ToLower(rec.Title + " " + rec.Summary)
	impact = 50
	urgency = 45
	relevance = 65
	velocity = 40 + math.Min(40, float64(evidenceCount)*10)
	novelty = 65
	if independentSources <= 1 {
		novelty = 75
	}
	actionability = 55
	exposure = 50

	if containsAny(text, "critical", "outage", "breach", "zero-day", "emergency", "cve", "sanction", "disruption",
		"shutdown") {
		impact = 85
		urgency = 85
		actionability = 80
		exposure = 80
	} else if containsAny(text, "release", "breaking", "vulnerability", "enforcement", "major") {
		impact = 72
		urgency = 68
		actionability = 72
		exposure = 68
	}

	if rec.ImpactHint > 0 {
		val := rec.ImpactHint
		if val <= 1.0 {
			val *= 100
		}
		impact = clampScore(val)
	}
	if rec.UrgencyHint > 0 {
		val := rec.UrgencyHint
		if val <= 1.0 {
			val *= 100
		}
		urgency = clampScore(val)
	}
	return impact, urgency, relevance, velocity, novelty, actionability, exposure
}

func classifyPriorityTier(score float64) string {
	switch {
	case score >= 85:
		return "critical"
	case score >= 70:
		return "high"
	case score >= 45:
		return "medium"
	default:
		return "low"
	}
}

func containsAny(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}

func clampScore(val float64) float64 {
	if val < 0 {
		return 0
	}
	if val > 100 {
		return 100
	}
	return val
}

func normalizeLimit(limit, defaultVal, maxVal int) int {
	if limit <= 0 {
		return defaultVal
	}
	if limit > maxVal {
		return maxVal
	}
	return limit
}

func hashStrings(parts ...string) string {
	hasher := sha256.New()
	for _, part := range parts {
		hasher.Write([]byte(part))
		hasher.Write([]byte{0})
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func makeID(prefix string, parts ...string) string {
	sum := hashStrings(parts...)
	return prefix + "-" + sum[:20]
}

// SortedIDs returns a sorted copy of a string slice for deterministic output.
func SortedIDs(values []string) []string {
	out := make([]string, len(values))
	copy(out, values)
	sort.Strings(out)
	return out
}

func parsePositiveInt(raw string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}
