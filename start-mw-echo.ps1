param(
  [string]$Addr = "127.0.0.1:4310",
  [string]$DataDir = "$env:LOCALAPPDATA\mw-echo",
  [switch]$NoBrowser
)

$ErrorActionPreference = "Stop"
$Exe = Join-Path $PSScriptRoot "mw-echo.exe"
if (-not (Test-Path $Exe)) {
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
