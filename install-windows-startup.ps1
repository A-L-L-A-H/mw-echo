param(
  [string]$Addr = "127.0.0.1:4310",
  [string]$DataDir = "$env:LOCALAPPDATA\mw-echo"
)

$ErrorActionPreference = "Stop"
$Exe = Join-Path $PSScriptRoot "mw-echo.exe"
if (-not (Test-Path $Exe)) {
  Push-Location $PSScriptRoot
  $env:CGO_ENABLED = "0"
  go build -ldflags="-s -w" -o $Exe ./cmd/mw-echo
  Pop-Location
}

$TaskName = "MoonWitness-MW-Echo"
$Action = New-ScheduledTaskAction -Execute $Exe -Argument "--addr $Addr --data-dir `"$DataDir`"" -WorkingDirectory $PSScriptRoot
$Trigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
$Settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)

Register-ScheduledTask -TaskName $TaskName -Action $Action -Trigger $Trigger -Settings $Settings -Force | Out-Null
Start-ScheduledTask -TaskName $TaskName
Write-Host "[mw-echo] Installed & started Windows logon task '$TaskName' -> http://$Addr"
