package blueprints_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/A-L-L-A-H/mw-echo/internal/blueprints"
	"github.com/A-L-L-A-H/mw-echo/internal/store"
	"github.com/labstack/echo/v4"
)

func openTestCatalog(t *testing.T, opts blueprints.Options) (*blueprints.Catalog, *store.HubStore) {
	t.Helper()
	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub store: %v", err)
	}
	t.Cleanup(func() { _ = hub.Close() })

	catalog, err := blueprints.NewCatalog(hub, opts)
	if err != nil {
		t.Fatalf("new catalog: %v", err)
	}
	return catalog, hub
}

func TestCuratedSeedAndFTS5Search(t *testing.T) {
	ctx := context.Background()
	catalog, _ := openTestCatalog(t, blueprints.Options{})

	all, err := catalog.Search(ctx, "", "", "", 100)
	if err != nil {
		t.Fatalf("search all: %v", err)
	}
	if len(all) != 12 {
		t.Fatalf("expected 12 curated blueprints, got %d", len(all))
	}

	expectedSlugs := []string{
		"postgres-16", "redis-7", "garage-s3", "minio-s3", "n8n",
		"uptime-kuma", "grafana", "prometheus", "qdrant", "ollama",
		"cloudflared", "mw-echo",
	}
	seenSlugs := make(map[string]bool, len(all))
	for _, blueprint := range all {
		seenSlugs[blueprint.Slug] = true
		if blueprint.Source != "curated" {
			t.Fatalf("expected curated source, got %s", blueprint.Source)
		}
		if strings.TrimSpace(blueprint.ComposeYAML) == "" {
			t.Fatalf("blueprint %s has empty compose YAML", blueprint.Slug)
		}
	}
	for _, slug := range expectedSlugs {
		if !seenSlugs[slug] {
			t.Fatalf("missing expected curated blueprint %q", slug)
		}
	}

	for _, query := range []string{"postgres", "garage", "mw-echo"} {
		matches, searchErr := catalog.Search(ctx, query, "", "", 20)
		if searchErr != nil {
			t.Fatalf("search %q: %v", query, searchErr)
		}
		if len(matches) == 0 {
			t.Fatalf("expected FTS5 search for %q to return at least 1 match", query)
		}
		if !strings.Contains(matches[0].Slug, strings.Split(query, "-")[0]) {
			t.Fatalf("expected top match for %q to relate to query, got %q", query, matches[0].Slug)
		}
	}

	mwEcho, err := catalog.Get(ctx, "mw-echo")
	if err != nil {
		t.Fatalf("get mw-echo: %v", err)
	}
	if !strings.Contains(mwEcho.ComposeYAML, "ghcr.io/a-l-l-a-h/mw-echo:latest") {
		t.Fatalf("expected mw-echo compose to use ghcr.io/a-l-l-a-h/mw-echo:latest, got:\n%s", mwEcho.ComposeYAML)
	}
}

func TestGetComposeAndHTTPRoutes(t *testing.T) {
	ctx := context.Background()
	catalog, _ := openTestCatalog(t, blueprints.Options{})

	composeText, filename, err := catalog.GetCompose(ctx, "garage-s3")
	if err != nil {
		t.Fatalf("get compose: %v", err)
	}
	if filename != "garage-s3-docker-compose.yml" {
		t.Fatalf("unexpected filename %q", filename)
	}
	if !strings.Contains(composeText, "dxflrs/garage") {
		t.Fatalf("expected garage image in compose YAML, got:\n%s", composeText)
	}

	app := echo.New()
	api := app.Group("/api")
	catalog.RegisterRoutes(api)

	listReq := httptest.NewRequest(http.MethodGet, "/api/blueprints?q=postgres&source=curated&limit=10", nil)
	listRec := httptest.NewRecorder()
	app.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on list, got %d: %s", listRec.Code, listRec.Body.String())
	}
	var listPayload struct {
		Blueprints []blueprints.Blueprint    `json:"blueprints"`
		Count      int                       `json:"count"`
		Sources    []blueprints.SourceStatus `json:"sources"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listPayload); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if listPayload.Count != 1 || len(listPayload.Blueprints) != 1 || listPayload.Blueprints[0].Slug != "postgres-16" {
		t.Fatalf("unexpected list payload: %+v", listPayload)
	}
	if len(listPayload.Sources) == 0 {
		t.Fatal("expected sources metadata in list response")
	}

	detailReq := httptest.NewRequest(http.MethodGet, "/api/blueprints/curated:mw-echo", nil)
	detailRec := httptest.NewRecorder()
	app.ServeHTTP(detailRec, detailReq)
	if detailRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on detail, got %d", detailRec.Code)
	}
	var detailBlueprint blueprints.Blueprint
	if err := json.Unmarshal(detailRec.Body.Bytes(), &detailBlueprint); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detailBlueprint.Slug != "mw-echo" || !strings.Contains(detailBlueprint.ComposeYAML,
		"ghcr.io/a-l-l-a-h/mw-echo:latest") {
		t.Fatalf("unexpected detail blueprint: %+v", detailBlueprint)
	}

	composeReq := httptest.NewRequest(http.MethodGet, "/api/blueprints/curated:postgres-16/compose", nil)
	composeRec := httptest.NewRecorder()
	app.ServeHTTP(composeRec, composeReq)
	if composeRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on compose, got %d", composeRec.Code)
	}
	if !strings.HasPrefix(composeRec.Header().Get(echo.HeaderContentType), "text/yaml") {
		t.Fatalf("expected text/yaml content-type, got %q", composeRec.Header().Get(echo.HeaderContentType))
	}
	if !strings.Contains(composeRec.Body.String(), "postgres:16-alpine") {
		t.Fatalf("unexpected compose body:\n%s", composeRec.Body.String())
	}

	missingReq := httptest.NewRequest(http.MethodGet, "/api/blueprints/does-not-exist", nil)
	missingRec := httptest.NewRecorder()
	app.ServeHTTP(missingRec, missingReq)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown blueprint, got %d", missingRec.Code)
	}
}

func TestSnifferOfflineMockSHATrackingAndPartialErrors(t *testing.T) {
	ctx := context.Background()

	var phase atomic.Int32
	phase.Store(1)

	coolifyCompose := base64.StdEncoding.EncodeToString([]byte("services:\n  plausible:\n    image: ghcr.io/plausible/community-edition:v2\n"))
	coolifyExtraCompose := base64.StdEncoding.EncodeToString([]byte("services:\n  pocketbase:\n    image: ghcr.io/muchobien/pocketbase:latest\n"))

	mockGitHub := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		currentPhase := phase.Load()
		reqPath := request.URL.Path

		switch {
		case reqPath == "/api/repos/Dokploy/templates/commits":
			if currentPhase == 3 {
				writer.WriteHeader(http.StatusTooManyRequests)
				_, _ = writer.Write([]byte(`{"message":"API rate limit exceeded"}`))
				return
			}
			sha := "dokploy-sha-1"
			if currentPhase >= 2 {
				sha = "dokploy-sha-2"
			}
			_, _ = writer.Write([]byte(`[{"sha":"` + sha + `"}]`))

		case strings.HasPrefix(reqPath, "/api/repos/Dokploy/templates/git/trees/"):
			_, _ = writer.Write([]byte(`{
				"sha": "tree-dokploy",
				"tree": [
					{"path": "meta.json", "type": "blob", "sha": "m1"},
					{"path": "blueprints/ghost/docker-compose.yml", "type": "blob", "sha": "g1"}
				]
			}`))

		case strings.HasPrefix(reqPath, "/raw/Dokploy/templates/") && strings.HasSuffix(reqPath, "/meta.json"):
			_, _ = writer.Write([]byte(`[
				{"id":"ghost","name":"Ghost CMS","version":"5","description":"Modern publishing platform","tags":["cms","blog"]}
			]`))

		case strings.HasPrefix(reqPath, "/raw/Dokploy/templates/") && strings.HasSuffix(reqPath,
			"/blueprints/ghost/docker-compose.yml"):
			_, _ = writer.Write([]byte("services:\n  ghost:\n    image: ghost:5-alpine\n"))

		case reqPath == "/api/repos/coollabsio/coolify/commits":
			sha := "coolify-sha-1"
			if currentPhase >= 2 {
				sha = "coolify-sha-2"
			}
			_, _ = writer.Write([]byte(`[{"sha":"` + sha + `"}]`))

		case strings.HasPrefix(reqPath, "/raw/coollabsio/coolify/") && strings.HasSuffix(reqPath,
			"/templates/service-templates-latest.json"):
			if strings.Contains(reqPath, "coolify-sha-2") {
				_, _ = writer.Write([]byte(`{
					"plausible": {"name":"Plausible Analytics","slogan":"Privacy-friendly web analytics","compose":"` + coolifyCompose + `","tags":["analytics"],"category":"analytics"},
					"pocketbase": {"name":"PocketBase","slogan":"Open Source backend in 1 file","compose":"` + coolifyExtraCompose + `","tags":["baas","sqlite"],"category":"database"}
				}`))
				return
			}
			_, _ = writer.Write([]byte(`{
				"plausible": {"name":"Plausible Analytics","slogan":"Privacy-friendly web analytics","compose":"` + coolifyCompose + `","tags":["analytics"],"category":"analytics"}
			}`))

		case reqPath == "/api/repos/bigbeartechworld/big-bear-casaos/commits":
			_, _ = writer.Write([]byte(`[{"sha":"bigbear-sha-1"}]`))

		case strings.HasPrefix(reqPath, "/api/repos/bigbeartechworld/big-bear-casaos/git/trees/"):
			_, _ = writer.Write([]byte(`{
				"sha": "tree-bigbear",
				"tree": [
					{"path": "apps-list.json", "type": "blob", "sha": "b1"},
					{"path": "Apps/immich/docker-compose.yml", "type": "blob", "sha": "i1"}
				]
			}`))

		case strings.HasPrefix(reqPath, "/raw/bigbeartechworld/big-bear-casaos/") && strings.HasSuffix(reqPath,
			"/apps-list.json"):
			_, _ = writer.Write([]byte(`[
				{"slug":"immich","name":"Immich","tagline":"Self-hosted photo and video backup","category":"media","tags":["photos","backup"]}
			]`))

		case strings.HasPrefix(reqPath, "/raw/bigbeartechworld/big-bear-casaos/") && strings.HasSuffix(reqPath,
			"/Apps/immich/docker-compose.yml"):
			_, _ = writer.Write([]byte("services:\n  immich-server:\n    image: ghcr.io/immich-app/immich-server:release\n"))

		default:
			http.NotFound(writer, request)
		}
	}))
	defer mockGitHub.Close()

	catalog, _ := openTestCatalog(t, blueprints.Options{
		HTTPClient:    mockGitHub.Client(),
		GitHubAPIBase: mockGitHub.URL + "/api",
		RawBase:       mockGitHub.URL + "/raw",
	})

	// First sniff indexes Dokploy (ghost), Coolify (plausible), BigBear (immich).
	firstResults, err := catalog.SniffAll(ctx)
	if err != nil {
		t.Fatalf("first sniff: %v", err)
	}
	if len(firstResults) != 3 {
		t.Fatalf("expected 3 sniff results, got %d", len(firstResults))
	}
	for _, result := range firstResults {
		if result.Status != "ok" || result.Upserted != 1 {
			t.Fatalf("expected ok with 1 upserted for %s, got %+v", result.Source, result)
		}
	}

	// Verify FTS5 indexes newly sniffed blueprints.
	plausibleMatches, err := catalog.Search(ctx, "plausible", "coolify", "", 10)
	if err != nil || len(plausibleMatches) != 1 {
		t.Fatalf("expected 1 coolify plausible match, got %d (err=%v)", len(plausibleMatches), err)
	}
	if !strings.Contains(plausibleMatches[0].ComposeYAML, "ghcr.io/plausible/community-edition:v2") {
		t.Fatalf("expected decoded base64 compose YAML, got %q", plausibleMatches[0].ComposeYAML)
	}

	immichMatches, err := catalog.Search(ctx, "immich", "bigbear", "", 10)
	if err != nil || len(immichMatches) != 1 {
		t.Fatalf("expected 1 bigbear immich match, got %d (err=%v)", len(immichMatches), err)
	}

	// Second sniff at same SHAs should skip all 3 sources.
	unchangedResults, err := catalog.SniffAll(ctx)
	if err != nil {
		t.Fatalf("unchanged sniff: %v", err)
	}
	for _, result := range unchangedResults {
		if !result.Skipped || result.Status != "unchanged" {
			t.Fatalf("expected unchanged/skipped for %s, got %+v", result.Source, result)
		}
	}

	// Advance Coolify SHA to sha-2 and simulate Dokploy rate-limit 429 in phase 3.
	phase.Store(3)

	app := echo.New()
	catalog.RegisterRoutes(app.Group("/api"))
	sniffReq := httptest.NewRequest(http.MethodPost, "/api/blueprints/sniff", nil)
	sniffRec := httptest.NewRecorder()
	app.ServeHTTP(sniffRec, sniffReq)
	if sniffRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on POST /api/blueprints/sniff, got %d: %s", sniffRec.Code, sniffRec.Body.String())
	}

	var sniffPayload struct {
		Results         []blueprints.SniffResult `json:"results"`
		TotalBlueprints int                      `json:"totalBlueprints"`
	}
	if err := json.Unmarshal(sniffRec.Body.Bytes(), &sniffPayload); err != nil {
		t.Fatalf("decode sniff payload: %v", err)
	}
	if sniffPayload.TotalBlueprints != 16 {
		t.Fatalf("expected 16 total blueprints (12 curated + ghost + plausible + pocketbase + immich), got %d",
			sniffPayload.TotalBlueprints)
	}

	statusBySource := make(map[string]blueprints.SniffResult)
	for _, result := range sniffPayload.Results {
		statusBySource[result.Source] = result
	}
	if statusBySource["dokploy"].Status != "error" {
		t.Fatalf("expected dokploy rate-limit error status, got %+v", statusBySource["dokploy"])
	}
	if statusBySource["coolify"].Status != "ok" || statusBySource["coolify"].Upserted != 2 {
		t.Fatalf("expected coolify sha-2 to upsert 2 templates, got %+v", statusBySource["coolify"])
	}
	if statusBySource["bigbear"].Status != "unchanged" {
		t.Fatalf("expected bigbear unchanged, got %+v", statusBySource["bigbear"])
	}

	pocketbaseMatches, err := catalog.Search(ctx, "pocketbase", "", "", 10)
	if err != nil || len(pocketbaseMatches) != 1 {
		t.Fatalf("expected pocketbase indexed after coolify sha bump, got %d (err=%v)", len(pocketbaseMatches), err)
	}
}
