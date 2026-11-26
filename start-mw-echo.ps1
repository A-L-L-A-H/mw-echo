param(
  [string]$Addr = "127.0.0.1:4310",
  [string]$DataDir = "$env:LOCALAPPDATA\mw-echo",
  [switch]$Rebuild,
  [switch]$NoBrowser
)

$ErrorActionPreference = "Stop"
$Exe = Join-Path $PSScriptRoot "mw-echo.exe"
$UiIndex = Join-Path $PSScriptRoot "internal\server\ui\dist\index.html"

if ($Rebuild -or -not (Test-Path $UiIndex)) {
  if (Get-Command bun -ErrorAction SilentlyContinue) {
    Write-Host "[mw-echo] Building React+Vite frontend..."
    Push-Location (Join-Path $PSScriptRoot "web")
    if (-not (Test-Path "node_modules")) { bun install }
    bun run build
    Pop-Location
  }
}

if ($Rebuild -or -not (Test-Path $Exe)) {
  Write-Host "[mw-echo] Building mw-echo.exe..."
  Push-Location $PSScriptRoot
  $env:CGO_ENABLED = "0"
  go build -ldflags="-s -w" -o $Exe ./cmd/mw-echo
  Pop-Location
}

if (-not $NoBrowser) {
  Start-Process "http://$Addr"
}
& $Exe --addr $Addr --data-dir $DataDir
