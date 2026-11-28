package connectors

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"
)

// ConnectorType identifies one of the 4 supported spec connectors (Section 3.1).
type ConnectorType string

const (
	ConnectorRSS            ConnectorType = "rss"
	ConnectorGitHubReleases ConnectorType = "github_releases"
	ConnectorWebFeedJSON    ConnectorType = "web_feed_json"
	ConnectorIDXBEI         ConnectorType = "idx_bei"
)

// Connector defines the common 4-phase acquisition contract (Discover, Acquire, Validate, Normalize).
type Connector interface {
	Type() ConnectorType
	Discover(ctx context.Context, checkpoint Checkpoint) ([]DiscoveredItem, Checkpoint, error)
	Acquire(ctx context.Context, item DiscoveredItem) (Observation, error)
	Validate(obs Observation) ValidationResult
	Normalize(obs Observation) (NormalizedObservation, error)
}

// DiscoveredItem is a lightweight discovery record emitted by Discover.
type DiscoveredItem struct {
	Locator     string            `json:"locator"`
	Title       string            `json:"title"`
	PublishedAt time.Time         `json:"published_at"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Fingerprint string            `json:"fingerprint"`
}

// Checkpoint is an idempotent resumption token persisted per source.
type Checkpoint struct {
	ConnectorType ConnectorType `json:"connector_type"`
	SourceID      string        `json:"source_id"`
	Cursor        string        `json:"cursor"`
	LastSeenAt    time.Time     `json:"last_seen_at"`
	ItemCount     int64         `json:"item_count"`
	Hash          string        `json:"hash"`
}

// ComputeHash computes the deterministic SHA-256 integrity hash over cursor + last_seen_at.
func (c Checkpoint) ComputeHash() string {
	h := sha256.New()
	h.Write([]byte(string(c.ConnectorType)))
	h.Write([]byte{0})
	h.Write([]byte(c.SourceID))
	h.Write([]byte{0})
	h.Write([]byte(c.Cursor))
	h.Write([]byte{0})
	if !c.LastSeenAt.IsZero() {
		h.Write([]byte(c.LastSeenAt.UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// VerifyHash reports whether Hash matches ComputeHash().
func (c Checkpoint) VerifyHash() bool {
	if c.Hash == "" {
		return c.Cursor == "" && c.LastSeenAt.IsZero()
	}
	return c.Hash == c.ComputeHash()
}

// CheckpointContract documents and validates the 5 idempotency guarantees (Section 3.1):
// 1. Monotonic, 2. Deterministic, 3. Idempotent, 4. Durable, 5. Recoverable.
type CheckpointContract struct {
	Monotonic     bool
	Deterministic bool
	Idempotent    bool
	Durable       bool
	Recoverable   bool
}

// ValidateAdvance ensures a new checkpoint never moves backwards in time or item count.
func (CheckpointContract) ValidateAdvance(prev, next Checkpoint) error {
	if prev.SourceID != "" && next.SourceID != "" && prev.SourceID != next.SourceID {
		return fmt.Errorf("checkpoint source mismatch: prev=%q next=%q", prev.SourceID, next.SourceID)
	}
	if !prev.LastSeenAt.IsZero() && !next.LastSeenAt.IsZero() && next.LastSeenAt.Before(prev.LastSeenAt) {
		return fmt.Errorf("checkpoint non-monotonic timestamp: prev=%s next=%s", prev.LastSeenAt.UTC(), next.LastSeenAt.UTC())
	}
	if next.ItemCount < prev.ItemCount {
		return fmt.Errorf("checkpoint non-monotonic item_count: prev=%d next=%d", prev.ItemCount, next.ItemCount)
	}
	return nil
}

// Observation represents a raw acquired item prior to validation and normalization.
type Observation struct {
	ObservationID string            `json:"observation_id"`
	SourceID      string            `json:"source_id"`
	SourceVersion string            `json:"source_version"`
	ConnectorType ConnectorType     `json:"connector_type"`
	CanonicalURL  string            `json:"canonical_url"`
	Title         string            `json:"title"`
	Summary       string            `json:"summary"`
	Body          string            `json:"body"`
	Language      string            `json:"language"`
	PayloadType   string            `json:"payload_type"`
	LicenseClass  string            `json:"license_class"`
	PublishedAt   time.Time         `json:"published_at"`
	ObservedAt    time.Time         `json:"observed_at"`
	CapturedAt    time.Time         `json:"captured_at"`
	RawPayload    []byte            `json:"raw_payload"`
	RawHash       string            `json:"raw_hash"`
	ContentHash   string            `json:"content_hash"`
	Metadata      map[string]any    `json:"metadata,omitempty"`
	Tags          []string          `json:"tags,omitempty"`
	Metrics       map[string]any    `json:"metrics,omitempty"`
	Entities      []string          `json:"entities,omitempty"`
	Place         map[string]any    `json:"place,omitempty"`
	ExtraHeaders  map[string]string `json:"-"` // never serialized into metadata/provenance
}

// ValidationResult captures schema validation outcome and reason codes.
type ValidationResult struct {
	Valid           bool     `json:"valid"`
	ValidationState string   `json:"validation_state"` // accepted, quarantined, rejected
	ReasonCodes     []string `json:"reason_codes,omitempty"`
	SchemaDrift     bool     `json:"schema_drift,omitempty"`
	UnknownFields   []string `json:"unknown_fields,omitempty"`
}

// NormalizedObservation is the canonical UTF-8 NFKC, UTC, privacy-filtered output of Normalize.
type NormalizedObservation struct {
	ObservationID   string         `json:"observation_id"`
	SourceID        string         `json:"source_id"`
	SourceVersion   string         `json:"source_version"`
	CanonicalURL    string         `json:"canonical_url"`
	Title           string         `json:"title"`
	Summary         string         `json:"summary"`
	Body            string         `json:"body"`
	Language        string         `json:"language"`
	PayloadType     string         `json:"payload_type"`
	LicenseClass    string         `json:"license_class"`
	PublishedAt     time.Time      `json:"published_at"`
	ObservedAt      time.Time      `json:"observed_at"`
	CapturedAt      time.Time      `json:"captured_at"`
	RawHash         string         `json:"raw_hash"`
	ContentHash     string         `json:"content_hash"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	Metrics         map[string]any `json:"metrics,omitempty"`
	Entities        []string       `json:"entities,omitempty"`
	Place           map[string]any `json:"place,omitempty"`
	Domains         []string       `json:"domains,omitempty"`
	PrivacyClass    string         `json:"privacy_class"`
	PIIRedacted     bool           `json:"pii_redacted"`
	ValidationState string         `json:"validation_state"`
	ReasonCodes     []string       `json:"reason_codes,omitempty"`
}

// NormalizeNFKC converts text to Unicode NFKC and normalizes whitespace.
func NormalizeNFKC(raw string) string {
	clean := norm.NFKC.String(strings.TrimSpace(raw))
	return strings.Join(strings.Fields(clean), " ")
}

// HashSHA256Hex computes a lowercase hex SHA-256 digest of b.
func HashSHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// DeterministicObservationID builds source_id|source_version|canonical_locator|content_hash (Section 2.1).
func DeterministicObservationID(sourceID, sourceVersion, locator, contentHash string) string {
	return fmt.Sprintf("%s|%s|%s|%s",
		strings.TrimSpace(sourceID),
		firstNonEmpty(strings.TrimSpace(sourceVersion), "v1"),
		strings.TrimSpace(locator),
		strings.TrimSpace(contentHash),
	)
}

var (
	emailPattern  = regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)
	phonePattern  = regexp.MustCompile(`(?:\+?\d{1,3}[\s\-.]?)?(?:\(?\d{2,4}\)?[\s\-.]?)\d{3,4}[\s\-.]?\d{4}`)
	handlePattern = regexp.MustCompile(`(?:^|[\s(])(@[A-Za-z0-9_]{2,30})`)
	secretPattern = regexp.MustCompile(`(?i)(bearer\s+[A-Za-z0-9\-._~+/]+=*|ghp_[A-Za-z0-9]{10,}|x-bei-api-key:\s*\S+|api[_-]?key=\S+)`)
)

// RedactPII redacts emails and phones, pseudonymizes social handles, and strips secret tokens.
func RedactPII(raw string) (string, bool) {
	redacted := false
	out := secretPattern.ReplaceAllStringFunc(raw, func(_ string) string {
		redacted = true
		return "[SECRET_REDACTED]"
	})
	out = emailPattern.ReplaceAllStringFunc(out, func(_ string) string {
		redacted = true
		return "[REDACTED]"
	})
	out = phonePattern.ReplaceAllStringFunc(out, func(_ string) string {
		redacted = true
		return "[REDACTED]"
	})
	out = handlePattern.ReplaceAllStringFunc(out, func(match string) string {
		idx := strings.Index(match, "@")
		if idx < 0 {
			return match
		}
		prefix := match[:idx]
		handle := match[idx:]
		sum := HashSHA256Hex([]byte(strings.ToLower(handle)))
		redacted = true
		return prefix + "anon_" + sum[:8]
	})
	return out, redacted
}

// FuzzLocationPrecision rounds lat/lon coordinates to approximately precisionMeters (default 1000m).
func FuzzLocationPrecision(place map[string]any, precisionMeters float64) (map[string]any, bool) {
	if len(place) == 0 {
		return nil, false
	}
	if precisionMeters <= 0 {
		precisionMeters = 1000
	}
	out := make(map[string]any, len(place))
	fuzzed := false
	stepDeg := precisionMeters / 111320.0
	if stepDeg <= 0 {
		stepDeg = 0.01
	}
	for k, v := range place {
		lk := strings.ToLower(k)
		if lk == "lat" || lk == "latitude" || lk == "lon" || lk == "lng" || lk == "longitude" {
			if f, ok := toFloat64(v); ok {
				out[k] = math.Round(f/stepDeg) * stepDeg
				fuzzed = true
				continue
			}
		}
		if lk == "exact_address" || lk == "street_address" {
			out[k] = "[REDACTED]"
			fuzzed = true
			continue
		}
		out[k] = v
	}
	return out, fuzzed
}

// SanitizeMetadata strips secret keys/values and redacts PII from metadata maps.
func SanitizeMetadata(meta map[string]any) (map[string]any, bool) {
	if len(meta) == 0 {
		return nil, false
	}
	out := make(map[string]any, len(meta))
	redacted := false
	for k, v := range meta {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "token") || strings.Contains(lk, "secret") || strings.Contains(lk,
			"password") || strings.Contains(lk, "api_key") || strings.Contains(lk, "apikey") || strings.Contains(lk,
			"authorization") {
			redacted = true
			continue
		}
		switch val := v.(type) {
		case string:
			clean, hit := RedactPII(val)
			if hit {
				redacted = true
			}
			out[k] = clean
		case map[string]any:
			nested, hit := SanitizeMetadata(val)
			if hit {
				redacted = true
			}
			out[k] = nested
		default:
			out[k] = v
		}
	}
	return out, redacted
}

// StandardNormalize performs common UTF-8 NFKC normalization, UTC conversion, and privacy gating.
func StandardNormalize(obs Observation, defaultDomains []string) (NormalizedObservation, error) {
	title, tRedacted := RedactPII(NormalizeNFKC(obs.Title))
	summary, sRedacted := RedactPII(NormalizeNFKC(obs.Summary))
	body, bRedacted := RedactPII(NormalizeNFKC(obs.Body))
	meta, mRedacted := SanitizeMetadata(obs.Metadata)
	place, pFuzzed := FuzzLocationPrecision(obs.Place, 1000)

	piiRedacted := tRedacted || sRedacted || bRedacted || mRedacted || pFuzzed
	privacyClass := "public"
	if piiRedacted {
		privacyClass = "pii_redacted"
	}

	pub := obs.PublishedAt.UTC()
	if pub.IsZero() {
		pub = obs.CapturedAt.UTC()
	}
	if pub.IsZero() {
		pub = time.Now().UTC()
	}
	obsAt := obs.ObservedAt.UTC()
	if obsAt.IsZero() {
		obsAt = pub
	}
	capAt := obs.CapturedAt.UTC()
	if capAt.IsZero() {
		capAt = time.Now().UTC()
	}

	contentBytes, _ := json.Marshal(map[string]any{
		"title":   title,
		"summary": summary,
		"body":    body,
		"url":     strings.TrimSpace(obs.CanonicalURL),
	})
	contentHash := obs.ContentHash
	if contentHash == "" {
		contentHash = HashSHA256Hex(contentBytes)
	}
	rawHash := obs.RawHash
	if rawHash == "" {
		rawHash = HashSHA256Hex(obs.RawPayload)
	}

	domains := make([]string, 0, len(obs.Tags)+len(defaultDomains))
	seen := make(map[string]bool)
	for _, d := range append(obs.Tags, defaultDomains...) {
		normD := strings.ToUpper(strings.TrimSpace(d))
		if len(normD) == 3 && normD[0] == 'D' && !seen[normD] {
			seen[normD] = true
			domains = append(domains, normD)
		}
	}

	obsID := obs.ObservationID
	if obsID == "" {
		obsID = DeterministicObservationID(obs.SourceID, obs.SourceVersion, obs.CanonicalURL, contentHash)
	}

	return NormalizedObservation{
		ObservationID:   obsID,
		SourceID:        obs.SourceID,
		SourceVersion:   firstNonEmpty(obs.SourceVersion, "v1"),
		CanonicalURL:    strings.TrimSpace(obs.CanonicalURL),
		Title:           title,
		Summary:         summary,
		Body:            body,
		Language:        firstNonEmpty(strings.ToLower(strings.TrimSpace(obs.Language)), "en"),
		PayloadType:     firstNonEmpty(obs.PayloadType, "structured_record"),
		LicenseClass:    firstNonEmpty(obs.LicenseClass, "redistribute_allowed"),
		PublishedAt:     pub,
		ObservedAt:      obsAt,
		CapturedAt:      capAt,
		RawHash:         rawHash,
		ContentHash:     contentHash,
		Metadata:        meta,
		Metrics:         obs.Metrics,
		Entities:        obs.Entities,
		Place:           place,
		Domains:         domains,
		PrivacyClass:    privacyClass,
		PIIRedacted:     piiRedacted,
		ValidationState: "accepted",
	}, nil
}

func doHTTPGet(ctx context.Context, client *http.Client, targetURL string, headers map[string]string) (int, http.Header,
	[]byte, error) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "WorldRadar/1.0")
	for k, v := range headers {
		if strings.TrimSpace(v) != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("http get %s: %w", targetURL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resp.StatusCode, resp.Header, nil, fmt.Errorf("read response: %w", err)
	}
	return resp.StatusCode, resp.Header, body, nil
}

func parseFlexibleTime(raw string, fallback time.Time) time.Time {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		if fallback.IsZero() {
			return time.Now().UTC()
		}
		return fallback.UTC()
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		time.RFC1123Z,
		time.RFC1123,
		time.RFC822Z,
		time.RFC822,
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, trimmed); err == nil {
			return t.UTC()
		}
	}
	if fallback.IsZero() {
		return time.Now().UTC()
	}
	return fallback.UTC()
}

func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
