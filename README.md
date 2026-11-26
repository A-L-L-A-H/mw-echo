# mw-echo

Lightweight single-binary **Go + Echo v4 + SQLite FTS5 + SHA-256 Storage** local hub for Windows (docs, canonical tasks, binary assets, and MCP JSON-RPC for AI agents).

## Quick Start (Windows)

```powershell
# Run dashboard + API on http://127.0.0.1:4310
.\start-mw-echo.ps1

# Import docs + tasks.json + optional media from any repo
.\mw-echo.exe import --repo Z:\repo\mw-xv --project moonwitness --assets

# Optional: register auto-start on Windows logon
.\install-windows-startup.ps1
```

## Endpoints

- Dashboard UI: `GET /`
- Health & Stats: `GET /health`, `GET /api/stats`
- Docs (SQLite FTS5): `GET /api/docs?q=...`, `GET /api/docs/:id`, `PUT /api/docs`, `DELETE /api/docs/:id`
- Tasks (TaskOS v2): `GET /api/tasks`, `POST /api/tasks`, `PATCH /api/tasks/:code`, `GET /api/export/tasks.json`
- Storage (9Drive Multi-Account Pool: Google Drive + S3/R2/Garage + Local fallback): `GET /api/providers`, `POST /api/providers`, `PUT /api/providers/policy` (`most_free|round_robin|priority`), `POST /api/providers/:id/sync-quota`, `POST /api/providers/gdrive/oauth/start`, `GET /api/storage`, `POST /api/storage/upload`, `GET /api/storage/:id/download`, `DELETE /api/storage/:id`
- Local Repo Importer: `POST /api/import/repo`
- Agent MCP (JSON-RPC 2.0): `POST /api/mcp` (`echo_search_docs`, `echo_get_doc`, `echo_list_tasks`, `echo_update_task`, `echo_list_storage`)

## Env Vars

- `MW_ECHO_ADDR` (default `127.0.0.1:4310`)
- `MW_ECHO_DATA_DIR` (default `%LOCALAPPDATA%\mw-echo`)
- `MW_ECHO_TOKEN` (optional Bearer token for `/api/*`)
- `MW_ECHO_MASTER_KEY` (optional AES-256-GCM master passphrase encrypting Google Drive / S3 credentials; auto-generated in `<dataDir>\.master.key` when unset)
