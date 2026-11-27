# mw-echo — Single-binary local control hub for the MoonWitness workstation.

**Repository:** [`A-L-L-A-H/mw-echo`](https://github.com/A-L-L-A-H/mw-echo) · **Stack:** Go 1.27 · Echo · React/Vite · SQLite FTS5

## What it does

Combines projects and Kanban tasks, full-text docs, encrypted 9Drive storage, an AI gateway, OTLP observability, WhatsApp gateway, and a coding agent.

## Quick start

```bash
.\start-mw-echo.ps1
.\start-mw-echo.ps1 -Rebuild
```

## Verify

```bash
go build ./...
go test ./...
```

## Configuration

- **Port:** 4310
- **Key configuration:** MW_ECHO_ADDR · MW_ECHO_DATA_DIR · MW_ECHO_TOKEN

```bash
docker compose -f docker-compose.yml -f docker-compose.cloudflared.yml up -d
```

## Notes

Set MW_ECHO_MASTER_KEY explicitly in production; otherwise an encryption key is generated in the data directory. Windows startup registration: .\install-windows-startup.ps1.
