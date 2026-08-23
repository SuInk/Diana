# Copyright (c) 2025-now SuInk.
# Licensed under the Limited Redistribution License in the repository root.

#Requires -Version 5.1

$ErrorActionPreference = "Stop"

$repository = if ($env:DIANA_REPOSITORY) { $env:DIANA_REPOSITORY } else { "SuInk/Diana" }
$installDir = if ($env:DIANA_INSTALL_DIR) { $env:DIANA_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "Diana" }
$version = if ($env:DIANA_VERSION) { $env:DIANA_VERSION } else { "latest" }
$port = if ($env:DIANA_PORT) { [int]$env:DIANA_PORT } else { 18080 }
# 默认只绑回环:WebUI 是带管理权限的控制台,装完就对外敞开不是合理默认。
# 要从别的机器访问就显式设 DIANA_HOST=0.0.0.0(或某张网卡的地址)。
$hostExplicit = [bool]$env:DIANA_HOST
$bindHost = if ($env:DIANA_HOST) { $env:DIANA_HOST } else { "127.0.0.1" }
# DIANA_ENV_FILE 指向额外的 KEY=VALUE 文件,内容原样并进 runtime.env。
$extraEnvFile = $env:DIANA_ENV_FILE
if ($extraEnvFile -and -not (Test-Path $extraEnvFile)) {
    throw "DIANA_ENV_FILE does not exist: $extraEnvFile"
}
# 这些配置在装的时候填好比装完再进 WebUI 改一遍省事,尤其是无人值守部署。
$optionalEnvKeys = @(
    "LLM_API_KEY", "LLM_BASE_URL", "LLM_MODEL", "LLM_API_FORMAT", "LLM_IMAGE_MODEL",
    "DIANA_PUBLIC_BASE_URL", "DIANA_LOCAL_MEDIA_BASE_URL",
    "DIANA_NAPCAT_WEBUI_URL", "DIANA_NAPCAT_WEBUI_TOKEN"
)

# 绑定地址不是回环时,健康检查要打到真正在听的地址;0.0.0.0 是通配符,
# 本机仍从回环探测。
# 注意 elseif/else 必须跟在右花括号同一行:换行写会让 PowerShell 把 if 当成
# 已结束的语句,后面的 elseif 直接语法错误。
$healthHost = if ($bindHost -in @("", "0.0.0.0", "::", "*")) { "127.0.0.1" } elseif ($bindHost -like "*:*") { "[$bindHost]" } else { $bindHost }

function Get-DianaOptionalEnvLines {
    $lines = @()
    foreach ($key in $optionalEnvKeys) {
        $value = [Environment]::GetEnvironmentVariable($key)
        if ($value) { $lines += "$key=$value" }
    }
    if ($extraEnvFile) {
        # 原样并入:文件由部署者自己写,只跳过空行和注释。
        $lines += Get-Content $extraEnvFile | Where-Object { $_.Trim() -and -not $_.TrimStart().StartsWith("#") }
    }
    return $lines
}
$startAfterInstall = $env:DIANA_START_AFTER_INSTALL -ne "false"

function Get-DianaDownload {
    param([string]$Uri, [string]$OutFile, [string]$Label)
    Write-Host "==> Download -> $Label"
    $client = [Net.Http.HttpClient]::new()
    try {
        $response = $client.GetAsync($Uri, [Net.Http.HttpCompletionOption]::ResponseHeadersRead).GetAwaiter().GetResult()
        $response.EnsureSuccessStatusCode()
        $total = $response.Content.Headers.ContentLength
        $input = $response.Content.ReadAsStreamAsync().GetAwaiter().GetResult()
        $output = [IO.File]::Create($OutFile)
        try {
            $buffer = New-Object byte[] 1048576
            [long]$received = 0
            while (($read = $input.Read($buffer, 0, $buffer.Length)) -gt 0) {
                $output.Write($buffer, 0, $read)
                $received += $read
                $percent = if ($total -gt 0) { [Math]::Min(100, [int](100 * $received / $total)) } else { 0 }
                Write-Progress -Activity "Diana installer" -Status "$Label -> $percent%" -PercentComplete $percent
            }
        } finally { $output.Dispose(); $input.Dispose() }
        Write-Progress -Activity "Diana installer" -Completed
    } finally { $client.Dispose() }
}

function New-DianaRandomHex {
    param([int]$ByteCount)
    $bytes = New-Object byte[] $ByteCount
    $generator = [Security.Cryptography.RandomNumberGenerator]::Create()
    try { $generator.GetBytes($bytes) } finally { $generator.Dispose() }
    return ([BitConverter]::ToString($bytes)).Replace("-", "").ToLowerInvariant()
}

if (-not [Environment]::Is64BitOperatingSystem) {
    throw "Diana Releases currently require 64-bit Windows."
}

if ($version -eq "latest") {
    Write-Host "==> Resolving the latest stable release"
    $release = Invoke-RestMethod -Headers @{ "User-Agent" = "Diana-Installer" } -Uri "https://api.github.com/repos/$repository/releases/latest"
    $version = $release.tag_name
}

if ($version -notmatch '^v\d') {
    throw "Invalid release version: $version"
}

$packageName = "diana-webui-windows-amd64"
$binaryName = "diana-webui.exe"
$archiveName = "$packageName.zip"
$baseUrl = "https://github.com/$repository/releases/download/$version"
$tempDir = Join-Path ([IO.Path]::GetTempPath()) ("diana-install-" + [guid]::NewGuid().ToString("N"))
$archivePath = Join-Path $tempDir $archiveName
$sumsPath = Join-Path $tempDir "SHA256SUMS"
$stageDir = Join-Path $tempDir "stage"

try {
    New-Item -ItemType Directory -Force -Path $tempDir, $stageDir | Out-Null
    Get-DianaDownload "$baseUrl/$archiveName" $archivePath "Diana $version for windows/amd64"
    Get-DianaDownload "$baseUrl/SHA256SUMS" $sumsPath "SHA256SUMS"

    $checksumLine = Get-Content $sumsPath | Where-Object {
        $parts = $_ -split '\s+', 2
        $parts.Count -eq 2 -and $parts[1].TrimStart('*') -eq $archiveName
    } | Select-Object -First 1
    if (-not $checksumLine) { throw "SHA-256 entry for $archiveName was not found." }
    $expected = ($checksumLine -split '\s+')[0].ToLowerInvariant()
    $actual = (Get-FileHash -Algorithm SHA256 -Path $archivePath).Hash.ToLowerInvariant()
    if ($actual -ne $expected) { throw "SHA-256 verification failed for $archiveName." }
    Write-Host "==> SHA-256 verified"

    Expand-Archive -Path $archivePath -DestinationPath $stageDir -Force
    $packageDir = Join-Path $stageDir $packageName
    if (-not (Test-Path (Join-Path $packageDir $binaryName))) { throw "Release package does not contain the Diana executable." }
    if (-not (Test-Path (Join-Path $packageDir "frontend-next\dist\index.html"))) { throw "Release package does not contain the WebUI." }

    New-Item -ItemType Directory -Force -Path $installDir, (Join-Path $installDir "data"), (Join-Path $installDir "logs") | Out-Null
    $timestamp = (Get-Date).ToUniversalTime().ToString("yyyyMMddTHHmmssZ")
    $backupDir = Join-Path $installDir ".installer\backups\$timestamp"
    $runtimeBackup = Join-Path $backupDir "runtime"
    $dataBackup = Join-Path $backupDir "data"
    New-Item -ItemType Directory -Force -Path $runtimeBackup, $dataBackup | Out-Null

    $hadPrevious = $false
    foreach ($item in @($binaryName, "$packageName.exe", "run.bat", "frontend-next")) {
        $current = Join-Path $installDir $item
        if (Test-Path $current) {
            $hadPrevious = $true
            Move-Item -Force -Path $current -Destination (Join-Path $runtimeBackup $item)
        }
    }

    $dbPath = Join-Path $installDir "data\diana.db"
    foreach ($suffix in @("", "-wal", "-shm")) {
        if (Test-Path "$dbPath$suffix") { Copy-Item -Force "$dbPath$suffix" (Join-Path $dataBackup "diana.db$suffix") }
    }

    Copy-Item -Recurse -Force -Path (Join-Path $packageDir "*") -Destination $installDir
    $runtimeEnv = Join-Path $installDir "runtime.env"
    $generatedPassword = $null
    $generatedUsername = $null
    if (-not (Test-Path $runtimeEnv)) {
        $username = if ($env:DIANA_ADMIN_USERNAME) { $env:DIANA_ADMIN_USERNAME } else { "diana#$(New-DianaRandomHex 8)" }
        $generatedPassword = if ($env:DIANA_ADMIN_PASSWORD) { $env:DIANA_ADMIN_PASSWORD } else { New-DianaRandomHex 16 }
        $runtimeContent = @"
HOST=$bindHost
PORT=$port
APP_DB_PATH=$dbPath
LOG_PATH=$(Join-Path $installDir "logs\diana.log")
FRONTEND_DIST=$(Join-Path $installDir "frontend-next\dist")
DIANA_ADMIN_USERNAME=$username
DIANA_ADMIN_PASSWORD=$generatedPassword
"@
        $optionalLines = Get-DianaOptionalEnvLines
        if ($optionalLines) { $runtimeContent += (($optionalLines -join "`n") + "`n") }
        $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
        [IO.File]::WriteAllText($runtimeEnv, $runtimeContent, $utf8NoBom)
    } else {
        # 重装时显式传的绑定地址要生效,否则「改成 0.0.0.0 再跑一遍安装」不起作用。
        if ($hostExplicit) {
            $rebound = @(Get-Content $runtimeEnv | Where-Object { $_ -notmatch '^HOST=' })
            $rebound = @("HOST=$bindHost") + $rebound
            Set-Content -Path $runtimeEnv -Value $rebound -Encoding UTF8
            # 绑定地址是进程启动时读的:默认路径后面会重启服务;不启动时要说清楚
            # 还没生效,否则改完连不上会以为是配置没写进去。
            if ($startAfterInstall) {
                Write-Host "==> Configuration -> bind address set to $bindHost"
            } else {
                Write-Host "==> Configuration -> bind address set to $bindHost (restart Diana to apply)"
            }
        }
        $optionalLines = Get-DianaOptionalEnvLines
        if ($optionalLines) { Add-Content -Path $runtimeEnv -Value $optionalLines -Encoding UTF8 }
        $runtimeLines = Get-Content $runtimeEnv
        $usernameLine = $runtimeLines | Where-Object { $_ -match '^DIANA_ADMIN_USERNAME=' } | Select-Object -First 1
        $existingUsername = if ($usernameLine) { ($usernameLine -replace '^DIANA_ADMIN_USERNAME=', '').Trim("'", '"') } else { "" }
        if ($existingUsername -in @("diana#admin", "diana#admin0000") -or $existingUsername -notmatch '^diana#[A-Za-z0-9]{8,}$') {
            $username = "diana#$(New-DianaRandomHex 8)"
            $generatedUsername = $username
            if ($usernameLine) {
                $runtimeLines = $runtimeLines | ForEach-Object { if ($_ -match '^DIANA_ADMIN_USERNAME=') { "DIANA_ADMIN_USERNAME=$username" } else { $_ } }
            } else {
                $runtimeLines = @($runtimeLines) + "DIANA_ADMIN_USERNAME=$username"
            }
            [IO.File]::WriteAllLines($runtimeEnv, $runtimeLines, (New-Object System.Text.UTF8Encoding($false)))
            Write-Host "==> Configuration -> repaired invalid administrator username"
        }
    }
    Set-Content -Encoding ASCII -Path (Join-Path $installDir ".installed-version") -Value $version

    if ($startAfterInstall) {
        Write-Host "==> Start -> enforcing one Diana instance"
        Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
            Where-Object { $_.Name -eq $binaryName -or $_.Name -eq "$packageName.exe" -or $_.Name -like "diana-webui-*.exe" } |
            ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }

        $occupied = Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue
        if ($occupied) {
            $owners = ($occupied | Select-Object -ExpandProperty OwningProcess -Unique) -join ", "
            throw "Port $port is already used by PID $owners; Diana did not start a second instance."
        }

        Get-Content $runtimeEnv | ForEach-Object {
            if ($_ -match '^([^#=]+)=(.*)$') { [Environment]::SetEnvironmentVariable($matches[1], $matches[2], "Process") }
        }
        $executablePath = Join-Path $installDir $binaryName
        $process = Start-Process -FilePath $executablePath -WorkingDirectory $installDir -WindowStyle Hidden -PassThru
        Set-Content -Encoding ASCII -Path (Join-Path $installDir ".diana.pid") -Value $process.Id

        $healthy = $false
        for ($attempt = 0; $attempt -lt 45; $attempt++) {
            try {
                Invoke-RestMethod -TimeoutSec 2 -Uri "http://${healthHost}:$port/api/health" | Out-Null
                $healthy = $true
                break
            } catch {
                Write-Progress -Activity "Starting Diana" -Status "Health check -> $([int](100 * ($attempt + 1) / 45))%" -PercentComplete ([int](100 * ($attempt + 1) / 45))
                Start-Sleep -Seconds 1
            }
        }
        Write-Progress -Activity "Starting Diana" -Completed
        if (-not $healthy) {
            Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
            if ($hadPrevious) {
                foreach ($item in @($binaryName, "$packageName.exe", "run.bat", "frontend-next")) {
                    $current = Join-Path $installDir $item
                    $backup = Join-Path $runtimeBackup $item
                    if (Test-Path $current) { Remove-Item -Recurse -Force $current }
                    if (Test-Path $backup) { Move-Item -Force $backup $current }
                }
                foreach ($suffix in @("", "-wal", "-shm")) {
                    $backup = Join-Path $dataBackup "diana.db$suffix"
                    if (Test-Path $backup) { Copy-Item -Force $backup "$dbPath$suffix" }
                }
                $restoredExecutable = Join-Path $installDir $binaryName
                if (-not (Test-Path $restoredExecutable)) {
                    $restoredExecutable = Join-Path $installDir "$packageName.exe"
                }
                $restored = Start-Process -FilePath $restoredExecutable -WorkingDirectory $installDir -WindowStyle Hidden -PassThru
                Set-Content -Encoding ASCII -Path (Join-Path $installDir ".diana.pid") -Value $restored.Id
            }
            $logFiles = @((Join-Path $installDir "logs\diana.log"), (Join-Path $installDir "logs\installer-service.log"))
            foreach ($logFile in $logFiles) {
                if (Test-Path $logFile) {
                    Write-Error "--- $logFile`n$((Get-Content $logFile -Tail 30) -join "`n")" -ErrorAction Continue
                }
            }
            throw "Health check failed. The previous runtime was restored when available. See $backupDir."
        }
        Write-Host "==> Diana is healthy at http://${healthHost}:$port"
        if ($bindHost -in @("127.0.0.1", "localhost", "::1")) {
            # 只绑回环是「装完打不开」的头号原因。默认不改,但要让人知道开关在哪。
            Write-Host "Access:    local only (bound to $bindHost)."
            Write-Host "           To reach it from another machine, reinstall with DIANA_HOST=0.0.0.0,"
            Write-Host "           or set HOST in $runtimeEnv and restart."
            Write-Host "           The console has admin rights: keep it behind a firewall or a"
            Write-Host "           reverse proxy with TLS rather than exposing it to the internet."
        } else {
            Write-Host "Access:    listening on $bindHost - make sure the port is allowed by the"
            Write-Host "           firewall, and prefer a reverse proxy with TLS."
        }
    } else {
        Write-Host "==> Installation completed without starting Diana"
        Write-Host "Note:      configuration changes apply the next time Diana starts."
    }

    Write-Host "Installed: $installDir"
    Write-Host "Backup:    $backupDir"
    if ($generatedPassword) {
        Write-Host "Username:  $username"
        Write-Host "Password:  $generatedPassword"
        Write-Host "Credentials are stored in $runtimeEnv."
    }
    if ($generatedUsername) {
        Write-Host "Username:  $generatedUsername"
        Write-Host "The existing password remains stored in $runtimeEnv."
    }
} finally {
    if (Test-Path $tempDir) { Remove-Item -Recurse -Force $tempDir }
}
