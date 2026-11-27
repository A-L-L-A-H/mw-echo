package worldradar

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/worldradar/connectors"
	"github.com/google/uuid"
)

// RadarEngineConfig mirrors Section 6.1 configuration for the World Radar vertical slice.
type RadarEngineConfig struct {
	InstanceID              string
	TaxonomyVersion         string
	MaxConcurrentIngestions int
	DefaultPollInterval     time.Duration
	CheckpointBatchSize     int
	QuarantineOnDrift       bool
	Scorer                  ScorerConfig
	Dedupe                  DedupeConfig
	Privacy                 PrivacyConfig
	Freshness               FreshnessConfig
}

// ScorerConfig configures ConfidenceScorer weights and band thresholds (Section 6.1).
type ScorerConfig struct {
	Version                    string
	WeightSourceReliability    float64
	WeightCorroboration        float64
	WeightProvenanceDepth      float64
	WeightExtractionQuality    float64
	WeightTemporalValidity     float64
	ContradictionPenaltyFactor float64
	BandHigh                   float64
	BandModerate               float64
	BandLow                    float64
}

// DedupeConfig configures Deduplicator thresholds and independence-group handling.
type DedupeConfig struct {
	AutoMergeThreshold   float64
	ProvisionalThreshold float64
	IndependenceGroups   bool
}

// PrivacyConfig configures the PII privacy gate.
type PrivacyConfig struct {
	PIIDetection           bool
	PseudonymizeHandles    bool
	FuzzLocationPrecisionM float64
	ExcludeSpecialCategory bool
}

// TierSLO defines capture and normalization freshness targets per source tier.
type TierSLO struct {
	Capture    time.Duration `json:"capture"`
	Normalized time.Duration `json:"normalized"`
}

// FreshnessConfig configures freshness SLO targets and stale source alerting.
type FreshnessConfig struct {
	SLOTargets           map[string]TierSLO
	StaleAlertHours      int
	HistoricalCutoffDays int
}

// DefaultRadarEngineConfig returns the normative Section 6.1 configuration.
func DefaultRadarEngineConfig() RadarEngineConfig {
	return RadarEngineConfig{
		TaxonomyVersion:         "v1.0.0",
		MaxConcurrentIngestions: 4,
		DefaultPollInterval:     10 * time.Minute,
		CheckpointBatchSize:     50,
		Scorer: ScorerConfig{
			Version:                    "v1.0.0",
			WeightSourceReliability:    0.30,
			WeightCorroboration:        0.25,
			WeightProvenanceDepth:      0.15,
			WeightExtractionQuality:    0.15,
			WeightTemporalValidity:     0.15,
			ContradictionPenaltyFactor: 0.35,
			BandHigh:                   0.85,
			BandModerate:               0.65,
			BandLow:                    0.40,
		},
		Dedupe: DedupeConfig{
			AutoMergeThreshold:   0.93,
			ProvisionalThreshold: 0.82,
			IndependenceGroups:   true,
		},
		Privacy: PrivacyConfig{
			PIIDetection:           true,
			PseudonymizeHandles:    true,
			FuzzLocationPrecisionM: 1000,
			ExcludeSpecialCategory: true,
		},
		Freshness: FreshnessConfig{
			SLOTargets: map[string]TierSLO{
				"A": {Capture: 6 * time.Hour, Normalized: 12 * time.Hour},
				"B": {Capture: 6 * time.Hour, Normalized: 12 * time.Hour},
				"C": {Capture: 10 * time.Minute, Normalized: 30 * time.Minute},
				"D": {Capture: 10 * time.Minute, Normalized: 30 * time.Minute},
				"E": {Capture: 5 * time.Minute, Normalized: 15 * time.Minute},
				"F": {Capture: 10 * time.Minute, Normalized: 30 * time.Minute},
				"G": {Capture: 30 * time.Minute, Normalized: 60 * time.Minute},
			},
			StaleAlertHours:      24,
			HistoricalCutoffDays: 30,
		},
	}
}

// InstanceCreateRequest creates a row in spec radar_instances.
type InstanceCreateRequest struct {
	InstanceID      string `json:"instance_id,omitempty"`
	Name            string `json:"name"`
	Description     string `json:"description,omitempty"`
	Version         string `json:"version"`
	TaxonomyVersion string `json:"taxonomy_version"`
	ConfigJSON      string `json:"config_json"`
	Status          string `json:"status,omitempty"`
}

// SpecInstance mirrors the Section 2.1/4.2 Instance schema.
type SpecInstance struct {
	InstanceID      string  `json:"instance_id"`
	Name            string  `json:"name"`
	Description     string  `json:"description,omitempty"`
	Version         string  `json:"version"`
	TaxonomyVersion string  `json:"taxonomy_version"`
	ConfigJSON      string  `json:"config_json"`
	Status          string  `json:"status"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
	ValidFrom       string  `json:"valid_from"`
	ValidTo         *string `json:"valid_to"`
}

// SourceConfig configures a source registration in RadarEngine.RegisterSource.
type SourceConfig struct {
	SourceID          string  `json:"source_id,omitempty"`
	InstanceID        string  `json:"instance_id"`
	SourceTier        string  `json:"source_tier"`
	Category          string  `json:"category"`
	TrustBase         float64 `json:"trust_base"`
	CollectionMethod  string  `json:"collection_method"`
	LicenseClass      string  `json:"license_class"`
	LanguageHint      string  `json:"language_hint,omitempty"`
	GeographyScope    string  `json:"geography_scope"`
	Status            string  `json:"status,omitempty"`
	IndependenceGroup string  `json:"independence_group,omitempty"`
	ConfigJSON        string  `json:"config_json"`
}

// SpecSource mirrors the Section 2.1/4.2 Source schema.
type SpecSource struct {
	SourceID          string  `json:"source_id"`
	InstanceID        string  `json:"instance_id"`
	SourceTier        string  `json:"source_tier"`
	Category          string  `json:"category"`
	TrustBase         float64 `json:"trust_base"`
	CollectionMethod  string  `json:"collection_method"`
	LicenseClass      string  `json:"license_class"`
	LanguageHint      string  `json:"language_hint,omitempty"`
	GeographyScope    string  `json:"geography_scope"`
	Status            string  `json:"status"`
	IndependenceGroup string  `json:"independence_group,omitempty"`
	ConfigJSON        string  `json:"config_json"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
	ValidFrom         string  `json:"valid_from"`
	ValidTo           *string `json:"valid_to"`
}

// SpecClaim mirrors the Section 2.1/4.2 Claim schema.
type SpecClaim struct {
	ClaimID          string  `json:"claim_id"`
	ObservationID    string  `json:"observation_id"`
	ClaimType        string  `json:"claim_type"`
	ClaimText        string  `json:"claim_text"`
	EntitiesJSON     string  `json:"entities_json"`
	MetricsJSON      string  `json:"metrics_json,omitempty"`
	TimeRangeStart   string  `json:"time_range_start,omitempty"`
	TimeRangeEnd     string  `json:"time_range_end,omitempty"`
	PlaceJSON        string  `json:"place_json,omitempty"`
	Confidence       float64 `json:"confidence"`
	ConfidenceBand   string  `json:"confidence_band"`
	ExtractorVersion string  `json:"extractor_version"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
	ValidFrom        string  `json:"valid_from"`
	ValidTo          *string `json:"valid_to"`
}

// SpecEvent mirrors the Section 2.1/4.2 Event schema.
type SpecEvent struct {
	EventID        string  `json:"event_id"`
	EventType      string  `json:"event_type"`
	EventStatus    string  `json:"event_status"`
	TimeRangeStart string  `json:"time_range_start"`
	TimeRangeEnd   *string `json:"time_range_end,omitempty"`
	PlaceJSON      string  `json:"place_json"`
	ActorsJSON     string  `json:"actors_json"`
	ClaimsJSON     string  `json:"claims_json"`
	MetricsJSON    string  `json:"metrics_json,omitempty"`
	TopicsJSON     string  `json:"topics_json"`
	Confidence     float64 `json:"confidence"`
	ConfidenceBand string  `json:"confidence_band"`
	Version        int     `json:"version"`
	ScorerVersion  string  `json:"scorer_version"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
	ValidFrom      string  `json:"valid_from"`
	ValidTo        *string `json:"valid_to"`
}

// ProvenanceRecord mirrors the Section 2.1/4.2 ProvenanceRecord schema.
type ProvenanceRecord struct {
	ProvenanceID         string  `json:"provenance_id"`
	ObservationID        string  `json:"observation_id"`
	SourceID             string  `json:"source_id"`
	SourceVersion        string  `json:"source_version"`
	CaptureMethod        string  `json:"capture_method"`
	CapturedAt           string  `json:"captured_at"`
	CanonicalURL         string  `json:"canonical_url,omitempty"`
	RawHash              string  `json:"raw_hash"`
	ContentHash          string  `json:"content_hash"`
	ParserVersion        string  `json:"parser_version"`
	NormalizerVersion    string  `json:"normalizer_version"`
	DedupeVersion        string  `json:"dedupe_version"`
	ScorerVersion        string  `json:"scorer_version"`
	TransformParentsJSON string  `json:"transform_parents_json,omitempty"`
	ExtractedAt          string  `json:"extracted_at"`
	LicenseClass         string  `json:"license_class"`
	PrivacyClass         string  `json:"privacy_class"`
	ReasonCodesJSON      string  `json:"reason_codes_json,omitempty"`
	CreatedAt            string  `json:"created_at"`
	UpdatedAt            string  `json:"updated_at"`
	ValidFrom            string  `json:"valid_from"`
	ValidTo              *string `json:"valid_to"`
}

// SpecEventDetail mirrors Section 4.2 EventDetail.
type SpecEventDetail struct {
	SpecEvent
	Provenance []ProvenanceRecord `json:"provenance"`
	Claims     []SpecClaim        `json:"claims"`
}

// EventQuery defines filters for QueryEvents (Section 4.1 & 4.2).
type EventQuery struct {
	InstanceID     string    `json:"instance_id,omitempty"`
	Domain         string    `json:"domain,omitempty"`
	Lens           string    `json:"lens,omitempty"`
	ConfidenceMin  float64   `json:"confidence_min,omitempty"`
	Since          time.Time `json:"since,omitempty"`
	Until          time.Time `json:"until,omitempty"`
	IncludeHistory bool      `json:"include_history,omitempty"`
	Limit          int       `json:"limit,omitempty"`
	Offset         int       `json:"offset,omitempty"`
}

// EventPage mirrors Section 4.2 EventPage.
type EventPage struct {
	Events []SpecEvent `json:"events"`
	Total  int         `json:"total"`
	Limit  int         `json:"limit"`
	Offset int         `json:"offset"`
}

// FreshnessSourceStatus summarizes SLO compliance for a single registered source.
type FreshnessSourceStatus struct {
	SourceID           string `json:"source_id"`
	SourceTier         string `json:"source_tier"`
	LastObservedAt     string `json:"last_observed_at,omitempty"`
	CaptureLatencyMs   int64  `json:"capture_latency_ms"`
	NormalizeLatencyMs int64  `json:"normalize_latency_ms"`
	SLOPassed          bool   `json:"slo_passed"`
	Stale              bool   `json:"stale"`
	ViolationReason    string `json:"violation_reason,omitempty"`
}

// FreshnessReport reports per-source freshness SLO adherence and stale source alerts.
type FreshnessReport struct {
	GeneratedAt  string                  `json:"generated_at"`
	TotalSources int                     `json:"total_sources"`
	SLOPassed    int                     `json:"slo_passed"`
	SLOFailed    int                     `json:"slo_failed"`
	StaleSources []string                `json:"stale_sources"`
	Sources      []FreshnessSourceStatus `json:"sources"`
}

// SpecScheduler manages periodic polling for RadarEngine.
type SpecScheduler struct {
	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// ConfidenceScorer calculates weighted confidence scores and bands (Section 6.1).
type ConfidenceScorer struct {
	cfg ScorerConfig
}

// NewConfidenceScorer creates a ConfidenceScorer with the given config.
func NewConfidenceScorer(cfg ScorerConfig) *ConfidenceScorer {
	return &ConfidenceScorer{cfg: cfg}
}

// Score computes [0,1] confidence and its confidence band.
func (s *ConfidenceScorer) Score(sourceTrust float64, distinctGroups int, provenanceDepth int, extractionQuality float64, temporalValid bool, contested bool) (float64, string) {
	corrob := math.Min(1.0, 0.40+0.30*float64(maxInt(distinctGroups-1, 0)))
	if distinctGroups <= 0 {
		corrob = 0.20
	}
	provScore := math.Min(1.0, 0.50+0.25*float64(maxInt(provenanceDepth-1, 0)))
	tempScore := 0.5
	if temporalValid {
		tempScore = 1.0
	}
	raw := s.cfg.WeightSourceReliability*clamp01(sourceTrust) +
		s.cfg.WeightCorroboration*corrob +
		s.cfg.WeightProvenanceDepth*provScore +
		s.cfg.WeightExtractionQuality*clamp01(extractionQuality) +
		s.cfg.WeightTemporalValidity*tempScore
	if contested {
		raw *= (1.0 - s.cfg.ContradictionPenaltyFactor)
	}
	score := math.Round(clamp01(raw)*10000) / 10000
	return score, s.Band(score)
}

// Band maps a numeric confidence score to high/moderate/low/unverified.
func (s *ConfidenceScorer) Band(score float64) string {
	switch {
	case score >= s.cfg.BandHigh:
		return "high"
	case score >= s.cfg.BandModerate:
		return "moderate"
	case score >= s.cfg.BandLow:
		return "low"
	default:
		return "unverified"
	}
}

// Deduplicator evaluates claim/event similarity and metric conflict detection.
type Deduplicator struct {
	cfg DedupeConfig
}

// NewDeduplicator constructs a Deduplicator.
func NewDeduplicator(cfg DedupeConfig) *Deduplicator {
	return &Deduplicator{cfg: cfg}
}

var stopWords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true, "of": true,
	"in": true, "on": true, "at": true, "to": true, "for": true, "with": true,
	"by": true, "is": true, "are": true, "was": true, "were": true,
}

// Similarity computes a normalized token Jaccard/containment similarity in [0,1].
func (d *Deduplicator) Similarity(a, b string) float64 {
	ta := tokenizeSemantic(a)
	tb := tokenizeSemantic(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	inter := 0
	unionSet := make(map[string]bool, len(ta)+len(tb))
	for k := range ta {
		unionSet[k] = true
		if tb[k] {
			inter++
		}
	}
	for k := range tb {
		unionSet[k] = true
	}
	jaccard := float64(inter) / float64(len(unionSet))
	minLen := len(ta)
	if len(tb) < minLen {
		minLen = len(tb)
	}
	containment := float64(inter) / float64(minLen)
	return 0.4*jaccard + 0.6*containment
}

// DetectMetricConflict reports whether two metric maps disagree materially on a shared key.
func (d *Deduplicator) DetectMetricConflict(existing, incoming map[string]any) bool {
	if len(existing) == 0 || len(incoming) == 0 {
		return false
	}
	for k, vIn := range incoming {
		vEx, ok := existing[k]
		if !ok {
			continue
		}
		if fmt.Sprintf("%v", vEx) != fmt.Sprintf("%v", vIn) {
			return true
		}
	}
	return false
}

// ProvenanceRecorder creates immutable lineage records in radar_provenance.
type ProvenanceRecorder struct{}

// MetricsCollector tracks ingestion, SLO, and quarantine metrics.
type MetricsCollector struct {
	mu               sync.Mutex
	IngestRuns       int64
	ObservationsNew  int64
	ObservationsDupe int64
	QuarantinedCount int64
	FreshnessSLOPass int64
	FreshnessSLOFail int64
	StaleAlertCount  int64
}

// RadarEngine is the Section 4.1 orchestrator operating on the normalized spec SQLite schema.
type RadarEngine struct {
	DB               *sql.DB
	Taxonomy         *TaxonomyRegistry
	Connectors       map[connectors.ConnectorType]connectors.Connector
	SourceConnectors map[string]connectors.Connector
	Scheduler        *SpecScheduler
	Scorer           *ConfidenceScorer
	Dedupe           *Deduplicator
	Provenance       *ProvenanceRecorder
	Metrics          *MetricsCollector
	Config           RadarEngineConfig
	HTTPClient       *http.Client
	NowFunc          func() time.Time

	mu      sync.Mutex
	started bool
}

// NewRadarEngine initializes the normalized spec schema and constructs a RadarEngine.
func NewRadarEngine(db *sql.DB, cfg RadarEngineConfig) (*RadarEngine, error) {
	if db == nil {
		return nil, fmt.Errorf("worldradar: nil *sql.DB")
	}
	if cfg.TaxonomyVersion == "" {
		def := DefaultRadarEngineConfig()
		cfg = def
	}
	if cfg.Scorer.Version == "" {
		cfg.Scorer = DefaultRadarEngineConfig().Scorer
	}
	if cfg.Dedupe.ProvisionalThreshold == 0 {
		cfg.Dedupe = DefaultRadarEngineConfig().Dedupe
	}
	if len(cfg.Freshness.SLOTargets) == 0 {
		cfg.Freshness = DefaultRadarEngineConfig().Freshness
	}
	if cfg.CheckpointBatchSize <= 0 {
		cfg.CheckpointBatchSize = 50
	}
	if err := MigrateSpecSchema(db); err != nil {
		return nil, err
	}

	eng := &RadarEngine{
		DB:               db,
		Taxonomy:         NewTaxonomyRegistry("v1.0.0"),
		Connectors:       make(map[connectors.ConnectorType]connectors.Connector),
		SourceConnectors: make(map[string]connectors.Connector),
		Scheduler:        &SpecScheduler{},
		Scorer:           NewConfidenceScorer(cfg.Scorer),
		Dedupe:           NewDeduplicator(cfg.Dedupe),
		Provenance:       &ProvenanceRecorder{},
		Metrics:          &MetricsCollector{},
		Config:           cfg,
		HTTPClient:       &http.Client{Timeout: 30 * time.Second},
		NowFunc:          func() time.Time { return time.Now().UTC() },
	}
	eng.Connectors[connectors.ConnectorRSS] = &connectors.RSSConnector{HTTPClient: eng.HTTPClient}
	eng.Connectors[connectors.ConnectorGitHubReleases] = &connectors.GitHubReleasesConnector{HTTPClient: eng.HTTPClient}
	eng.Connectors[connectors.ConnectorWebFeedJSON] = &connectors.WebFeedJSONConnector{HTTPClient: eng.HTTPClient}
	eng.Connectors[connectors.ConnectorIDXBEI] = &connectors.IDXBEIConnector{HTTPClient: eng.HTTPClient}
	return eng, nil
}

func (e *RadarEngine) now() time.Time {
	if e.NowFunc != nil {
		return e.NowFunc().UTC()
	}
	return time.Now().UTC()
}

// Start begins the engine lifecycle and optional background scheduler.
func (e *RadarEngine) Start(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started {
		return nil
	}
	e.started = true
	e.Scheduler.mu.Lock()
	if !e.Scheduler.running {
		sCtx, cancel := context.WithCancel(ctx)
		e.Scheduler.cancel = cancel
		e.Scheduler.running = true
		e.Scheduler.wg.Add(1)
		go func() {
			defer e.Scheduler.wg.Done()
			<-sCtx.Done()
		}()
	}
	e.Scheduler.mu.Unlock()
	return nil
}

// Stop gracefully shuts down the engine and its scheduler.
func (e *RadarEngine) Stop() error {
	e.mu.Lock()
	e.started = false
	e.mu.Unlock()

	if e.Scheduler != nil {
		e.Scheduler.mu.Lock()
		cancel := e.Scheduler.cancel
		running := e.Scheduler.running
		e.Scheduler.running = false
		e.Scheduler.mu.Unlock()
		if running && cancel != nil {
			cancel()
			e.Scheduler.wg.Wait()
		}
	}
	return nil
}

// CreateInstance persists a new radar instance with all 4 temporal fields.
func (e *RadarEngine) CreateInstance(ctx context.Context, req InstanceCreateRequest) (*SpecInstance, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, fmt.Errorf("%w: instance name is required", ErrValidation)
	}
	instID := strings.TrimSpace(req.InstanceID)
	if instID == "" {
		instID = newUUIDv7()
	}
	status := firstNonEmpty(strings.ToLower(strings.TrimSpace(req.Status)), "active")
	switch status {
	case "active", "paused", "archived":
	default:
		return nil, fmt.Errorf("%w: invalid instance status %q", ErrValidation, status)
	}
	version := firstNonEmpty(req.Version, "1.0.0")
	taxVer := firstNonEmpty(req.TaxonomyVersion, e.Config.TaxonomyVersion, "v1.0.0")
	cfgJSON := firstNonEmpty(req.ConfigJSON, "{}")
	nowStr := e.now().Format(time.RFC3339Nano)

	_, err := e.DB.ExecContext(ctx, `
INSERT INTO radar_instances (instance_id, name, description, version, taxonomy_version, config_json, status, created_at, updated_at, valid_from, valid_to)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		instID, req.Name, req.Description, version, taxVer, cfgJSON, status, nowStr, nowStr, nowStr,
	)
	if err != nil {
		return nil, fmt.Errorf("insert radar_instances: %w", err)
	}
	e.recordAudit(ctx, "instance", instID, "create", "system", "", cfgJSON, "instance created", nowStr)
	return e.GetInstance(ctx, instID)
}

// GetInstance returns a radar instance by ID.
func (e *RadarEngine) GetInstance(ctx context.Context, instanceID string) (*SpecInstance, error) {
	row := e.DB.QueryRowContext(ctx, `
SELECT instance_id, name, COALESCE(description,''), version, taxonomy_version, config_json, status, created_at, updated_at, valid_from, valid_to
FROM radar_instances WHERE instance_id = ?`, strings.TrimSpace(instanceID))
	var inst SpecInstance
	var validTo sql.NullString
	if err := row.Scan(&inst.InstanceID, &inst.Name, &inst.Description, &inst.Version, &inst.TaxonomyVersion, &inst.ConfigJSON, &inst.Status, &inst.CreatedAt, &inst.UpdatedAt, &inst.ValidFrom, &validTo); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: instance %q", ErrNotFound, instanceID)
		}
		return nil, err
	}
	if validTo.Valid {
		v := validTo.String
		inst.ValidTo = &v
	}
	return &inst, nil
}

// ListInstances lists all radar instances.
func (e *RadarEngine) ListInstances(ctx context.Context) ([]SpecInstance, error) {
	rows, err := e.DB.QueryContext(ctx, `
SELECT instance_id, name, COALESCE(description,''), version, taxonomy_version, config_json, status, created_at, updated_at, valid_from, valid_to
FROM radar_instances ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]SpecInstance, 0)
	for rows.Next() {
		var inst SpecInstance
		var validTo sql.NullString
		if err := rows.Scan(&inst.InstanceID, &inst.Name, &inst.Description, &inst.Version, &inst.TaxonomyVersion, &inst.ConfigJSON, &inst.Status, &inst.CreatedAt, &inst.UpdatedAt, &inst.ValidFrom, &validTo); err != nil {
			return nil, err
		}
		if validTo.Valid {
			v := validTo.String
			inst.ValidTo = &v
		}
		out = append(out, inst)
	}
	return out, rows.Err()
}

var allowedLicenseClasses = map[string]bool{
	"redistribute_allowed": true,
	"internal_only":        true,
	"attribution_required": true,
	"public_domain":        true,
}

// RegisterSource validates and registers an ingestion source (AT-01, AT-23).
func (e *RadarEngine) RegisterSource(cfg SourceConfig) (string, error) {
	ctx := context.Background()
	if strings.TrimSpace(cfg.InstanceID) == "" {
		return "", fmt.Errorf("%w: instance_id is required", ErrValidation)
	}
	if _, err := e.GetInstance(ctx, cfg.InstanceID); err != nil {
		return "", err
	}
	if cfg.TrustBase < 0 || cfg.TrustBase > 1 {
		return "", fmt.Errorf("%w: trust_base must be in [0,1]", ErrValidation)
	}
	license := strings.ToLower(strings.TrimSpace(cfg.LicenseClass))
	if !allowedLicenseClasses[license] {
		return "", fmt.Errorf("%w: unsupported license_class %q", ErrValidation, cfg.LicenseClass)
	}
	tier := strings.ToUpper(strings.TrimSpace(cfg.SourceTier))
	switch tier {
	case "A", "B", "C", "D", "E", "F", "G", "X":
	default:
		return "", fmt.Errorf("%w: invalid source_tier %q", ErrValidation, cfg.SourceTier)
	}

	sourceID := strings.TrimSpace(cfg.SourceID)
	if sourceID == "" {
		sourceID = newUUIDv7()
	}
	status := firstNonEmpty(strings.ToLower(strings.TrimSpace(cfg.Status)), "active")
	indGroup := firstNonEmpty(strings.TrimSpace(cfg.IndependenceGroup), sourceID)
	cfgJSON := firstNonEmpty(cfg.ConfigJSON, "{}")
	nowStr := e.now().Format(time.RFC3339Nano)

	_, err := e.DB.ExecContext(ctx, `
INSERT INTO radar_sources
  (source_id, instance_id, source_tier, category, trust_base, collection_method, license_class, language_hint, geography_scope, status, independence_group, config_json, created_at, updated_at, valid_from, valid_to)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		sourceID, cfg.InstanceID, tier, cfg.Category, cfg.TrustBase, cfg.CollectionMethod,
		license, cfg.LanguageHint, cfg.GeographyScope, status, indGroup, cfgJSON,
		nowStr, nowStr, nowStr,
	)
	if err != nil {
		return "", fmt.Errorf("insert radar_sources: %w", err)
	}

	// Build and register source-specific connector if config_json specifies one.
	if conn := e.buildConnectorForSource(sourceID, license, cfgJSON); conn != nil {
		e.mu.Lock()
		e.SourceConnectors[sourceID] = conn
		e.mu.Unlock()
	}
	e.recordAudit(ctx, "source", sourceID, "create", "system", "", sanitizeConfigJSON(cfgJSON), "source registered", nowStr)
	return sourceID, nil
}

// RegisterSourceConnector binds an explicit Connector instance (e.g., mock HTTP client) to a sourceID.
func (e *RadarEngine) RegisterSourceConnector(sourceID string, conn connectors.Connector) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.SourceConnectors[strings.TrimSpace(sourceID)] = conn
}

func (e *RadarEngine) buildConnectorForSource(sourceID, licenseClass, cfgJSON string) connectors.Connector {
	var raw map[string]any
	if err := json.Unmarshal([]byte(cfgJSON), &raw); err != nil {
		return nil
	}
	ctype := strings.ToLower(strings.TrimSpace(asString(raw["connector_type"])))
	switch ctype {
	case string(connectors.ConnectorRSS), "":
		if feedURL := asString(raw["feed_url"]); feedURL != "" && ctype != string(connectors.ConnectorWebFeedJSON) {
			return &connectors.RSSConnector{
				SourceID:     sourceID,
				FeedURL:      feedURL,
				LicenseClass: licenseClass,
				HTTPClient:   e.HTTPClient,
			}
		}
	case string(connectors.ConnectorGitHubReleases):
		return &connectors.GitHubReleasesConnector{
			SourceID:           sourceID,
			Owner:              asString(raw["owner"]),
			Repo:               asString(raw["repo"]),
			Token:              asString(raw["token"]),
			APIBase:            asString(raw["api_base"]),
			LicenseClass:       licenseClass,
			IncludePrereleases: asBool(raw["include_prereleases"]),
			IncludeDrafts:      asBool(raw["include_drafts"]),
			HTTPClient:         e.HTTPClient,
		}
	case string(connectors.ConnectorWebFeedJSON):
		return &connectors.WebFeedJSONConnector{
			SourceID:     sourceID,
			FeedURL:      asString(raw["feed_url"]),
			Schema:       connectors.JSONFeedSchema(firstNonEmpty(asString(raw["schema"]), string(connectors.JSONFeedSchema11))),
			LicenseClass: licenseClass,
			HTTPClient:   e.HTTPClient,
		}
	case string(connectors.ConnectorIDXBEI):
		return &connectors.IDXBEIConnector{
			SourceID:     sourceID,
			Endpoint:     asString(raw["endpoint"]),
			APIKey:       asString(raw["api_key"]),
			DatasetID:    asString(raw["dataset_id"]),
			LicenseClass: licenseClass,
			HTTPClient:   e.HTTPClient,
		}
	}
	return nil
}

// ListSources returns sources for an instance optionally filtered by status.
func (e *RadarEngine) ListSources(ctx context.Context, instanceID, status string) ([]SpecSource, error) {
	q := `SELECT source_id, instance_id, source_tier, category, trust_base, collection_method, license_class, COALESCE(language_hint,''), geography_scope, status, COALESCE(independence_group,''), config_json, created_at, updated_at, valid_from, valid_to FROM radar_sources WHERE instance_id = ?`
	args := []any{strings.TrimSpace(instanceID)}
	if strings.TrimSpace(status) != "" {
		q += ` AND status = ?`
		args = append(args, strings.TrimSpace(status))
	}
	q += ` ORDER BY created_at ASC`
	rows, err := e.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]SpecSource, 0)
	for rows.Next() {
		var s SpecSource
		var validTo sql.NullString
		if err := rows.Scan(&s.SourceID, &s.InstanceID, &s.SourceTier, &s.Category, &s.TrustBase, &s.CollectionMethod, &s.LicenseClass, &s.LanguageHint, &s.GeographyScope, &s.Status, &s.IndependenceGroup, &s.ConfigJSON, &s.CreatedAt, &s.UpdatedAt, &s.ValidFrom, &validTo); err != nil {
			return nil, err
		}
		if validTo.Valid {
			v := validTo.String
			s.ValidTo = &v
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (e *RadarEngine) getSource(ctx context.Context, sourceID string) (SpecSource, error) {
	row := e.DB.QueryRowContext(ctx, `
SELECT source_id, instance_id, source_tier, category, trust_base, collection_method, license_class, COALESCE(language_hint,''), geography_scope, status, COALESCE(independence_group,''), config_json, created_at, updated_at, valid_from, valid_to
FROM radar_sources WHERE source_id = ?`, strings.TrimSpace(sourceID))
	var s SpecSource
	var validTo sql.NullString
	if err := row.Scan(&s.SourceID, &s.InstanceID, &s.SourceTier, &s.Category, &s.TrustBase, &s.CollectionMethod, &s.LicenseClass, &s.LanguageHint, &s.GeographyScope, &s.Status, &s.IndependenceGroup, &s.ConfigJSON, &s.CreatedAt, &s.UpdatedAt, &s.ValidFrom, &validTo); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SpecSource{}, fmt.Errorf("%w: source %q", ErrNotFound, sourceID)
		}
		return SpecSource{}, err
	}
	if validTo.Valid {
		v := validTo.String
		s.ValidTo = &v
	}
	return s, nil
}

// GetCheckpoint loads the persisted checkpoint for a source.
func (e *RadarEngine) GetCheckpoint(ctx context.Context, sourceID string) (connectors.Checkpoint, error) {
	row := e.DB.QueryRowContext(ctx, `
SELECT source_id, connector_type, cursor, last_seen_at, item_count, hash
FROM radar_checkpoints WHERE source_id = ?`, strings.TrimSpace(sourceID))
	var cp connectors.Checkpoint
	var ctype, lastSeen string
	err := row.Scan(&cp.SourceID, &ctype, &cp.Cursor, &lastSeen, &cp.ItemCount, &cp.Hash)
	if errors.Is(err, sql.ErrNoRows) {
		return connectors.Checkpoint{SourceID: sourceID}, nil
	}
	if err != nil {
		return connectors.Checkpoint{}, err
	}
	cp.ConnectorType = connectors.ConnectorType(ctype)
	if strings.TrimSpace(lastSeen) != "" {
		if t, parseErr := time.Parse(time.RFC3339Nano, lastSeen); parseErr == nil {
			cp.LastSeenAt = t.UTC()
		}
	}
	return cp, nil
}

// SetCheckpoint persists a checkpoint idempotently into radar_checkpoints.
func (e *RadarEngine) SetCheckpoint(ctx context.Context, sourceID string, cp connectors.Checkpoint) error {
	cp.SourceID = sourceID
	if cp.Hash == "" {
		cp.Hash = cp.ComputeHash()
	}
	nowStr := e.now().Format(time.RFC3339Nano)
	lastSeenStr := ""
	if !cp.LastSeenAt.IsZero() {
		lastSeenStr = cp.LastSeenAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := e.DB.ExecContext(ctx, `
INSERT INTO radar_checkpoints (source_id, connector_type, cursor, last_seen_at, item_count, hash, created_at, updated_at, valid_from, valid_to)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
ON CONFLICT(source_id) DO UPDATE SET
  connector_type = excluded.connector_type,
  cursor = excluded.cursor,
  last_seen_at = excluded.last_seen_at,
  item_count = excluded.item_count,
  hash = excluded.hash,
  updated_at = excluded.updated_at`,
		sourceID, string(cp.ConnectorType), cp.Cursor, lastSeenStr, cp.ItemCount, cp.Hash, nowStr, nowStr, nowStr,
	)
	return err
}

// TriggerIngestion executes Discover -> Acquire -> Validate -> Normalize -> Persist -> Dedupe/Score
// for the given sourceID using Context background.
func (e *RadarEngine) TriggerIngestion(sourceID string) error {
	return e.TriggerIngestionContext(context.Background(), sourceID)
}

// TriggerIngestionContext executes context-aware ingestion with incremental batch checkpointing.
func (e *RadarEngine) TriggerIngestionContext(ctx context.Context, sourceID string) error {
	src, err := e.getSource(ctx, sourceID)
	if err != nil {
		return err
	}
	if src.Status == "suspended" || src.Status == "retired" {
		return fmt.Errorf("%w: source %q is %s", ErrValidation, sourceID, src.Status)
	}

	e.mu.Lock()
	conn := e.SourceConnectors[sourceID]
	e.mu.Unlock()
	if conn == nil {
		conn = e.buildConnectorForSource(sourceID, src.LicenseClass, src.ConfigJSON)
		if conn == nil {
			return fmt.Errorf("%w: no connector configured for source %q", ErrValidation, sourceID)
		}
		e.mu.Lock()
		e.SourceConnectors[sourceID] = conn
		e.mu.Unlock()
	}

	prevCP, err := e.GetCheckpoint(ctx, sourceID)
	if err != nil {
		return err
	}
	if prevCP.ConnectorType == "" {
		prevCP.ConnectorType = conn.Type()
	}

	items, finalCP, err := conn.Discover(ctx, prevCP)
	if err != nil {
		return err
	}
	e.Metrics.mu.Lock()
	e.Metrics.IngestRuns++
	e.Metrics.mu.Unlock()

	if len(items) == 0 {
		return nil
	}

	batchSize := e.Config.CheckpointBatchSize
	if batchSize <= 0 {
		batchSize = 50
	}

	runningCP := prevCP
	runningCP.ConnectorType = conn.Type()
	runningCP.SourceID = sourceID

	for start := 0; start < len(items); start += batchSize {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		end := start + batchSize
		if end > len(items) {
			end = len(items)
		}
		batch := items[start:end]

		var lastBatchItem connectors.DiscoveredItem
		for _, item := range batch {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			obs, acqErr := conn.Acquire(ctx, item)
			if acqErr != nil {
				return acqErr
			}
			valRes := conn.Validate(obs)
			normObs, normErr := conn.Normalize(obs)
			if normErr != nil {
				return normErr
			}
			if valRes.SchemaDrift && e.Config.QuarantineOnDrift {
				valRes.ValidationState = "quarantined"
				valRes.ReasonCodes = append(valRes.ReasonCodes, "schema_drift_quarantine")
			}
			normObs.ValidationState = valRes.ValidationState
			normObs.ReasonCodes = valRes.ReasonCodes
			if e.NowFunc != nil {
				now := e.NowFunc().UTC()
				obs.CapturedAt = now
				normObs.CapturedAt = now
			}

			if persistErr := e.persistNormalizedObservation(ctx, src, obs, normObs); persistErr != nil {
				return persistErr
			}
			lastBatchItem = item
		}

		// Persist durable batch checkpoint after batch completes.
		if end == len(items) && finalCP.Cursor != "" {
			runningCP = finalCP
		} else {
			runningCP.Cursor = buildBatchCursor(conn.Type(), lastBatchItem)
			runningCP.LastSeenAt = lastBatchItem.PublishedAt.UTC()
			runningCP.ItemCount += int64(len(batch))
			runningCP.Hash = runningCP.ComputeHash()
		}
		if err := (connectors.CheckpointContract{}).ValidateAdvance(prevCP, runningCP); err == nil {
			if cpErr := e.SetCheckpoint(ctx, sourceID, runningCP); cpErr != nil {
				return cpErr
			}
			prevCP = runningCP
		}
	}

	e.evaluateFreshnessForSource(ctx, src)
	return nil
}

func buildBatchCursor(ctype connectors.ConnectorType, item connectors.DiscoveredItem) string {
	switch ctype {
	case connectors.ConnectorRSS:
		guid := item.Metadata["guid"]
		if guid == "" {
			guid = item.Locator
		}
		return guid + "|" + item.PublishedAt.UTC().Format(time.RFC3339Nano)
	case connectors.ConnectorGitHubReleases:
		return item.Metadata["release_id"] + "|" + item.PublishedAt.UTC().Format(time.RFC3339Nano)
	case connectors.ConnectorWebFeedJSON:
		return item.Metadata["item_id"] + "|" + item.PublishedAt.UTC().Format(time.RFC3339Nano) + "|" + item.Metadata["feed_hash"]
	case connectors.ConnectorIDXBEI:
		return item.Metadata["record_id"] + "|" + item.PublishedAt.UTC().Format(time.RFC3339Nano) + "|"
	default:
		return item.Locator + "|" + item.PublishedAt.UTC().Format(time.RFC3339Nano)
	}
}

func (e *RadarEngine) persistNormalizedObservation(ctx context.Context, src SpecSource, rawObs connectors.Observation, normObs connectors.NormalizedObservation) error {
	nowStr := e.now().Format(time.RFC3339Nano)
	obsID := normObs.ObservationID

	var existingObs string
	err := e.DB.QueryRowContext(ctx, `SELECT observation_id FROM radar_observations WHERE observation_id = ?`, obsID).Scan(&existingObs)
	if err == nil && existingObs != "" {
		e.Metrics.mu.Lock()
		e.Metrics.ObservationsDupe++
		e.Metrics.mu.Unlock()
		return nil
	}

	// Also enforce exact content_hash dedupe per source (AT-02).
	var dupeObsID string
	err = e.DB.QueryRowContext(ctx, `SELECT observation_id FROM radar_observations WHERE source_id = ? AND content_hash = ? LIMIT 1`, src.SourceID, normObs.ContentHash).Scan(&dupeObsID)
	if err == nil && dupeObsID != "" {
		e.Metrics.mu.Lock()
		e.Metrics.ObservationsDupe++
		e.Metrics.mu.Unlock()
		return nil
	}

	metaMap := map[string]any{
		"title":        normObs.Title,
		"summary":      normObs.Summary,
		"entities":     normObs.Entities,
		"metrics":      normObs.Metrics,
		"place":        normObs.Place,
		"domains":      normObs.Domains,
		"privacy":      normObs.PrivacyClass,
		"pii_redacted": normObs.PIIRedacted,
	}
	for k, v := range normObs.Metadata {
		metaMap[k] = v
	}
	metaBytes, _ := json.Marshal(metaMap)

	capturedStr := normObs.CapturedAt.UTC().Format(time.RFC3339Nano)
	publishedStr := normObs.PublishedAt.UTC().Format(time.RFC3339Nano)
	observedStr := normObs.ObservedAt.UTC().Format(time.RFC3339Nano)
	if normObs.ValidationState == "" {
		normObs.ValidationState = "accepted"
	}

	_, err = e.DB.ExecContext(ctx, `
INSERT INTO radar_observations
  (observation_id, source_id, source_version, captured_at, published_at, observed_at, canonical_url, content_hash, raw_hash, language, payload_type, license_class, validation_state, raw_payload, metadata_json, created_at, updated_at, valid_from, valid_to)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		obsID, src.SourceID, normObs.SourceVersion, capturedStr, publishedStr, observedStr,
		normObs.CanonicalURL, normObs.ContentHash, normObs.RawHash, normObs.Language,
		normObs.PayloadType, normObs.LicenseClass, normObs.ValidationState,
		rawObs.RawPayload, string(metaBytes), nowStr, nowStr, observedStr,
	)
	if err != nil {
		return fmt.Errorf("insert radar_observations: %w", err)
	}

	provID := newUUIDv7()
	reasonsBytes, _ := json.Marshal(normObs.ReasonCodes)
	_, err = e.DB.ExecContext(ctx, `
INSERT INTO radar_provenance
  (provenance_id, observation_id, source_id, source_version, capture_method, captured_at, canonical_url, raw_hash, content_hash, parser_version, normalizer_version, dedupe_version, scorer_version, transform_parents_json, extracted_at, license_class, privacy_class, reason_codes_json, created_at, updated_at, valid_from, valid_to)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'v1.0.0', 'v1.0.0', 'v1.0.0', ?, '[]', ?, ?, ?, ?, ?, ?, ?, NULL)`,
		provID, obsID, src.SourceID, normObs.SourceVersion, src.CollectionMethod, capturedStr,
		normObs.CanonicalURL, normObs.RawHash, normObs.ContentHash, e.Config.Scorer.Version,
		nowStr, normObs.LicenseClass, normObs.PrivacyClass, string(reasonsBytes),
		nowStr, nowStr, observedStr,
	)
	if err != nil {
		return fmt.Errorf("insert radar_provenance: %w", err)
	}

	if normObs.ValidationState == "quarantined" || normObs.ValidationState == "rejected" {
		e.Metrics.mu.Lock()
		e.Metrics.QuarantinedCount++
		e.Metrics.mu.Unlock()
		e.recordAudit(ctx, "observation", obsID, "quarantine", "connector", "", string(reasonsBytes), "validation "+normObs.ValidationState, nowStr)
		return nil
	}

	e.Metrics.mu.Lock()
	e.Metrics.ObservationsNew++
	e.Metrics.mu.Unlock()

	return e.processDerivedClaimAndEvent(ctx, src, obsID, normObs, nowStr)
}

func (e *RadarEngine) processDerivedClaimAndEvent(ctx context.Context, src SpecSource, obsID string, normObs connectors.NormalizedObservation, nowStr string) error {
	isRetraction := false
	if normObs.Metadata != nil {
		if r, ok := normObs.Metadata["retracted"].(bool); ok && r {
			isRetraction = true
		}
	}
	if strings.Contains(strings.ToLower(normObs.Title), "retraction:") || strings.HasPrefix(strings.ToLower(normObs.Title), "retract ") {
		isRetraction = true
	}

	claimType := "assertion"
	if isRetraction {
		claimType = "retraction"
	}
	claimText := normObs.Title
	if normObs.Summary != "" && normObs.Summary != normObs.Title {
		claimText = normObs.Title + " — " + normObs.Summary
	}
	// Privacy gate: ensure derived claim_text contains zero raw PII.
	claimText, _ = connectors.RedactPII(claimText)

	entitiesJSON, _ := json.Marshal(normObs.Entities)
	metricsJSON := ""
	if len(normObs.Metrics) > 0 {
		b, _ := json.Marshal(normObs.Metrics)
		metricsJSON = string(b)
	}
	placeJSON := "{}"
	if len(normObs.Place) > 0 {
		b, _ := json.Marshal(normObs.Place)
		placeJSON = string(b)
	}

	claimConf, claimBand := e.Scorer.Score(src.TrustBase, 1, 1, 0.90, true, false)
	claimID := newUUIDv7()
	pubStr := normObs.PublishedAt.UTC().Format(time.RFC3339Nano)

	_, err := e.DB.ExecContext(ctx, `
INSERT INTO radar_claims
  (claim_id, observation_id, claim_type, claim_text, entities_json, metrics_json, time_range_start, time_range_end, place_json, confidence, confidence_band, extractor_version, created_at, updated_at, valid_from, valid_to)
VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, 'v1.0.0', ?, ?, ?, NULL)`,
		claimID, obsID, claimType, claimText, string(entitiesJSON), metricsJSON,
		pubStr, placeJSON, claimConf, claimBand, nowStr, nowStr, pubStr,
	)
	if err != nil {
		return fmt.Errorf("insert radar_claims: %w", err)
	}

	topics := buildTopicTags(normObs, e.Taxonomy)
	topicsJSON, _ := json.Marshal(topics)

	matchedEvent, err := e.findMatchingCurrentEvent(ctx, claimText, normObs)
	if err != nil {
		return err
	}

	if matchedEvent == nil {
		eventID := newUUIDv7()
		claimsArr, _ := json.Marshal([]string{claimID})
		status := "reported"
		if isRetraction {
			status = "retracted"
		} else if src.SourceTier == "A" {
			status = "confirmed"
		}
		_, err := e.DB.ExecContext(ctx, `
INSERT INTO radar_events
  (event_id, event_type, event_status, time_range_start, time_range_end, place_json, actors_json, claims_json, metrics_json, topics_json, confidence, confidence_band, version, scorer_version, created_at, updated_at, valid_from, valid_to)
VALUES (?, ?, ?, ?, NULL, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, NULL)`,
			eventID, claimType, status, pubStr, placeJSON, string(entitiesJSON),
			string(claimsArr), metricsJSON, string(topicsJSON), claimConf, claimBand,
			e.Config.Scorer.Version, nowStr, nowStr, nowStr,
		)
		if err != nil {
			return fmt.Errorf("insert radar_events: %w", err)
		}
		e.recordAudit(ctx, "event", eventID, "create", "engine", "", string(claimsArr), "created from claim", nowStr)
		return nil
	}

	// Merge into existing event cluster (SC-02, SC-03, SC-04).
	var existingClaims []string
	_ = json.Unmarshal([]byte(matchedEvent.ClaimsJSON), &existingClaims)
	existingClaims = append(existingClaims, claimID)
	mergedClaimsJSON, _ := json.Marshal(existingClaims)

	var existingActors []string
	_ = json.Unmarshal([]byte(matchedEvent.ActorsJSON), &existingActors)
	mergedActors := mergeStringSets(existingActors, normObs.Entities)
	mergedActorsJSON, _ := json.Marshal(mergedActors)

	var existingTopics []string
	_ = json.Unmarshal([]byte(matchedEvent.TopicsJSON), &existingTopics)
	mergedTopics := mergeStringSets(existingTopics, topics)
	mergedTopicsJSON, _ := json.Marshal(mergedTopics)

	var existingMetrics map[string]any
	if matchedEvent.MetricsJSON != "" {
		_ = json.Unmarshal([]byte(matchedEvent.MetricsJSON), &existingMetrics)
	}
	contested := matchedEvent.EventStatus == "contested" || e.Dedupe.DetectMetricConflict(existingMetrics, normObs.Metrics)
	mergedMetrics := mergeMetrics(existingMetrics, normObs.Metrics)
	mergedMetricsBytes, _ := json.Marshal(mergedMetrics)

	distinctGroups, avgTrust, provDepth := e.countDistinctGroupsForClaims(ctx, existingClaims)
	newConf, newBand := e.Scorer.Score(avgTrust, distinctGroups, provDepth, 0.90, true, contested)

	if isRetraction {
		// Supersede prior event version with valid_to = nowStr and create new version with event_status = "retracted".
		if _, err := e.DB.ExecContext(ctx, `UPDATE radar_events SET valid_to = ?, updated_at = ? WHERE event_id = ?`, nowStr, nowStr, matchedEvent.EventID); err != nil {
			return fmt.Errorf("close superseded event: %w", err)
		}
		newEventID := newUUIDv7()
		newVersion := matchedEvent.Version + 1
		_, err := e.DB.ExecContext(ctx, `
INSERT INTO radar_events
  (event_id, event_type, event_status, time_range_start, time_range_end, place_json, actors_json, claims_json, metrics_json, topics_json, confidence, confidence_band, version, scorer_version, created_at, updated_at, valid_from, valid_to)
VALUES (?, ?, 'retracted', ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
			newEventID, matchedEvent.EventType, matchedEvent.TimeRangeStart, matchedEvent.PlaceJSON,
			string(mergedActorsJSON), string(mergedClaimsJSON), string(mergedMetricsBytes),
			string(mergedTopicsJSON), newConf, newBand, newVersion, e.Config.Scorer.Version,
			nowStr, nowStr, nowStr,
		)
		if err != nil {
			return fmt.Errorf("insert retracted event version: %w", err)
		}
		e.recordAudit(ctx, "event", newEventID, "retract", src.SourceID, matchedEvent.EventID, string(mergedClaimsJSON), "authoritative retraction", nowStr)
		return nil
	}

	nextStatus := matchedEvent.EventStatus
	if contested {
		nextStatus = "contested"
	} else if distinctGroups >= 2 {
		nextStatus = "confirmed"
	}

	_, err = e.DB.ExecContext(ctx, `
UPDATE radar_events
SET event_status = ?, actors_json = ?, claims_json = ?, metrics_json = ?, topics_json = ?, confidence = ?, confidence_band = ?, updated_at = ?
WHERE event_id = ?`,
		nextStatus, string(mergedActorsJSON), string(mergedClaimsJSON), string(mergedMetricsBytes),
		string(mergedTopicsJSON), newConf, newBand, nowStr, matchedEvent.EventID,
	)
	if err != nil {
		return fmt.Errorf("update radar_events: %w", err)
	}
	return nil
}

func (e *RadarEngine) findMatchingCurrentEvent(ctx context.Context, claimText string, normObs connectors.NormalizedObservation) (*SpecEvent, error) {
	// If retracting a specific target ID, match directly.
	retractsID := ""
	if normObs.Metadata != nil {
		if s, ok := normObs.Metadata["retracts_id"].(string); ok {
			retractsID = strings.TrimSpace(s)
		}
	}

	rows, err := e.DB.QueryContext(ctx, `
SELECT event_id, event_type, event_status, time_range_start, time_range_end, place_json, actors_json, claims_json, COALESCE(metrics_json,''), topics_json, confidence, confidence_band, version, scorer_version, created_at, updated_at, valid_from, valid_to
FROM radar_events WHERE valid_to IS NULL ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []SpecEvent
	for rows.Next() {
		ev, scanErr := scanSpecEvent(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		candidates = append(candidates, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var best *SpecEvent
	bestSim := 0.0
	cleanTitle := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(normObs.Title, "RETRACTION:"), "Retraction:"))
	cleanClaim := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(claimText, "RETRACTION:"), "Retraction:"))

	for i := range candidates {
		ev := &candidates[i]
		var claimIDs []string
		_ = json.Unmarshal([]byte(ev.ClaimsJSON), &claimIDs)
		for _, cid := range claimIDs {
			var existingText, existingObsID, existingMeta string
			if err := e.DB.QueryRowContext(ctx, `
SELECT c.claim_text, c.observation_id, COALESCE(o.metadata_json, '')
FROM radar_claims c
JOIN radar_observations o ON o.observation_id = c.observation_id
WHERE c.claim_id = ?`, cid).Scan(&existingText, &existingObsID, &existingMeta); err != nil {
				continue
			}
			if retractsID != "" && (strings.Contains(existingObsID, retractsID) || strings.Contains(existingText, retractsID) || strings.Contains(existingMeta, `"`+retractsID+`"`)) {
				return ev, nil
			}
			sim := e.Dedupe.Similarity(claimText, existingText)
			if sim2 := e.Dedupe.Similarity(cleanClaim, existingText); sim2 > sim {
				sim = sim2
			}
			if cleanTitle != "" {
				if sim3 := e.Dedupe.Similarity(cleanTitle, existingText); sim3 > sim {
					sim = sim3
				}
			}
			if sim >= e.Config.Dedupe.ProvisionalThreshold && sim > bestSim {
				bestSim = sim
				best = ev
			}
		}
	}
	return best, nil
}

func (e *RadarEngine) countDistinctGroupsForClaims(ctx context.Context, claimIDs []string) (int, float64, int) {
	groups := make(map[string]float64)
	provCount := 0
	for _, cid := range claimIDs {
		var indGroup string
		var trust float64
		err := e.DB.QueryRowContext(ctx, `
SELECT COALESCE(NULLIF(s.independence_group,''), s.source_id), s.trust_base
FROM radar_claims c
JOIN radar_observations o ON o.observation_id = c.observation_id
JOIN radar_sources s ON s.source_id = o.source_id
WHERE c.claim_id = ?`, cid).Scan(&indGroup, &trust)
		if err != nil {
			continue
		}
		provCount++
		if prev, ok := groups[indGroup]; !ok || trust > prev {
			groups[indGroup] = trust
		}
	}
	if len(groups) == 0 {
		return 1, 0.5, maxInt(provCount, 1)
	}
	var sum float64
	for _, t := range groups {
		sum += t
	}
	return len(groups), sum / float64(len(groups)), provCount
}

// GetEvent returns a SpecEventDetail with its claims and provenance chain (Section 4.1).
func (e *RadarEngine) GetEvent(eventID string) (*SpecEventDetail, error) {
	ctx := context.Background()
	row := e.DB.QueryRowContext(ctx, `
SELECT event_id, event_type, event_status, time_range_start, time_range_end, place_json, actors_json, claims_json, COALESCE(metrics_json,''), topics_json, confidence, confidence_band, version, scorer_version, created_at, updated_at, valid_from, valid_to
FROM radar_events WHERE event_id = ?`, strings.TrimSpace(eventID))
	ev, err := scanSpecEvent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: event %q", ErrNotFound, eventID)
	}
	if err != nil {
		return nil, err
	}

	var claimIDs []string
	_ = json.Unmarshal([]byte(ev.ClaimsJSON), &claimIDs)
	claims, err := e.loadClaims(ctx, claimIDs)
	if err != nil {
		return nil, err
	}
	prov, err := e.loadProvenanceForClaims(ctx, claimIDs)
	if err != nil {
		return nil, err
	}

	return &SpecEventDetail{
		SpecEvent:  ev,
		Provenance: prov,
		Claims:     claims,
	}, nil
}

// GetProvenance returns the full provenance chain for an event.
func (e *RadarEngine) GetProvenance(eventID string) ([]ProvenanceRecord, error) {
	detail, err := e.GetEvent(eventID)
	if err != nil {
		return nil, err
	}
	return detail.Provenance, nil
}

// QueryEvents queries current (or historical) events with domain, lens, confidence_min, and time filters.
func (e *RadarEngine) QueryEvents(q EventQuery) (*EventPage, error) {
	ctx := context.Background()
	limit := normalizeLimit(q.Limit, 50, 1000)
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}

	where := []string{"1=1"}
	var args []any
	if !q.IncludeHistory {
		where = append(where, "valid_to IS NULL")
	}
	if strings.TrimSpace(q.Domain) != "" {
		where = append(where, "topics_json LIKE ?")
		args = append(args, `%"`+strings.ToUpper(strings.TrimSpace(q.Domain))+`"%`)
	}
	if strings.TrimSpace(q.Lens) != "" {
		where = append(where, "topics_json LIKE ?")
		args = append(args, `%"`+strings.ToUpper(strings.TrimSpace(q.Lens))+`"%`)
	}
	if q.ConfidenceMin > 0 {
		where = append(where, "confidence >= ?")
		args = append(args, q.ConfidenceMin)
	}
	if !q.Since.IsZero() {
		where = append(where, "time_range_start >= ?")
		args = append(args, q.Since.UTC().Format(time.RFC3339Nano))
	}
	if !q.Until.IsZero() {
		where = append(where, "time_range_start <= ?")
		args = append(args, q.Until.UTC().Format(time.RFC3339Nano))
	}

	whereSQL := strings.Join(where, " AND ")
	var total int
	if err := e.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM radar_events WHERE `+whereSQL, args...).Scan(&total); err != nil {
		return nil, err
	}

	querySQL := `SELECT event_id, event_type, event_status, time_range_start, time_range_end, place_json, actors_json, claims_json, COALESCE(metrics_json,''), topics_json, confidence, confidence_band, version, scorer_version, created_at, updated_at, valid_from, valid_to
FROM radar_events WHERE ` + whereSQL + ` ORDER BY updated_at DESC, created_at DESC LIMIT ? OFFSET ?`
	queryArgs := append(append([]any(nil), args...), limit, offset)

	rows, err := e.DB.QueryContext(ctx, querySQL, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]SpecEvent, 0)
	for rows.Next() {
		ev, scanErr := scanSpecEvent(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return &EventPage{
		Events: events,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	}, nil
}

// GetFreshnessReport computes tier SLO compliance and stale source alerts (AT-12, AT-13).
func (e *RadarEngine) GetFreshnessReport() FreshnessReport {
	ctx := context.Background()
	now := e.now()
	report := FreshnessReport{
		GeneratedAt:  now.Format(time.RFC3339Nano),
		StaleSources: make([]string, 0),
		Sources:      make([]FreshnessSourceStatus, 0),
	}

	rows, err := e.DB.QueryContext(ctx, `
SELECT source_id, source_tier, created_at FROM radar_sources WHERE status = 'active' ORDER BY created_at ASC`)
	if err != nil {
		return report
	}
	defer rows.Close()

	type srcRow struct {
		id, tier, createdAt string
	}
	var srcs []srcRow
	for rows.Next() {
		var sr srcRow
		if err := rows.Scan(&sr.id, &sr.tier, &sr.createdAt); err == nil {
			srcs = append(srcs, sr)
		}
	}

	staleHours := e.Config.Freshness.StaleAlertHours
	if staleHours <= 0 {
		staleHours = 24
	}

	for _, sr := range srcs {
		report.TotalSources++
		st := FreshnessSourceStatus{
			SourceID:   sr.id,
			SourceTier: sr.tier,
			SLOPassed:  true,
		}
		var pubAtStr, capAtStr, obsCreatedStr string
		obsErr := e.DB.QueryRowContext(ctx, `
SELECT COALESCE(published_at,''), captured_at, created_at
FROM radar_observations WHERE source_id = ? ORDER BY captured_at DESC LIMIT 1`, sr.id).Scan(&pubAtStr, &capAtStr, &obsCreatedStr)

		target, hasTarget := e.Config.Freshness.SLOTargets[sr.tier]
		if errors.Is(obsErr, sql.ErrNoRows) {
			createdT, pErr := time.Parse(time.RFC3339Nano, sr.createdAt)
			if pErr == nil && now.Sub(createdT) > time.Duration(staleHours)*time.Hour {
				st.Stale = true
				st.SLOPassed = false
				st.ViolationReason = fmt.Sprintf("stale: no observations in %dh", staleHours)
			}
		} else if obsErr == nil {
			st.LastObservedAt = capAtStr
			pubT, _ := time.Parse(time.RFC3339Nano, pubAtStr)
			capT, _ := time.Parse(time.RFC3339Nano, capAtStr)
			normT, _ := time.Parse(time.RFC3339Nano, obsCreatedStr)

			if !pubT.IsZero() && !capT.IsZero() {
				capLat := capT.Sub(pubT)
				if capLat < 0 {
					capLat = 0
				}
				st.CaptureLatencyMs = capLat.Milliseconds()
				if hasTarget && capLat > target.Capture {
					st.SLOPassed = false
					st.ViolationReason = fmt.Sprintf("capture latency %s > SLO %s", capLat, target.Capture)
				}
			}
			if !pubT.IsZero() && !normT.IsZero() {
				normLat := normT.Sub(pubT)
				if normLat < 0 {
					normLat = 0
				}
				st.NormalizeLatencyMs = normLat.Milliseconds()
				if hasTarget && normLat > target.Normalized {
					st.SLOPassed = false
					st.ViolationReason = fmt.Sprintf("normalized latency %s > SLO %s", normLat, target.Normalized)
				}
			}
			if !capT.IsZero() && now.Sub(capT) > time.Duration(staleHours)*time.Hour {
				st.Stale = true
				st.SLOPassed = false
				st.ViolationReason = fmt.Sprintf("stale: last observation %s ago", now.Sub(capT))
			}
		}

		if st.Stale {
			report.StaleSources = append(report.StaleSources, sr.id)
		}
		if st.SLOPassed {
			report.SLOPassed++
		} else {
			report.SLOFailed++
		}
		report.Sources = append(report.Sources, st)
	}

	e.Metrics.mu.Lock()
	e.Metrics.FreshnessSLOPass += int64(report.SLOPassed)
	e.Metrics.FreshnessSLOFail += int64(report.SLOFailed)
	e.Metrics.StaleAlertCount += int64(len(report.StaleSources))
	e.Metrics.mu.Unlock()

	return report
}

func (e *RadarEngine) evaluateFreshnessForSource(ctx context.Context, src SpecSource) {
	target, ok := e.Config.Freshness.SLOTargets[src.SourceTier]
	if !ok {
		return
	}
	var pubAtStr, capAtStr, createdStr string
	err := e.DB.QueryRowContext(ctx, `
SELECT COALESCE(published_at,''), captured_at, created_at
FROM radar_observations WHERE source_id = ? ORDER BY captured_at DESC LIMIT 1`, src.SourceID).Scan(&pubAtStr, &capAtStr, &createdStr)
	if err != nil {
		return
	}
	pubT, _ := time.Parse(time.RFC3339Nano, pubAtStr)
	createdT, _ := time.Parse(time.RFC3339Nano, createdStr)
	if !pubT.IsZero() && !createdT.IsZero() {
		if createdT.Sub(pubT) > target.Normalized {
			e.Metrics.mu.Lock()
			e.Metrics.FreshnessSLOFail++
			e.Metrics.mu.Unlock()
		} else {
			e.Metrics.mu.Lock()
			e.Metrics.FreshnessSLOPass++
			e.Metrics.mu.Unlock()
		}
	}
}

// RebuildDerivedViews drops/clears derived view rows (radar_events, radar_claims) and
// replays them deterministically from normalized radar_observations + radar_provenance (SC-09, AT-11, AT-22).
func (e *RadarEngine) RebuildDerivedViews(ctx context.Context) error {
	if _, err := e.DB.ExecContext(ctx, `DELETE FROM radar_events; DELETE FROM radar_claims;`); err != nil {
		return fmt.Errorf("clear derived views: %w", err)
	}

	rows, err := e.DB.QueryContext(ctx, `
SELECT o.observation_id, o.source_id, o.source_version, o.captured_at, COALESCE(o.published_at,''), COALESCE(o.observed_at,''),
       COALESCE(o.canonical_url,''), o.content_hash, o.raw_hash, COALESCE(o.language,'en'), o.payload_type, o.license_class, o.validation_state,
       COALESCE(o.metadata_json,'{}'), o.created_at
FROM radar_observations o
WHERE o.validation_state = 'accepted'
ORDER BY o.created_at ASC, o.observation_id ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type obsRow struct {
		srcID, obsID, srcVer, capStr, pubStr, obsStr, url, cHash, rHash, lang, pType, lic, valState, metaJSON, createdAt string
	}
	var loaded []obsRow
	for rows.Next() {
		var r obsRow
		if err := rows.Scan(&r.obsID, &r.srcID, &r.srcVer, &r.capStr, &r.pubStr, &r.obsStr, &r.url, &r.cHash, &r.rHash, &r.lang, &r.pType, &r.lic, &r.valState, &r.metaJSON, &r.createdAt); err != nil {
			return err
		}
		loaded = append(loaded, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, r := range loaded {
		src, err := e.getSource(ctx, r.srcID)
		if err != nil {
			return err
		}
		var metaMap map[string]any
		_ = json.Unmarshal([]byte(r.metaJSON), &metaMap)

		normObs := connectors.NormalizedObservation{
			ObservationID:   r.obsID,
			SourceID:        r.srcID,
			SourceVersion:   r.srcVer,
			CanonicalURL:    r.url,
			Title:           asString(metaMap["title"]),
			Summary:         asString(metaMap["summary"]),
			Language:        r.lang,
			PayloadType:     r.pType,
			LicenseClass:    r.lic,
			PublishedAt:     parseTimeOrNow(r.pubStr, e.now()),
			ObservedAt:      parseTimeOrNow(r.obsStr, e.now()),
			CapturedAt:      parseTimeOrNow(r.capStr, e.now()),
			RawHash:         r.rHash,
			ContentHash:     r.cHash,
			Metadata:        metaMap,
			ValidationState: r.valState,
		}
		if ents, ok := metaMap["entities"].([]any); ok {
			for _, it := range ents {
				if s, ok := it.(string); ok {
					normObs.Entities = append(normObs.Entities, s)
				}
			}
		}
		if m, ok := metaMap["metrics"].(map[string]any); ok {
			normObs.Metrics = m
		}
		if p, ok := metaMap["place"].(map[string]any); ok {
			normObs.Place = p
		}
		if doms, ok := metaMap["domains"].([]any); ok {
			for _, d := range doms {
				if s, ok := d.(string); ok {
					normObs.Domains = append(normObs.Domains, s)
				}
			}
		}
		if err := e.processDerivedClaimAndEvent(ctx, src, r.obsID, normObs, r.createdAt); err != nil {
			return err
		}
	}
	return nil
}

// ErasePII propagates an erasure request across claims, events, and normalized metadata
// while preserving immutable non-PII provenance lineage (AT-17).
func (e *RadarEngine) ErasePII(ctx context.Context, needle string) error {
	clean := strings.TrimSpace(needle)
	if clean == "" {
		return nil
	}
	nowStr := e.now().Format(time.RFC3339Nano)

	if _, err := e.DB.ExecContext(ctx, `
UPDATE radar_claims
SET claim_text = REPLACE(claim_text, ?, '[ERASED]'),
    entities_json = REPLACE(entities_json, ?, '[ERASED]'),
    updated_at = ?
WHERE claim_text LIKE ? OR entities_json LIKE ?`,
		clean, clean, nowStr, "%"+clean+"%", "%"+clean+"%"); err != nil {
		return err
	}

	if _, err := e.DB.ExecContext(ctx, `
UPDATE radar_events
SET actors_json = REPLACE(actors_json, ?, '[ERASED]'),
    updated_at = ?
WHERE actors_json LIKE ?`,
		clean, nowStr, "%"+clean+"%"); err != nil {
		return err
	}

	if _, err := e.DB.ExecContext(ctx, `
UPDATE radar_observations
SET metadata_json = REPLACE(metadata_json, ?, '[ERASED]'),
    updated_at = ?
WHERE metadata_json LIKE ?`,
		clean, nowStr, "%"+clean+"%"); err != nil {
		return err
	}

	e.recordAudit(ctx, "privacy", "erasure", "erase_pii", "operator", "", "[ERASED]", "GDPR/privacy erasure propagated", nowStr)
	return nil
}

func (e *RadarEngine) loadClaims(ctx context.Context, claimIDs []string) ([]SpecClaim, error) {
	out := make([]SpecClaim, 0, len(claimIDs))
	for _, cid := range claimIDs {
		row := e.DB.QueryRowContext(ctx, `
SELECT claim_id, observation_id, claim_type, claim_text, entities_json, COALESCE(metrics_json,''), COALESCE(time_range_start,''), COALESCE(time_range_end,''), COALESCE(place_json,''), confidence, confidence_band, extractor_version, created_at, updated_at, valid_from, valid_to
FROM radar_claims WHERE claim_id = ?`, cid)
		var c SpecClaim
		var validTo sql.NullString
		err := row.Scan(&c.ClaimID, &c.ObservationID, &c.ClaimType, &c.ClaimText, &c.EntitiesJSON, &c.MetricsJSON, &c.TimeRangeStart, &c.TimeRangeEnd, &c.PlaceJSON, &c.Confidence, &c.ConfidenceBand, &c.ExtractorVersion, &c.CreatedAt, &c.UpdatedAt, &c.ValidFrom, &validTo)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if validTo.Valid {
			v := validTo.String
			c.ValidTo = &v
		}
		out = append(out, c)
	}
	return out, nil
}

func (e *RadarEngine) loadProvenanceForClaims(ctx context.Context, claimIDs []string) ([]ProvenanceRecord, error) {
	seen := make(map[string]bool)
	out := make([]ProvenanceRecord, 0)
	for _, cid := range claimIDs {
		rows, err := e.DB.QueryContext(ctx, `
SELECT p.provenance_id, p.observation_id, p.source_id, p.source_version, p.capture_method, p.captured_at, COALESCE(p.canonical_url,''), p.raw_hash, p.content_hash, p.parser_version, p.normalizer_version, p.dedupe_version, p.scorer_version, COALESCE(p.transform_parents_json,''), p.extracted_at, p.license_class, p.privacy_class, COALESCE(p.reason_codes_json,''), p.created_at, p.updated_at, p.valid_from, p.valid_to
FROM radar_provenance p
JOIN radar_claims c ON c.observation_id = p.observation_id
WHERE c.claim_id = ?`, cid)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var p ProvenanceRecord
			var validTo sql.NullString
			if scanErr := rows.Scan(&p.ProvenanceID, &p.ObservationID, &p.SourceID, &p.SourceVersion, &p.CaptureMethod, &p.CapturedAt, &p.CanonicalURL, &p.RawHash, &p.ContentHash, &p.ParserVersion, &p.NormalizerVersion, &p.DedupeVersion, &p.ScorerVersion, &p.TransformParentsJSON, &p.ExtractedAt, &p.LicenseClass, &p.PrivacyClass, &p.ReasonCodesJSON, &p.CreatedAt, &p.UpdatedAt, &p.ValidFrom, &validTo); scanErr != nil {
				rows.Close()
				return nil, scanErr
			}
			if validTo.Valid {
				v := validTo.String
				p.ValidTo = &v
			}
			if !seen[p.ProvenanceID] {
				seen[p.ProvenanceID] = true
				out = append(out, p)
			}
		}
		rows.Close()
	}
	return out, nil
}

func (e *RadarEngine) recordAudit(ctx context.Context, entityType, entityID, action, actor, beforeJSON, afterJSON, reason, nowStr string) {
	auditID := newUUIDv7()
	_, _ = e.DB.ExecContext(ctx, `
INSERT INTO radar_audit_logs (audit_id, entity_type, entity_id, action, actor, before_json, after_json, reason, created_at, updated_at, valid_from, valid_to)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		auditID, entityType, entityID, action, actor, beforeJSON, afterJSON, reason, nowStr, nowStr, nowStr,
	)
}

func scanSpecEvent(scanner rowScanner) (SpecEvent, error) {
	var ev SpecEvent
	var endTS, validTo sql.NullString
	err := scanner.Scan(
		&ev.EventID, &ev.EventType, &ev.EventStatus, &ev.TimeRangeStart, &endTS,
		&ev.PlaceJSON, &ev.ActorsJSON, &ev.ClaimsJSON, &ev.MetricsJSON, &ev.TopicsJSON,
		&ev.Confidence, &ev.ConfidenceBand, &ev.Version, &ev.ScorerVersion,
		&ev.CreatedAt, &ev.UpdatedAt, &ev.ValidFrom, &validTo,
	)
	if err != nil {
		return SpecEvent{}, err
	}
	if endTS.Valid {
		v := endTS.String
		ev.TimeRangeEnd = &v
	}
	if validTo.Valid {
		v := validTo.String
		ev.ValidTo = &v
	}
	return ev, nil
}

func buildTopicTags(obs connectors.NormalizedObservation, reg *TaxonomyRegistry) []string {
	tags := make([]string, 0)
	seen := make(map[string]bool)
	for _, d := range obs.Domains {
		dc := DomainCode(strings.ToUpper(strings.TrimSpace(d)))
		if reg != nil && reg.IsValidDomain(dc) && !seen[string(dc)] {
			seen[string(dc)] = true
			tags = append(tags, string(dc))
		}
	}
	if len(tags) == 0 {
		tags = append(tags, string(DomainGeopolitics))
		seen[string(DomainGeopolitics)] = true
	}
	if reg != nil {
		if dom, ok := reg.Domain(DomainCode(tags[0])); ok && len(dom.PrimaryLenses) > 0 {
			lc := string(dom.PrimaryLenses[0])
			if !seen[lc] {
				seen[lc] = true
				tags = append(tags, lc)
			}
		}
	}
	sort.Strings(tags)
	return tags
}

func tokenizeSemantic(raw string) map[string]bool {
	lower := strings.ToLower(connectors.NormalizeNFKC(raw))
	var b strings.Builder
	for _, r := range lower {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == ' ' {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	out := make(map[string]bool)
	for _, tok := range strings.Fields(b.String()) {
		if len(tok) <= 1 || stopWords[tok] {
			continue
		}
		out[tok] = true
	}
	return out
}

func mergeStringSets(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range append(append([]string(nil), a...), b...) {
		clean := strings.TrimSpace(s)
		if clean != "" && !seen[clean] {
			seen[clean] = true
			out = append(out, clean)
		}
	}
	return out
}

func mergeMetrics(a, b map[string]any) map[string]any {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		if prev, exists := out[k]; exists && fmt.Sprintf("%v", prev) != fmt.Sprintf("%v", v) {
			out[k+"_contested"] = []any{prev, v}
		} else {
			out[k] = v
		}
	}
	return out
}

func sanitizeConfigJSON(raw string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return raw
	}
	clean, _ := connectors.SanitizeMetadata(m)
	b, _ := json.Marshal(clean)
	return string(b)
}

func newUUIDv7() string {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return id.String()
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func asBool(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

func parseTimeOrNow(raw string, fallback time.Time) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw)); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(raw)); err == nil {
		return t.UTC()
	}
	return fallback.UTC()
}

func deterministicHashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
