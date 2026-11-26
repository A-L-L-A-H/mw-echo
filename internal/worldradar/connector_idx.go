package worldradar

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
)

// IDXBEIConnector acquires Indonesia Stock Exchange (BEI/IDX) corporate disclosures and market anomaly feeds.
type IDXBEIConnector struct{}

// ID returns the canonical connector identifier for IDX/BEI feeds.
func (c *IDXBEIConnector) ID() string { return "idx_bei" }

// RegisterIDXConnector registers IDXBEIConnector on the provided ConnectorRegistry.
func RegisterIDXConnector(reg *ConnectorRegistry) {
	if reg == nil {
		return
	}
	reg.Register(&IDXBEIConnector{})
}

type idxFeedEnvelope struct {
	Announcements []idxAnnouncement `json:"announcements"`
	Stocks        []idxStockQuote   `json:"stocks"`
}

type idxAnnouncement struct {
	ID          string  `json:"id"`
	Ticker      string  `json:"ticker"`
	CompanyName string  `json:"companyName"`
	Title       string  `json:"title"`
	Summary     string  `json:"summary"`
	Category    string  `json:"category"`
	PublishedAt string  `json:"publishedAt"`
	URL         string  `json:"url"`
	ChangePct   float64 `json:"changePct"`
	VolumeRatio float64 `json:"volumeRatio"`
}

type idxStockQuote struct {
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	LastPrice   float64 `json:"lastPrice"`
	ChangePct   float64 `json:"changePct"`
	VolumeRatio float64 `json:"volumeRatio"`
	UpdatedAt   string  `json:"updatedAt"`
	URL         string  `json:"url"`
}

type idxRecordPayload struct {
	Ticker      string  `json:"ticker"`
	ChangePct   float64 `json:"changePct"`
	VolumeRatio float64 `json:"volumeRatio"`
	Exchange    string  `json:"exchange"`
	Category    string  `json:"category,omitempty"`
	LastPrice   float64 `json:"lastPrice,omitempty"`
}

// Collect fetches and normalizes IDX/BEI announcement and stock quote JSON feeds using conditional GET.
func (c *IDXBEIConnector) Collect(ctx context.Context, req CollectRequest) CollectResult {
	statusCode, respHeaders, bodyBytes, err := performConditionalGET(
		ctx, req.HTTPClient, req.Target.SourceURI, req.Target.ETag,
		"application/json",
	)
	if err != nil {
		return CollectResult{
			Status:       RunStatusConnectorFailed,
			ETag:         req.Target.ETag,
			NextCursor:   req.Target.Cursor,
			ErrorMessage: err.Error(),
		}
	}
	if statusCode == http.StatusNotModified {
		return CollectResult{
			Status:     RunStatusNoNewData,
			ETag:       firstNonEmpty(respHeaders.Get("ETag"), req.Target.ETag),
			NextCursor: req.Target.Cursor,
		}
	}
	if statusCode == http.StatusTooManyRequests {
		return CollectResult{
			Status:       RunStatusRateLimited,
			ETag:         req.Target.ETag,
			NextCursor:   req.Target.Cursor,
			RetryAfter:   parseRetryAfter(respHeaders, req.Now),
			ErrorMessage: "idx_bei rate limited (http 429)",
		}
	}
	if statusCode < 200 || statusCode >= 300 {
		return CollectResult{
			Status:       RunStatusConnectorFailed,
			ETag:         req.Target.ETag,
			NextCursor:   req.Target.Cursor,
			ErrorMessage: fmt.Sprintf("idx_bei http status %d", statusCode),
		}
	}

	nextETag := firstNonEmpty(respHeaders.Get("ETag"), req.Target.ETag)
	trimmed := strings.TrimSpace(string(bodyBytes))
	if trimmed == "" {
		return CollectResult{
			Status:     RunStatusNoNewData,
			ETag:       nextETag,
			NextCursor: req.Target.Cursor,
		}
	}

	var envelope idxFeedEnvelope
	if err := json.Unmarshal(bodyBytes, &envelope); err != nil {
		return CollectResult{
			Status:       RunStatusConnectorFailed,
			ETag:         req.Target.ETag,
			NextCursor:   req.Target.Cursor,
			ErrorMessage: fmt.Sprintf("decode idx_bei json: %v", err),
		}
	}

	records := make([]CollectedRecord, 0, len(envelope.Announcements)+len(envelope.Stocks))
	for _, announcement := range envelope.Announcements {
		record, ok := buildIDXAnnouncementRecord(announcement, req.Target.SourceURI, req.Now)
		if ok {
			records = append(records, record)
		}
	}
	for _, stockQuote := range envelope.Stocks {
		record, ok := buildIDXStockRecord(stockQuote, req.Target.SourceURI, req.Now)
		if ok {
			records = append(records, record)
		}
	}

	if len(records) == 0 {
		return CollectResult{
			Status:     RunStatusNoNewData,
			ETag:       nextETag,
			NextCursor: req.Target.Cursor,
		}
	}
	return CollectResult{
		Status:     RunStatusOK,
		ETag:       nextETag,
		NextCursor: req.Target.Cursor,
		Records:    records,
	}
}

func buildIDXAnnouncementRecord(announcement idxAnnouncement, feedURI string, now time.Time) (CollectedRecord, bool) {
	upperTicker := strings.ToUpper(strings.TrimSpace(announcement.Ticker))
	companyName := firstNonEmpty(strings.TrimSpace(announcement.CompanyName), upperTicker)
	title := strings.TrimSpace(announcement.Title)
	summary := strings.TrimSpace(announcement.Summary)
	if title == "" && summary == "" {
		return CollectedRecord{}, false
	}
	if upperTicker != "" && title != "" && !strings.Contains(strings.ToUpper(title), upperTicker) {
		title = fmt.Sprintf("[%s] %s", upperTicker, title)
	}
	if summary == "" {
		summary = title
	}

	pubTime, precision := parseTimestampWithPrecision(announcement.PublishedAt, now)
	impactHint, urgencyHint := computeIDXMarketHints(announcement.ChangePct, announcement.VolumeRatio)
	strongID := ""
	if upperTicker != "" {
		strongID = "IDX:" + upperTicker
	}

	payloadBytes, _ := json.Marshal(idxRecordPayload{
		Ticker:      upperTicker,
		ChangePct:   announcement.ChangePct,
		VolumeRatio: announcement.VolumeRatio,
		Exchange:    "IDX",
		Category:    strings.TrimSpace(announcement.Category),
	})

	extID := firstNonEmpty(
		strings.TrimSpace(announcement.ID),
		strings.TrimSpace(announcement.URL),
		fmt.Sprintf("idx:ann:%s:%s", upperTicker, pubTime.UTC().Format(time.RFC3339)),
	)

	return CollectedRecord{
		ExternalID:    extID,
		SourceURI:     firstNonEmpty(strings.TrimSpace(announcement.URL), feedURI),
		Title:         title,
		Summary:       summary,
		Language:      "id",
		MimeType:      "application/json",
		RawExcerpt:    truncateText(firstNonEmpty(summary, title), 2000),
		OccurredAt:    pubTime.UTC().Format(time.RFC3339Nano),
		PublishedAt:   pubTime.UTC().Format(time.RFC3339Nano),
		TimePrecision: precision,
		EntityName:    companyName,
		EntityType:    "company",
		StrongID:      strongID,
		Domains:       []string{"D04", "D05"},
		ImpactHint:    impactHint,
		UrgencyHint:   urgencyHint,
		PayloadJSON:   string(payloadBytes),
	}, true
}

func buildIDXStockRecord(stockQuote idxStockQuote, feedURI string, now time.Time) (CollectedRecord, bool) {
	upperTicker := strings.ToUpper(strings.TrimSpace(stockQuote.Code))
	companyName := firstNonEmpty(strings.TrimSpace(stockQuote.Name), upperTicker)
	if upperTicker == "" && companyName == "" {
		return CollectedRecord{}, false
	}

	pubTime, precision := parseTimestampWithPrecision(stockQuote.UpdatedAt, now)
	impactHint, urgencyHint := computeIDXMarketHints(stockQuote.ChangePct, stockQuote.VolumeRatio)
	strongID := ""
	if upperTicker != "" {
		strongID = "IDX:" + upperTicker
	}

	title := fmt.Sprintf("IDX:%s %s (%+.2f%%, vol %.2fx)", upperTicker, companyName, stockQuote.ChangePct, stockQuote.VolumeRatio)
	summary := fmt.Sprintf("IDX stock %s (%s) lastPrice=%.2f changePct=%+.2f%% volumeRatio=%.2fx",
		upperTicker, companyName, stockQuote.LastPrice, stockQuote.ChangePct, stockQuote.VolumeRatio)

	payloadBytes, _ := json.Marshal(idxRecordPayload{
		Ticker:      upperTicker,
		ChangePct:   stockQuote.ChangePct,
		VolumeRatio: stockQuote.VolumeRatio,
		Exchange:    "IDX",
		LastPrice:   stockQuote.LastPrice,
	})

	extID := fmt.Sprintf("idx:stock:%s:%s", upperTicker, pubTime.UTC().Format(time.RFC3339))

	return CollectedRecord{
		ExternalID:    extID,
		SourceURI:     firstNonEmpty(strings.TrimSpace(stockQuote.URL), feedURI),
		Title:         title,
		Summary:       summary,
		Language:      "id",
		MimeType:      "application/json",
		RawExcerpt:    truncateText(summary, 2000),
		OccurredAt:    pubTime.UTC().Format(time.RFC3339Nano),
		PublishedAt:   pubTime.UTC().Format(time.RFC3339Nano),
		TimePrecision: precision,
		EntityName:    companyName,
		EntityType:    "company",
		StrongID:      strongID,
		Domains:       []string{"D04", "D05"},
		ImpactHint:    impactHint,
		UrgencyHint:   urgencyHint,
		PayloadJSON:   string(payloadBytes),
	}, true
}

func computeIDXMarketHints(changePct, volumeRatio float64) (float64, float64) {
	absMove := math.Abs(changePct)
	impact := 0.45
	urgency := 0.40

	if absMove >= 5.0 || volumeRatio >= 3.0 {
		moveScore := math.Min(1.0, 0.65+(absMove/25.0)*0.35)
		volScore := math.Min(1.0, 0.65+(volumeRatio/10.0)*0.35)
		impact = math.Max(moveScore, volScore)
		urgency = math.Min(1.0, math.Max(0.75, impact+0.05))
		return impact, urgency
	}

	if absMove > 0 {
		impact = math.Min(0.64, 0.45+(absMove/5.0)*0.18)
	}
	if volumeRatio > 1.0 {
		urgency = math.Min(0.64, 0.40+((volumeRatio-1.0)/2.0)*0.20)
	}
	return impact, urgency
}
