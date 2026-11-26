# mw-echo

Single-binary **Go + Echo v4 + React Vite + SQLite FTS5** local control hub for Windows:
- **Projects & Kanban Tasks** (TaskOS v2 compatible, auto project discovery, milestone progress rollups, Kanban board + table view)
- **Docs Hub** (SQLite FTS5 full-text Markdown search — keep repos free of sprawling `docs/` trees)
- **9Drive Storage Pool** (Multi-account Google Drive + S3/R2/Garage with AES-256-GCM encrypted credentials, SQLite `VACUUM INTO` database backups via `POST /api/storage/backup`, & zero local disk waste)
- **Unified AI Gateway** (Bifrost OpenAI `/v1/chat/completions` & Anthropic `/v1/messages` router, 9Router RTK `tool_result` token compression + response cache + 429 cooldown failover, Magpie per-agent model switchboard, FreeLLMAPI upstream GitHub sniffer)
- **Host Observatory** (OTLP HTTP/JSON `/v1/traces`, `/v1/metrics`, `/v1/logs`, concurrent localhost protocol/port auto-discovery, Local Git Repo Radar)
- **GOWA WhatsApp** (Native Go `whatsmeow` multi-device gateway, drop-in GOWA REST routes, QR/pairing login, webhook dispatch, auto-reply via AI Gateway, & React Inbox)
- **Coding Agent (`mw-echo code`)** (Built-in Pi/OpenCode/Codex tool loop routed through the AI Gateway with session management, SSE events, forking, & compaction)
- **Gamification Arena** (XP, ranks, streaks, combo multipliers, daily quests, badges, & boss milestones fueled by real hub activity)
- **Shadow Sidekick** (Multi-persona AI assistant with daily briefs, WhatsApp send, coding-mission dispatch, & XP integration)
- **World Radar (`12 WORLD`)** (R!OTS World Radar Network across 23 domains `D01`–`D23`, analytical lenses, Alpha Radar Packs, watch targets, event clustering, & epistemic reports)
- **MCP JSON-RPC 2.0** (`POST /api/mcp`)

## Quick Start (Windows)

```powershell
# Launch React+Go dashboard + API + AI Gateway on http://127.0.0.1:4310
.\start-mw-echo.ps1

# Rebuild UI + binary before launching
.\start-mw-echo.ps1 -Rebuild

# Import docs + tasks.json + optional media from any repo
.\mw-echo.exe import --repo Z:\repo\mw-xv --project moonwitness --assets

# Run one-shot Coding Agent turn against a workspace
.\mw-echo.exe code --dir Z:\repo\mw-xv "Summarize failing tests"

# Register auto-start on Windows logon
.\install-windows-startup.ps1
```

## Docker & Cloudflare Tunnel

```powershell
# Run mw-echo + cloudflared tunnel sidecar (quick tunnel by default, or named tunnel when CLOUDFLARE_TUNNEL_TOKEN is set)
docker compose -f docker-compose.yml -f docker-compose.cloudflared.yml up -d
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
- 9Drive Pool & Storage: `GET/POST/DELETE /api/providers`, `PUT /api/providers/policy`, `POST /api/providers/:id/sync-quota`, `POST /api/providers/gdrive/oauth/start`, `GET /api/storage`, `POST /api/storage/upload`, `POST /api/storage/backup`, `GET /api/storage/:id/download`
- AI Gateway: `GET /v1/models`, `POST /v1/chat/completions`, `POST /v1/messages`, `GET/POST /api/ai/providers`, `GET/PUT /api/ai/agents`, `GET /api/ai/catalog`, `POST /api/ai/catalog/sniff`, `GET/PUT /api/ai/settings`, `GET /api/ai/stats`
- Observatory: `POST /v1/traces`, `POST /v1/metrics`, `POST /v1/logs`, `GET /api/observatory/events`, `GET /api/observatory/stats`, `GET /api/observatory/services`, `GET/POST /api/observatory/repos`
- GOWA WhatsApp: `GET /app/info`, `GET/POST/DELETE /devices`, `GET /app/login`, `POST /send/message`, `GET /chats`, `GET /api/whatsapp/stats`, `GET/POST /api/whatsapp/devices`, `GET /api/whatsapp/chats`
- Coding Agent: `GET/POST /api/agent/sessions`, `POST /api/agent/sessions/:id/prompt`, `POST /api/agent/sessions/:id/fork`, `POST /api/agent/sessions/:id/compact`, `GET /api/agent/sessions/:id/events`, `GET /api/agent/tools`
- Gamification Arena: `GET /api/game/profile`, `GET /api/game/feed`, `POST /api/game/quests/:id/claim`
- Shadow Sidekick: `GET /api/assistant/personas`, `GET/DELETE /api/assistant/history`, `POST /api/assistant/chat`, `POST /api/assistant/brief`
- World Radar (`12 WORLD`): `GET /api/radar/domains`, `GET /api/radar/lenses`, `GET /api/radar/packs`, `POST /api/radar/packs/:packId/install`, `GET/POST /api/radar/instances`, `POST /api/radar/instances/:id/run`, `GET /api/radar/events`, `GET /api/radar/alerts`, `GET/POST /api/radar/reports`, `GET /api/radar/sources/health`
- Repo Importer & MCP: `POST /api/import/repo`, `POST /api/mcp` (`echo_radar_events`, `echo_radar_run`, Docs, Tasks, Storage, Projects, WhatsApp)

## Env Vars

- `MW_ECHO_ADDR` (default `127.0.0.1:4310`)
- `MW_ECHO_DATA_DIR` (default `%LOCALAPPDATA%\mw-echo`)
- `MW_ECHO_TOKEN` (optional Bearer token for `/api/*`)
- `MW_ECHO_MASTER_KEY` (optional AES-256-GCM passphrase for encrypted credentials; auto-generated in `<dataDir>\.master.key` when unset)
