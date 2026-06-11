<#
.SYNOPSIS
  Install Docker Commander to run in the background on Windows.

.DESCRIPTION
  dockercmd is a plain console program, not a native Windows service (it doesn't
  implement the Service Control Manager protocol), so `sc.exe create` / New-Service
  would fail with error 1053 ("did not respond in time"). Instead we register a
  Scheduled Task that starts it at boot and restarts it on failure — no extra
  dependencies. For a "real" Windows service, wrap the exe with NSSM (https://nssm.cc)
  or WinSW; this script is the dependency-free option.

.PARAMETER BinPath
  Path to dockercmd.exe (default: .\dockercmd.exe, then .\dockercmd-windows-amd64.exe).

.PARAMETER AtLogon
  Run the task at your logon (recommended when Docker Desktop runs per-user) instead
  of at system boot as SYSTEM.

.EXAMPLE
  # From an elevated PowerShell:
  .\deploy\install-windows.ps1
  .\deploy\install-windows.ps1 -BinPath C:\tools\dockercmd.exe -AtLogon
#>
[CmdletBinding()]
param(
  [string]$BinPath,
  [switch]$AtLogon
)

$ErrorActionPreference = 'Stop'
$TaskName = 'DockerCommander'
$InstallDir = "$env:ProgramFiles\docker-commander"
$DataDir = "$env:ProgramData\docker-commander\data"

# --- must be elevated --------------------------------------------------------
$admin = ([Security.Principal.WindowsPrincipal] [Security.Principal.WindowsIdentity]::GetCurrent()
  ).IsInRole([Security.Principal.WindowsBuiltinRole]::Administrator)
if (-not $admin) { throw "Run this from an elevated (Administrator) PowerShell." }

# --- locate the binary -------------------------------------------------------
if (-not $BinPath) {
  foreach ($cand in '.\dockercmd.exe', '.\dockercmd-windows-amd64.exe', '.\dockercmd-windows-arm64.exe') {
    if (Test-Path $cand) { $BinPath = $cand; break }
  }
}
if (-not $BinPath -or -not (Test-Path $BinPath)) {
  throw "dockercmd.exe not found. Download a release or build it, then pass -BinPath."
}
Write-Host "==> Binary: $BinPath"

# --- install binary + data dir ----------------------------------------------
New-Item -ItemType Directory -Force -Path $InstallDir, $DataDir | Out-Null
$exe = Join-Path $InstallDir 'dockercmd.exe'
Copy-Item -Force $BinPath $exe
Write-Host "==> Installed $exe"

# --- register the scheduled task --------------------------------------------
$action   = New-ScheduledTaskAction -Execute $exe -Argument "-data-dir `"$DataDir`""
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries `
  -DontStopIfGoingOnBatteries -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) `
  -ExecutionTimeLimit ([TimeSpan]::Zero)

if ($AtLogon) {
  $trigger   = New-ScheduledTaskTrigger -AtLogOn
  $principal = New-ScheduledTaskPrincipal -UserId "$env:USERDOMAIN\$env:USERNAME" -RunLevel Highest
  Write-Host "==> Task runs at logon as $env:USERDOMAIN\$env:USERNAME"
} else {
  $trigger   = New-ScheduledTaskTrigger -AtStartup
  $principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
  Write-Host "==> Task runs at boot as SYSTEM"
}

Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger `
  -Principal $principal -Settings $settings -Force | Out-Null
Start-ScheduledTask -TaskName $TaskName

Start-Sleep -Seconds 2
$state = (Get-ScheduledTask -TaskName $TaskName).State
Write-Host ""
Write-Host "OK Done. Task '$TaskName' is $state."
Write-Host "   Listen address + TLS come from DC_HOST/DC_PORT/DC_TLS_* (default 127.0.0.1:8470);"
Write-Host "   create the admin account in the UI on first visit."
Write-Host "   Stop:    Stop-ScheduledTask -TaskName $TaskName"
Write-Host "   Remove:  Unregister-ScheduledTask -TaskName $TaskName -Confirm:`$false"
Write-Host ""
Write-Host "Note: if the UI never comes up, Docker Desktop's engine may only be"
Write-Host "reachable by your user — re-run with -AtLogon instead of boot/SYSTEM."
