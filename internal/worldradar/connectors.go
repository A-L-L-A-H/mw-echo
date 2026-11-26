package worldradar

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	RunStatusOK              = "ok"
	RunStatusNoNewData       = "no_new_data"
	RunStatusConnectorFailed = "connector_failed"
	RunStatusRateLimited     = "rate_limited"
	RunStatusPaused          = "paused"
)

// CollectedRecord represents a normalized raw observation payload produced by a SourceConnector.
type CollectedRecord struct {
	ExternalID    string   `json:"externalId"`
	SourceURI     string   `json:"sourceUri"`
	Title         string   `json:"title"`
	Summary       string   `json:"summary"`
	Language      string   `json:"language"`
	MimeType      string   `json:"mimeType"`
	RawExcerpt    string   `json:"rawExcerpt"`
	OccurredAt    string   `json:"occurredAt"`
	PublishedAt   string   `json:"publishedAt"`
	TimePrecision string   `json:"timePrecision"`
	EntityName    string   `json:"entityName,omitempty"`
	EntityType    string   `json:"entityType,omitempty"`
	StrongID      string   `json:"strongId,omitempty"`
	Domains       []string `json:"domains,omitempty"`
	ImpactHint    float64  `json:"impactHint,omitempty"`
	UrgencyHint   float64  `json:"urgencyHint,omitempty"`
	PayloadJSON   string   `json:"payloadJson,omitempty"`
}

// CollectRequest supplies target state and HTTP transport to a SourceConnector.
type CollectRequest struct {
	Target        WatchTarget
	HTTPClient    *http.Client
	GitHubAPIBase string
	Now           time.Time
}

// CollectResult captures connector execution status, checkpoint updates, and acquired records.
type CollectResult struct {
	Status       string
	ETag         string
	NextCursor   string
	RetryAfter   time.Duration
	ErrorMessage string
	Records      []CollectedRecord
}

// SourceConnector defines a permitted source acquisition adapter.
type SourceConnector interface {
	ID() string
	Collect(ctx context.Context, req CollectRequest) CollectResult
}

// ConnectorRegistry maps connector IDs to SourceConnector implementations.
type ConnectorRegistry struct {
	connectors map[string]SourceConnector
}

// NewConnectorRegistry creates a registry preloaded with rss, github, web_feed, arxiv, cyber_kev, usgs_geo, and idx_bei connectors.
func NewConnectorRegistry() *ConnectorRegistry {
	reg := &ConnectorRegistry{
		connectors: make(map[string]SourceConnector),
	}
	reg.Register(&RSSConnector{})
	reg.Register(&GitHubConnector{})
	reg.Register(&WebFeedConnector{})
	reg.Register(&ArxivConnector{})
	reg.Register(&CyberKEVConnector{})
	reg.Register(&USGSGeoConnector{})
	RegisterIDXConnector(reg)
	return reg
}

// Register adds or replaces a SourceConnector in the registry.
func (r *ConnectorRegistry) Register(connector SourceConnector) {
	if connector == nil {
		return
	}
	r.connectors[strings.ToLower(strings.TrimSpace(connector.ID()))] = connector
}

// Get retrieves a registered connector by ID.
func (r *ConnectorRegistry) Get(connectorID string) (SourceConnector, bool) {
	connector, ok := r.connectors[strings.ToLower(strings.TrimSpace(connectorID))]
	return connector, ok
}

// RSSConnector acquires RSS 2.0 and Atom feeds with ETag conditional requests.
type RSSConnector struct{}

func (c *RSSConnector) ID() string { return "rss" }

type rssEnvelope struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Title    string    `xml:"title"`
		Language string    `xml:"language"`
		Items    []rssItem `xml:"item"`
	} `xml:"channel"`
}

type rssItem struct {
	Title       string   `xml:"title"`
	Link        string   `xml:"link"`
	GUID        string   `xml:"guid"`
	PubDate     string   `xml:"pubDate"`
	OccurredAt  string   `xml:"occurredAt"`
	Description string   `xml:"description"`
	Categories  []string `xml:"category"`
	Entity      string   `xml:"entity"`
	StrongID    string   `xml:"strongId"`
	Impact      float64  `xml:"impact"`
	Urgency     float64  `xml:"urgency"`
}

type atomFeed struct {
	XMLName xml.Name    `xml:"feed"`
	Title   string      `xml:"title"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	ID         string `xml:"id"`
	Title      string `xml:"title"`
	Updated    string `xml:"updated"`
	Published  string `xml:"published"`
	OccurredAt string `xml:"occurredAt"`
	Summary    string `xml:"summary"`
	Content    string `xml:"content"`
	Link       struct {
		Href string `xml:"href,attr"`
	} `xml:"link"`
	Categories []struct {
		Term string `xml:"term,attr"`
	} `xml:"category"`
	Entity   string  `xml:"entity"`
	StrongID string  `xml:"strongId"`
	Impact   float64 `xml:"impact"`
	Urgency  float64 `xml:"urgency"`
}

func (c *RSSConnector) Collect(ctx context.Context, req CollectRequest) CollectResult {
	statusCode, respHeaders, bodyBytes, err := performConditionalGET(ctx, req.HTTPClient, req.Target.SourceURI, req.Target.ETag, "application/rss+xml, application/atom+xml, application/xml, text/xml")
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
			ErrorMessage: "http 429 rate limited",
		}
	}
	if statusCode < 200 || statusCode >= 300 {
		return CollectResult{
			Status:       RunStatusConnectorFailed,
			ETag:         req.Target.ETag,
			NextCursor:   req.Target.Cursor,
			ErrorMessage: fmt.Sprintf("http status %d", statusCode),
		}
	}

	nextETag := firstNonEmpty(respHeaders.Get("ETag"), req.Target.ETag)
	records, parseErr := parseRSSOrAtom(bodyBytes, req.Target.SourceURI, req.Now)
	if parseErr != nil {
		return CollectResult{
			Status:       RunStatusConnectorFailed,
			ETag:         req.Target.ETag,
			NextCursor:   req.Target.Cursor,
			ErrorMessage: parseErr.Error(),
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

func parseRSSOrAtom(bodyBytes []byte, feedURI string, now time.Time) ([]CollectedRecord, error) {
	trimmed := strings.TrimSpace(string(bodyBytes))
	if trimmed == "" {
		return nil, nil
	}

	var rssDoc rssEnvelope
	if err := xml.Unmarshal(bodyBytes, &rssDoc); err == nil && strings.EqualFold(rssDoc.XMLName.Local, "rss") {
		lang := firstNonEmpty(strings.TrimSpace(rssDoc.Channel.Language), "en")
		records := make([]CollectedRecord, 0, len(rssDoc.Channel.Items))
		for _, feedItem := range rssDoc.Channel.Items {
			title := strings.TrimSpace(feedItem.Title)
			summary := strings.TrimSpace(feedItem.Description)
			if title == "" && summary == "" {
				continue
			}
			pubTime, pubPrecision := parseTimestampWithPrecision(feedItem.PubDate, now)
			occTime, occPrecision := parseTimestampWithPrecision(feedItem.OccurredAt, pubTime)
			if strings.TrimSpace(feedItem.OccurredAt) == "" {
				occPrecision = pubPrecision
			}
			extID := firstNonEmpty(strings.TrimSpace(feedItem.GUID), strings.TrimSpace(feedItem.Link), title)
			link := firstNonEmpty(strings.TrimSpace(feedItem.Link), feedURI)
			rawPayload, _ := json.Marshal(feedItem)
			records = append(records, CollectedRecord{
				ExternalID:    extID,
				SourceURI:     link,
				Title:         title,
				Summary:       summary,
				Language:      lang,
				MimeType:      "application/rss+xml",
				RawExcerpt:    truncateText(firstNonEmpty(summary, title), 2000),
				OccurredAt:    occTime.UTC().Format(time.RFC3339Nano),
				PublishedAt:   pubTime.UTC().Format(time.RFC3339Nano),
				TimePrecision: occPrecision,
				EntityName:    strings.TrimSpace(feedItem.Entity),
				StrongID:      strings.TrimSpace(feedItem.StrongID),
				Domains:       extractDomainTags(feedItem.Categories),
				ImpactHint:    feedItem.Impact,
				UrgencyHint:   feedItem.Urgency,
				PayloadJSON:   string(rawPayload),
			})
		}
		return records, nil
	}

	var atomDoc atomFeed
	if err := xml.Unmarshal(bodyBytes, &atomDoc); err == nil && strings.EqualFold(atomDoc.XMLName.Local, "feed") {
		records := make([]CollectedRecord, 0, len(atomDoc.Entries))
		for _, entry := range atomDoc.Entries {
			title := strings.TrimSpace(entry.Title)
			summary := firstNonEmpty(strings.TrimSpace(entry.Summary), strings.TrimSpace(entry.Content))
			if title == "" && summary == "" {
				continue
			}
			rawPub := firstNonEmpty(strings.TrimSpace(entry.Published), strings.TrimSpace(entry.Updated))
			pubTime, pubPrecision := parseTimestampWithPrecision(rawPub, now)
			occTime, occPrecision := parseTimestampWithPrecision(entry.OccurredAt, pubTime)
			if strings.TrimSpace(entry.OccurredAt) == "" {
				occPrecision = pubPrecision
			}
			extID := firstNonEmpty(strings.TrimSpace(entry.ID), strings.TrimSpace(entry.Link.Href), title)
			link := firstNonEmpty(strings.TrimSpace(entry.Link.Href), feedURI)
			var terms []string
			for _, cat := range entry.Categories {
				terms = append(terms, cat.Term)
			}
			rawPayload, _ := json.Marshal(entry)
			records = append(records, CollectedRecord{
				ExternalID:    extID,
				SourceURI:     link,
				Title:         title,
				Summary:       summary,
				Language:      "en",
				MimeType:      "application/atom+xml",
				RawExcerpt:    truncateText(firstNonEmpty(summary, title), 2000),
				OccurredAt:    occTime.UTC().Format(time.RFC3339Nano),
				PublishedAt:   pubTime.UTC().Format(time.RFC3339Nano),
				TimePrecision: occPrecision,
				EntityName:    strings.TrimSpace(entry.Entity),
				StrongID:      strings.TrimSpace(entry.StrongID),
				Domains:       extractDomainTags(terms),
				ImpactHint:    entry.Impact,
				UrgencyHint:   entry.Urgency,
				PayloadJSON:   string(rawPayload),
			})
		}
		return records, nil
	}

	return nil, fmt.Errorf("invalid rss or atom XML document")
}

// GitHubConnector collects releases, commits, or repository metadata from GitHub REST endpoints.
type GitHubConnector struct{}

func (c *GitHubConnector) ID() string { return "github" }

type githubRelease struct {
	ID          int64  `json:"id"`
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	HTMLURL     string `json:"html_url"`
	Body        string `json:"body"`
	PublishedAt string `json:"published_at"`
	CreatedAt   string `json:"created_at"`
}

type githubCommit struct {
	SHA     string `json:"sha"`
	HTMLURL string `json:"html_url"`
	Commit  struct {
		Message string `json:"message"`
		Author  struct {
			Name string `json:"name"`
			Date string `json:"date"`
		} `json:"author"`
	} `json:"commit"`
}

func (c *GitHubConnector) Collect(ctx context.Context, req CollectRequest) CollectResult {
	endpoint, repoSlug := resolveGitHubEndpoint(req.Target.SourceURI, req.GitHubAPIBase)
	statusCode, respHeaders, bodyBytes, err := performConditionalGET(ctx, req.HTTPClient, endpoint, req.Target.ETag, "application/vnd.github+json")
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
	isRateLimited := statusCode == http.StatusTooManyRequests ||
		(statusCode == http.StatusForbidden && strings.TrimSpace(respHeaders.Get("X-RateLimit-Remaining")) == "0")
	if isRateLimited {
		return CollectResult{
			Status:       RunStatusRateLimited,
			ETag:         req.Target.ETag,
			NextCursor:   req.Target.Cursor,
			RetryAfter:   parseRetryAfter(respHeaders, req.Now),
			ErrorMessage: fmt.Sprintf("github rate limited (http %d)", statusCode),
		}
	}
	if statusCode < 200 || statusCode >= 300 {
		return CollectResult{
			Status:       RunStatusConnectorFailed,
			ETag:         req.Target.ETag,
			NextCursor:   req.Target.Cursor,
			ErrorMessage: fmt.Sprintf("github http status %d", statusCode),
		}
	}

	nextETag := firstNonEmpty(respHeaders.Get("ETag"), req.Target.ETag)
	records, nextCursor, parseErr := parseGitHubPayload(bodyBytes, endpoint, repoSlug, req.Target.Cursor, req.Now)
	if parseErr != nil {
		return CollectResult{
			Status:       RunStatusConnectorFailed,
			ETag:         req.Target.ETag,
			NextCursor:   req.Target.Cursor,
			ErrorMessage: parseErr.Error(),
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
		NextCursor: nextCursor,
		Records:    records,
	}
}

func resolveGitHubEndpoint(sourceURI, apiBase string) (string, string) {
	trimmed := strings.TrimSpace(sourceURI)
	base := strings.TrimRight(firstNonEmpty(strings.TrimSpace(apiBase), "https://api.github.com"), "/")
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		parsed, err := url.Parse(trimmed)
		if err == nil && strings.EqualFold(parsed.Hostname(), "github.com") {
			parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
			if len(parts) >= 2 {
				slug := parts[0] + "/" + parts[1]
				return base + "/repos/" + slug + "/releases", slug
			}
		}
		return trimmed, extractRepoSlugFromURL(trimmed)
	}
	slug := strings.Trim(trimmed, "/")
	return base + "/repos/" + slug + "/releases", slug
}

func extractRepoSlugFromURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] == "repos" {
			return parts[i+1] + "/" + parts[i+2]
		}
	}
	return strings.Trim(parsed.Path, "/")
}

func parseGitHubPayload(bodyBytes []byte, endpoint, repoSlug, prevCursor string, now time.Time) ([]CollectedRecord, string, error) {
	trimmed := strings.TrimSpace(string(bodyBytes))
	if trimmed == "" || trimmed == "[]" {
		return nil, prevCursor, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var releases []githubRelease
		if err := json.Unmarshal(bodyBytes, &releases); err == nil && len(releases) > 0 && (releases[0].TagName != "" || releases[0].ID != 0) {
			records := make([]CollectedRecord, 0, len(releases))
			nextCursor := prevCursor
			for i, rel := range releases {
				pubTime, precision := parseTimestampWithPrecision(firstNonEmpty(rel.PublishedAt, rel.CreatedAt), now)
				title := firstNonEmpty(strings.TrimSpace(rel.Name), strings.TrimSpace(rel.TagName))
				if repoSlug != "" && !strings.Contains(strings.ToLower(title), strings.ToLower(repoSlug)) {
					title = repoSlug + " " + title
				}
				extID := strconv.FormatInt(rel.ID, 10)
				if rel.ID == 0 {
					extID = rel.TagName
				}
				if i == 0 {
					nextCursor = extID
				}
				rawPayload, _ := json.Marshal(rel)
				records = append(records, CollectedRecord{
					ExternalID:    extID,
					SourceURI:     firstNonEmpty(rel.HTMLURL, endpoint),
					Title:         title,
					Summary:       strings.TrimSpace(rel.Body),
					Language:      "en",
					MimeType:      "application/json",
					RawExcerpt:    truncateText(firstNonEmpty(rel.Body, title), 2000),
					OccurredAt:    pubTime.UTC().Format(time.RFC3339Nano),
					PublishedAt:   pubTime.UTC().Format(time.RFC3339Nano),
					TimePrecision: precision,
					EntityName:    repoSlug,
					EntityType:    "repository",
					StrongID:      "github:" + strings.ToLower(repoSlug),
					PayloadJSON:   string(rawPayload),
				})
			}
			return records, nextCursor, nil
		}

		var commits []githubCommit
		if err := json.Unmarshal(bodyBytes, &commits); err == nil && len(commits) > 0 && commits[0].SHA != "" {
			records := make([]CollectedRecord, 0, len(commits))
			nextCursor := prevCursor
			for i, cm := range commits {
				pubTime, precision := parseTimestampWithPrecision(cm.Commit.Author.Date, now)
				firstLine := strings.Split(strings.TrimSpace(cm.Commit.Message), "\n")[0]
				if i == 0 {
					nextCursor = cm.SHA
				}
				rawPayload, _ := json.Marshal(cm)
				records = append(records, CollectedRecord{
					ExternalID:    cm.SHA,
					SourceURI:     firstNonEmpty(cm.HTMLURL, endpoint),
					Title:         firstLine,
					Summary:       strings.TrimSpace(cm.Commit.Message),
					Language:      "en",
					MimeType:      "application/json",
					RawExcerpt:    truncateText(cm.Commit.Message, 2000),
					OccurredAt:    pubTime.UTC().Format(time.RFC3339Nano),
					PublishedAt:   pubTime.UTC().Format(time.RFC3339Nano),
					TimePrecision: precision,
					EntityName:    repoSlug,
					EntityType:    "repository",
					StrongID:      "github:" + strings.ToLower(repoSlug),
					PayloadJSON:   string(rawPayload),
				})
			}
			return records, nextCursor, nil
		}
	}

	var repoMeta struct {
		FullName    string `json:"full_name"`
		HTMLURL     string `json:"html_url"`
		Description string `json:"description"`
		PushedAt    string `json:"pushed_at"`
		UpdatedAt   string `json:"updated_at"`
	}
	if err := json.Unmarshal(bodyBytes, &repoMeta); err == nil && repoMeta.FullName != "" {
		pubTime, precision := parseTimestampWithPrecision(firstNonEmpty(repoMeta.PushedAt, repoMeta.UpdatedAt), now)
		return []CollectedRecord{{
			ExternalID:    repoMeta.FullName + "@" + pubTime.UTC().Format(time.RFC3339),
			SourceURI:     firstNonEmpty(repoMeta.HTMLURL, endpoint),
			Title:         repoMeta.FullName + " repository update",
			Summary:       strings.TrimSpace(repoMeta.Description),
			Language:      "en",
			MimeType:      "application/json",
			RawExcerpt:    truncateText(firstNonEmpty(repoMeta.Description, repoMeta.FullName), 2000),
			OccurredAt:    pubTime.UTC().Format(time.RFC3339Nano),
			PublishedAt:   pubTime.UTC().Format(time.RFC3339Nano),
			TimePrecision: precision,
			EntityName:    repoMeta.FullName,
			EntityType:    "repository",
			StrongID:      "github:" + strings.ToLower(repoMeta.FullName),
			PayloadJSON:   trimmed,
		}}, pubTime.UTC().Format(time.RFC3339), nil
	}

	return nil, prevCursor, fmt.Errorf("unrecognized github JSON payload")
}

// WebFeedConnector collects structured JSON institutional/API feed items.
type WebFeedConnector struct{}

func (c *WebFeedConnector) ID() string { return "web_feed" }

type webFeedEntry struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	URL         string   `json:"url"`
	PublishedAt string   `json:"publishedAt"`
	Summary     string   `json:"summary"`
	OccurredAt  string   `json:"occurredAt"`
	Entity      string   `json:"entity"`
	EntityType  string   `json:"entityType"`
	StrongID    string   `json:"strongId"`
	Language    string   `json:"language"`
	Domains     []string `json:"domains"`
	Impact      float64  `json:"impact"`
	Urgency     float64  `json:"urgency"`
}

func (c *WebFeedConnector) Collect(ctx context.Context, req CollectRequest) CollectResult {
	statusCode, respHeaders, bodyBytes, err := performConditionalGET(ctx, req.HTTPClient, req.Target.SourceURI, req.Target.ETag, "application/json")
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
			ErrorMessage: "http 429 rate limited",
		}
	}
	if statusCode < 200 || statusCode >= 300 {
		return CollectResult{
			Status:       RunStatusConnectorFailed,
			ETag:         req.Target.ETag,
			NextCursor:   req.Target.Cursor,
			ErrorMessage: fmt.Sprintf("http status %d", statusCode),
		}
	}

	nextETag := firstNonEmpty(respHeaders.Get("ETag"), req.Target.ETag)
	entries, nextCursor, parseErr := parseWebFeedEntries(bodyBytes, req.Target.Cursor)
	if parseErr != nil {
		return CollectResult{
			Status:       RunStatusConnectorFailed,
			ETag:         req.Target.ETag,
			NextCursor:   req.Target.Cursor,
			ErrorMessage: parseErr.Error(),
		}
	}
	if len(entries) == 0 {
		return CollectResult{
			Status:     RunStatusNoNewData,
			ETag:       nextETag,
			NextCursor: req.Target.Cursor,
		}
	}

	records := make([]CollectedRecord, 0, len(entries))
	for _, entry := range entries {
		title := strings.TrimSpace(entry.Title)
		summary := strings.TrimSpace(entry.Summary)
		if title == "" && summary == "" {
			continue
		}
		pubTime, pubPrecision := parseTimestampWithPrecision(entry.PublishedAt, req.Now)
		occTime, occPrecision := parseTimestampWithPrecision(entry.OccurredAt, pubTime)
		if strings.TrimSpace(entry.OccurredAt) == "" {
			occPrecision = pubPrecision
		}
		rawPayload, _ := json.Marshal(entry)
		records = append(records, CollectedRecord{
			ExternalID:    firstNonEmpty(strings.TrimSpace(entry.ID), strings.TrimSpace(entry.URL), title),
			SourceURI:     firstNonEmpty(strings.TrimSpace(entry.URL), req.Target.SourceURI),
			Title:         title,
			Summary:       summary,
			Language:      firstNonEmpty(strings.TrimSpace(entry.Language), "en"),
			MimeType:      "application/json",
			RawExcerpt:    truncateText(firstNonEmpty(summary, title), 2000),
			OccurredAt:    occTime.UTC().Format(time.RFC3339Nano),
			PublishedAt:   pubTime.UTC().Format(time.RFC3339Nano),
			TimePrecision: occPrecision,
			EntityName:    strings.TrimSpace(entry.Entity),
			EntityType:    firstNonEmpty(strings.TrimSpace(entry.EntityType), "organization"),
			StrongID:      strings.TrimSpace(entry.StrongID),
			Domains:       extractDomainTags(entry.Domains),
			ImpactHint:    entry.Impact,
			UrgencyHint:   entry.Urgency,
			PayloadJSON:   string(rawPayload),
		})
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
		NextCursor: nextCursor,
		Records:    records,
	}
}

func parseWebFeedEntries(bodyBytes []byte, prevCursor string) ([]webFeedEntry, string, error) {
	trimmed := strings.TrimSpace(string(bodyBytes))
	if trimmed == "" {
		return nil, prevCursor, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var entries []webFeedEntry
		if err := json.Unmarshal(bodyBytes, &entries); err != nil {
			return nil, prevCursor, fmt.Errorf("decode web_feed array: %w", err)
		}
		return entries, prevCursor, nil
	}
	var envelope struct {
		Items      []webFeedEntry `json:"items"`
		NextCursor string         `json:"nextCursor"`
	}
	if err := json.Unmarshal(bodyBytes, &envelope); err != nil {
		return nil, prevCursor, fmt.Errorf("decode web_feed object: %w", err)
	}
	cursor := firstNonEmpty(strings.TrimSpace(envelope.NextCursor), prevCursor)
	return envelope.Items, cursor, nil
}

func performConditionalGET(ctx context.Context, client *http.Client, targetURL, etag, acceptHeader string) (int, http.Header, []byte, error) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("User-Agent", "mw-echo-worldradar/1.0")
	if acceptHeader != "" {
		httpReq.Header.Set("Accept", acceptHeader)
	}
	if strings.TrimSpace(etag) != "" {
		httpReq.Header.Set("If-None-Match", strings.TrimSpace(etag))
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("http get %s: %w", targetURL, err)
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, 4<<20)
	bodyBytes, readErr := io.ReadAll(limited)
	if readErr != nil {
		return resp.StatusCode, resp.Header, nil, fmt.Errorf("read response body: %w", readErr)
	}
	return resp.StatusCode, resp.Header, bodyBytes, nil
}

func parseRetryAfter(headers http.Header, now time.Time) time.Duration {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	raw := strings.TrimSpace(headers.Get("Retry-After"))
	if raw != "" {
		if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
		if retryTime, err := http.ParseTime(raw); err == nil {
			diff := retryTime.Sub(now)
			if diff > 0 {
				return diff
			}
		}
	}
	resetRaw := strings.TrimSpace(headers.Get("X-RateLimit-Reset"))
	if resetRaw != "" {
		if epochSecs, err := strconv.ParseInt(resetRaw, 10, 64); err == nil {
			diff := time.Unix(epochSecs, 0).UTC().Sub(now)
			if diff > 0 {
				return diff
			}
		}
	}
	return 60 * time.Second
}

func parseTimestampWithPrecision(raw string, fallback time.Time) (time.Time, string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		if fallback.IsZero() {
			fallback = time.Now().UTC()
		}
		return fallback.UTC(), "unknown"
	}
	layouts := []struct {
		layout    string
		precision string
	}{
		{time.RFC3339Nano, "second"},
		{time.RFC3339, "second"},
		{time.RFC1123Z, "second"},
		{time.RFC1123, "second"},
		{time.RFC822Z, "second"},
		{time.RFC822, "second"},
		{"2006-01-02 15:04:05", "second"},
		{"2006-01-02", "day"},
	}
	for _, candidate := range layouts {
		if parsed, err := time.Parse(candidate.layout, trimmed); err == nil {
			return parsed.UTC(), candidate.precision
		}
	}
	if fallback.IsZero() {
		fallback = time.Now().UTC()
	}
	return fallback.UTC(), "unknown"
}

func extractDomainTags(rawTags []string) []string {
	var valid []string
	seen := make(map[string]bool)
	for _, tag := range rawTags {
		norm := strings.ToUpper(strings.TrimSpace(tag))
		if _, ok := GetDomain(norm); ok && !seen[norm] {
			seen[norm] = true
			valid = append(valid, norm)
		}
	}
	return valid
}

func firstNonEmpty(values ...string) string {
	for _, val := range values {
		if strings.TrimSpace(val) != "" {
			return strings.TrimSpace(val)
		}
	}
	return ""
}

func truncateText(text string, maxLen int) string {
	trimmed := strings.TrimSpace(text)
	if maxLen <= 0 || len(trimmed) <= maxLen {
		return trimmed
	}
	return trimmed[:maxLen]
}
