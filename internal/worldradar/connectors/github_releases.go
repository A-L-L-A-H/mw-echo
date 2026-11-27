package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// GitHubReleasesConnector implements the Connector contract for GitHub Releases REST API (Section 3.2.2).
// Cursor format: "<release_id>|<published_at_rfc3339>"
type GitHubReleasesConnector struct {
	SourceID           string
	Owner              string
	Repo               string
	Token              string // PAT or GitHub App token (never persisted in observation/provenance)
	APIBase            string // defaults to https://api.github.com
	LicenseClass       string
	IncludePrereleases bool
	IncludeDrafts      bool
	HTTPClient         *http.Client
	Checkpoint         Checkpoint
}

type ghReleaseAsset struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	ContentType        string `json:"content_type"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type ghRelease struct {
	ID          int64            `json:"id"`
	TagName     string           `json:"tag_name"`
	Name        string           `json:"name"`
	HTMLURL     string           `json:"html_url"`
	Body        string           `json:"body"`
	Draft       bool             `json:"draft"`
	Prerelease  bool             `json:"prerelease"`
	CreatedAt   string           `json:"created_at"`
	PublishedAt string           `json:"published_at"`
	Assets      []ghReleaseAsset `json:"assets"`
}

// Type returns ConnectorGitHubReleases ("github_releases").
func (c *GitHubReleasesConnector) Type() ConnectorType { return ConnectorGitHubReleases }

// Discover paginates /repos/{owner}/{repo}/releases and returns releases newer than checkpoint.
func (c *GitHubReleasesConnector) Discover(ctx context.Context, checkpoint Checkpoint) ([]DiscoveredItem, Checkpoint,
	error) {
	if strings.TrimSpace(c.Owner) == "" || strings.TrimSpace(c.Repo) == "" {
		return nil, checkpoint, fmt.Errorf("github owner and repo are required")
	}
	base := strings.TrimRight(firstNonEmpty(c.APIBase, "https://api.github.com"), "/")
	prevID, prevPub := parseGitHubCursor(checkpoint.Cursor)

	var collected []ghRelease
	for page := 1; page <= 10; page++ {
		endpoint := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=100&page=%d", base, c.Owner, c.Repo, page)
		headers := map[string]string{
			"Accept": "application/vnd.github+json",
		}
		if strings.TrimSpace(c.Token) != "" {
			headers["Authorization"] = "Bearer " + strings.TrimSpace(c.Token)
		}

		statusCode, respHeaders, body, err := doHTTPGet(ctx, c.HTTPClient, endpoint, headers)
		if err != nil {
			return nil, checkpoint, err
		}
		if statusCode == http.StatusTooManyRequests || (statusCode == http.StatusForbidden &&
			strings.TrimSpace(respHeaders.Get("X-RateLimit-Remaining")) == "0") {
			return nil, checkpoint, fmt.Errorf("github rate limited (http %d)", statusCode)
		}
		if statusCode < 200 || statusCode >= 300 {
			return nil, checkpoint, fmt.Errorf("github http status %d", statusCode)
		}

		var pageReleases []ghRelease
		if err := json.Unmarshal(body, &pageReleases); err != nil {
			return nil, checkpoint, fmt.Errorf("decode github releases: %w", err)
		}
		if len(pageReleases) == 0 {
			break
		}
		collected = append(collected, pageReleases...)
		if !hasNextLink(respHeaders.Get("Link")) {
			break
		}
	}

	items := make([]DiscoveredItem, 0, len(collected))
	for _, rel := range collected {
		if rel.Draft && !c.IncludeDrafts {
			continue
		}
		if rel.Prerelease && !c.IncludePrereleases {
			continue
		}
		pubAt := parseFlexibleTime(firstNonEmpty(rel.PublishedAt, rel.CreatedAt), time.Now().UTC())
		title := firstNonEmpty(rel.Name, rel.TagName)
		relIDStr := strconv.FormatInt(rel.ID, 10)
		rawJSON, _ := json.Marshal(rel)
		assetsJSON, _ := json.Marshal(rel.Assets)
		fp := HashSHA256Hex([]byte(relIDStr + "|" + pubAt.UTC().Format(time.RFC3339Nano) + "|" + rel.TagName))

		items = append(items, DiscoveredItem{
			Locator: firstNonEmpty(rel.HTMLURL, fmt.Sprintf("https://github.com/%s/%s/releases/tag/%s", c.Owner, c.Repo,
				rel.TagName)),
			Title:       title,
			PublishedAt: pubAt,
			Metadata: map[string]string{
				"release_id": relIDStr,
				"tag_name":   rel.TagName,
				"body":       rel.Body,
				"prerelease": strconv.FormatBool(rel.Prerelease),
				"draft":      strconv.FormatBool(rel.Draft),
				"assets":     string(assetsJSON),
				"repo":       c.Owner + "/" + c.Repo,
				"raw_json":   string(rawJSON),
			},
			Fingerprint: fp,
		})
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].PublishedAt.Equal(items[j].PublishedAt) {
			return items[i].Metadata["release_id"] < items[j].Metadata["release_id"]
		}
		return items[i].PublishedAt.Before(items[j].PublishedAt)
	})

	filtered := make([]DiscoveredItem, 0, len(items))
	latestID := prevID
	latestPub := prevPub
	for _, it := range items {
		rid := it.Metadata["release_id"]
		if !prevPub.IsZero() {
			if it.PublishedAt.Before(prevPub) {
				continue
			}
			if it.PublishedAt.Equal(prevPub) && prevID != "" && rid <= prevID {
				continue
			}
		} else if prevID != "" && rid == prevID {
			continue
		}
		filtered = append(filtered, it)
		if it.PublishedAt.After(latestPub) || (it.PublishedAt.Equal(latestPub) && rid > latestID) {
			latestPub = it.PublishedAt
			latestID = rid
		}
	}

	nextCP := checkpoint
	nextCP.ConnectorType = ConnectorGitHubReleases
	if nextCP.SourceID == "" {
		nextCP.SourceID = c.SourceID
	}
	if len(filtered) > 0 {
		nextCP.Cursor = latestID + "|" + latestPub.UTC().Format(time.RFC3339Nano)
		nextCP.LastSeenAt = latestPub.UTC()
		nextCP.ItemCount = checkpoint.ItemCount + int64(len(filtered))
		nextCP.Hash = nextCP.ComputeHash()
	}
	c.Checkpoint = nextCP
	return filtered, nextCP, nil
}

// Acquire materializes an Observation from a discovered GitHub release.
func (c *GitHubReleasesConnector) Acquire(_ context.Context, item DiscoveredItem) (Observation, error) {
	rawPayload := []byte(item.Metadata["raw_json"])
	if len(rawPayload) == 0 {
		rawPayload, _ = json.Marshal(item)
	}
	rawHash := HashSHA256Hex(rawPayload)
	title := item.Title
	body := item.Metadata["body"]
	contentHash := HashSHA256Hex([]byte(NormalizeNFKC(title) + "\x00" + NormalizeNFKC(body)))

	var assets []ghReleaseAsset
	_ = json.Unmarshal([]byte(item.Metadata["assets"]), &assets)

	return Observation{
		ObservationID: DeterministicObservationID(c.SourceID, "github-v3", item.Locator, contentHash),
		SourceID:      c.SourceID,
		SourceVersion: "github-v3",
		ConnectorType: ConnectorGitHubReleases,
		CanonicalURL:  item.Locator,
		Title:         title,
		Summary:       body,
		Body:          body,
		Language:      "en",
		PayloadType:   "structured_record",
		LicenseClass:  firstNonEmpty(c.LicenseClass, "redistribute_allowed"),
		PublishedAt:   item.PublishedAt.UTC(),
		ObservedAt:    item.PublishedAt.UTC(),
		CapturedAt:    time.Now().UTC(),
		RawPayload:    rawPayload,
		RawHash:       rawHash,
		ContentHash:   contentHash,
		Entities:      []string{firstNonEmpty(item.Metadata["repo"], c.Owner+"/"+c.Repo)},
		Metadata: map[string]any{
			"release_id": item.Metadata["release_id"],
			"tag_name":   item.Metadata["tag_name"],
			"prerelease": item.Metadata["prerelease"] == "true",
			"draft":      item.Metadata["draft"] == "true",
			"assets":     assets,
			"repo":       item.Metadata["repo"],
		},
		Tags: []string{"D04", "L14"},
	}, nil
}

// Validate checks required GitHub Release fields.
func (c *GitHubReleasesConnector) Validate(obs Observation) ValidationResult {
	var reasons []string
	if strings.TrimSpace(obs.Title) == "" {
		reasons = append(reasons, "missing_release_title")
	}
	if strings.TrimSpace(obs.CanonicalURL) == "" {
		reasons = append(reasons, "missing_canonical_url")
	}
	if len(reasons) > 0 {
		return ValidationResult{
			Valid:           false,
			ValidationState: "quarantined",
			ReasonCodes:     reasons,
		}
	}
	return ValidationResult{Valid: true, ValidationState: "accepted"}
}

// Normalize canonicalizes the GitHub Release observation.
func (c *GitHubReleasesConnector) Normalize(obs Observation) (NormalizedObservation, error) {
	return StandardNormalize(obs, []string{"D04"})
}

func parseGitHubCursor(cursor string) (string, time.Time) {
	if strings.TrimSpace(cursor) == "" {
		return "", time.Time{}
	}
	parts := strings.SplitN(cursor, "|", 2)
	id := strings.TrimSpace(parts[0])
	if len(parts) < 2 || strings.TrimSpace(parts[1]) == "" {
		return id, time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(parts[1]))
	if err != nil {
		t, err = time.Parse(time.RFC3339, strings.TrimSpace(parts[1]))
		if err != nil {
			return id, time.Time{}
		}
	}
	return id, t.UTC()
}

func hasNextLink(linkHeader string) bool {
	return strings.Contains(strings.ToLower(linkHeader), `rel="next"`)
}
