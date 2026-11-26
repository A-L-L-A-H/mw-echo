# mw-echo

Single-binary **Go + Echo v4 + React Vite + SQLite FTS5** local control hub for Windows:
- **Projects & Kanban Tasks** (TaskOS v2 compatible, auto project discovery, milestone progress rollups, Kanban board + table view)
- **Docs Hub** (SQLite FTS5 full-text Markdown search — keep repos free of sprawling `docs/` trees)
- **9Drive Storage Pool** (Multi-account Google Drive + S3/R2/Garage with AES-256-GCM encrypted credentials & zero local disk waste)
- **Unified AI Gateway** (Bifrost OpenAI `/v1/chat/completions` & Anthropic `/v1/messages` router, 9Router RTK `tool_result` token compression + response cache + 429 cooldown failover, Magpie per-agent model switchboard, FreeLLMAPI upstream GitHub sniffer)
- **Host Observatory** (OTLP HTTP/JSON `/v1/traces`, `/v1/metrics`, `/v1/logs`, concurrent localhost protocol/port auto-discovery, Local Git Repo Radar)
- **MCP JSON-RPC 2.0** (`POST /api/mcp`)

## Quick Start (Windows)

```powershell
# Launch React+Go dashboard + API + AI Gateway on http://127.0.0.1:4310
.\start-mw-echo.ps1

# Rebuild UI + binary before launching
.\start-mw-echo.ps1 -Rebuild

# Import docs + tasks.json + optional media from any repo
.\mw-echo.exe import --repo Z:\repo\mw-xv --project moonwitness --assets

# Register auto-start on Windows logon
.\install-windows-startup.ps1
```

## Agent & OTLP Integration

```powershell
# Route Claude Code through Magpie switchboard + 9Router RTK Token Saver
$env:ANTHROPIC_BASE_URL = "http://127.0.0.1:4310"

# Route OpenAI/Codex/OpenCode compatible tools through Bifrost gateway
$env:OPENAI_BASE_URL = "http://127.0.0.1:4310/v1"

# Send OTLP traces/metrics/logs from any agent to mw-echo Observatory
$env:OTEL_EXPORTER_OTLP_ENDPOINT = "http://127.0.0.1:4310"
```

## Key Endpoints

- UI SPA: `GET /`
- Health & Stats: `GET /health`, `GET /api/stats`
- Projects: `GET /api/projects`, `GET /api/projects/:slug`, `POST /api/projects`, `PUT /api/projects/:slug`, `DELETE /api/projects/:slug`
- Tasks (TaskOS v2): `GET /api/tasks`, `POST /api/tasks`, `PATCH /api/tasks/:code`, `DELETE /api/tasks/:code`, `GET /api/export/tasks.json`
- Docs (FTS5): `GET /api/docs?q=...`, `GET /api/docs/:id`, `PUT /api/docs`, `DELETE /api/docs/:id`
- 9Drive Pool & Storage: `GET/POST/DELETE /api/providers`, `PUT /api/providers/policy`, `POST /api/providers/:id/sync-quota`, `POST /api/providers/gdrive/oauth/start`, `GET /api/storage`, `POST /api/storage/upload`, `GET /api/storage/:id/download`
- AI Gateway: `GET /v1/models`, `POST /v1/chat/completions`, `POST /v1/messages`, `GET/POST /api/ai/providers`, `GET/PUT /api/ai/agents`, `GET /api/ai/catalog`, `POST /api/ai/catalog/sniff`, `GET/PUT /api/ai/settings`, `GET /api/ai/stats`
- Observatory: `POST /v1/traces`, `POST /v1/metrics`, `POST /v1/logs`, `GET /api/observatory/events`, `GET /api/observatory/stats`, `GET /api/observatory/services`, `GET/POST /api/observatory/repos`
- Repo Importer & MCP: `POST /api/import/repo`, `POST /api/mcp`

## Env Vars

- `MW_ECHO_ADDR` (default `127.0.0.1:4310`)
- `MW_ECHO_DATA_DIR` (default `%LOCALAPPDATA%\mw-echo`)
- `MW_ECHO_TOKEN` (optional Bearer token for `/api/*`)
- `MW_ECHO_MASTER_KEY` (optional AES-256-GCM passphrase for encrypted credentials; auto-generated in `<dataDir>\.master.key` when unset)
