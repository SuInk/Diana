# Copyright (c) 2025-now SuInk.
# Licensed under the Limited Redistribution License in the repository root.

#Requires -Version 5.1

param(
    [switch]$Purge,
    [switch]$Yes
)

$ErrorActionPreference = "Stop"
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$installDir = [IO.Path]::GetFullPath($scriptDir)
$localAppData = [IO.Path]::GetFullPath($env:LOCALAPPDATA)
if ($installDir -eq [IO.Path]::GetPathRoot($installDir) -or $installDir -eq $localAppData) {
    throw "Unsafe Diana install directory: $installDir"
}

if (-not $Yes) {
    $question = if ($Purge) {
        "Remove Diana and permanently delete all data in $installDir? Type YES to continue"
    } else {
        "Remove Diana but keep its configuration and data in $installDir? Type YES to continue"
    }
    if ((Read-Host $question) -cne "YES") {
        Write-Host "Cancelled."
        exit 0
    }
}

$pidFile = Join-Path $installDir ".diana.pid"
if (Test-Path $pidFile) {
    $dianaPID = 0
    if ([int]::TryParse((Get-Content $pidFile -Raw).Trim(), [ref]$dianaPID)) {
        $process = Get-CimInstance Win32_Process -Filter "ProcessId = $dianaPID" -ErrorAction SilentlyContinue
        if ($process -and $process.ExecutablePath -and ([IO.Path]::GetFullPath($process.ExecutablePath)).StartsWith($installDir, [StringComparison]::OrdinalIgnoreCase)) {
            Stop-Process -Id $dianaPID -Force -ErrorAction SilentlyContinue
        }
    }
}

$commandShim = Join-Path $env:LOCALAPPDATA "Microsoft\WindowsApps\diana.cmd"
if (Test-Path $commandShim) {
    $shimContent = Get-Content $commandShim -Raw -ErrorAction SilentlyContinue
    if ($shimContent -and $shimContent.IndexOf($installDir, [StringComparison]::OrdinalIgnoreCase) -ge 0) {
        Remove-Item -LiteralPath $commandShim -Force
    }
}

if ($Purge) {
    if (-not (Test-Path (Join-Path $installDir ".installed-version")) -and -not (Test-Path (Join-Path $installDir "config.yaml"))) {
        throw "Refusing to purge a directory without a Diana installation marker: $installDir"
    }
    $temporaryScript = Join-Path ([IO.Path]::GetTempPath()) ("diana-remove-" + [guid]::NewGuid().ToString("N") + ".ps1")
    @"
Start-Sleep -Milliseconds 500
Remove-Item -LiteralPath '$($installDir.Replace("'", "''"))' -Recurse -Force -ErrorAction SilentlyContinue
Remove-Item -LiteralPath `$MyInvocation.MyCommand.Path -Force -ErrorAction SilentlyContinue
"@ | Set-Content -Encoding UTF8 $temporaryScript
    Start-Process powershell.exe -WindowStyle Hidden -ArgumentList @("-NoProfile", "-ExecutionPolicy", "Bypass", "-File", $temporaryScript) | Out-Null
    Write-Host "Diana and its data are being removed from $installDir"
    exit 0
}

@(
    "diana-webui.exe",
    "diana-webui-windows-amd64.exe",
    "frontend-next",
    "run.bat",
    ".installed-version",
    ".diana.pid",
    ".diana-updates"
) | ForEach-Object {
    $path = Join-Path $installDir $_
    if (Test-Path $path) { Remove-Item -LiteralPath $path -Recurse -Force }
}

Write-Host "Diana was removed. Configuration and data remain in $installDir"
Write-Host "You may delete uninstall.ps1 after this window closes."
