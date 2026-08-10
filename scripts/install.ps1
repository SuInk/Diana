#Requires -Version 5.1

$ErrorActionPreference = "Stop"

$repository = if ($env:DIANA_REPOSITORY) { $env:DIANA_REPOSITORY } else { "SuInk/Diana" }
$installDir = if ($env:DIANA_INSTALL_DIR) { $env:DIANA_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "Diana" }
$version = if ($env:DIANA_VERSION) { $env:DIANA_VERSION } else { "latest" }
$port = if ($env:DIANA_PORT) { [int]$env:DIANA_PORT } else { 18080 }
$startAfterInstall = $env:DIANA_START_AFTER_INSTALL -ne "false"

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
$archiveName = "$packageName.zip"
$baseUrl = "https://github.com/$repository/releases/download/$version"
$tempDir = Join-Path ([IO.Path]::GetTempPath()) ("diana-install-" + [guid]::NewGuid().ToString("N"))
$archivePath = Join-Path $tempDir $archiveName
$sumsPath = Join-Path $tempDir "SHA256SUMS"
$stageDir = Join-Path $tempDir "stage"

try {
    New-Item -ItemType Directory -Force -Path $tempDir, $stageDir | Out-Null
    Write-Host "==> Downloading Diana $version for windows/amd64"
    Invoke-WebRequest -UseBasicParsing -Uri "$baseUrl/$archiveName" -OutFile $archivePath
    Invoke-WebRequest -UseBasicParsing -Uri "$baseUrl/SHA256SUMS" -OutFile $sumsPath

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
    if (-not (Test-Path (Join-Path $packageDir "$packageName.exe"))) { throw "Release package does not contain the Diana executable." }
    if (-not (Test-Path (Join-Path $packageDir "frontend-next\dist\index.html"))) { throw "Release package does not contain the WebUI." }

    New-Item -ItemType Directory -Force -Path $installDir, (Join-Path $installDir "data"), (Join-Path $installDir "logs") | Out-Null
    $timestamp = (Get-Date).ToUniversalTime().ToString("yyyyMMddTHHmmssZ")
    $backupDir = Join-Path $installDir ".installer\backups\$timestamp"
    $runtimeBackup = Join-Path $backupDir "runtime"
    $dataBackup = Join-Path $backupDir "data"
    New-Item -ItemType Directory -Force -Path $runtimeBackup, $dataBackup | Out-Null

    $hadPrevious = $false
    foreach ($item in @("$packageName.exe", "run.bat", "frontend-next")) {
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
    if (-not (Test-Path $runtimeEnv)) {
        $username = if ($env:DIANA_ADMIN_USERNAME) { $env:DIANA_ADMIN_USERNAME } else { "diana#admin" }
        $generatedPassword = if ($env:DIANA_ADMIN_PASSWORD) { $env:DIANA_ADMIN_PASSWORD } else { -join ((1..32) | ForEach-Object { "0123456789abcdef"[(Get-Random -Maximum 16)] }) }
        $runtimeContent = @"
HOST=127.0.0.1
PORT=$port
APP_DB_PATH=$dbPath
LOG_PATH=$(Join-Path $installDir "logs\diana.log")
FRONTEND_DIST=$(Join-Path $installDir "frontend-next\dist")
DIANA_ADMIN_USERNAME=$username
DIANA_ADMIN_PASSWORD=$generatedPassword
"@
        $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
        [IO.File]::WriteAllText($runtimeEnv, $runtimeContent, $utf8NoBom)
    }
    Set-Content -Encoding ASCII -Path (Join-Path $installDir ".installed-version") -Value $version

    if ($startAfterInstall) {
        Write-Host "==> Starting Diana"
        Get-CimInstance Win32_Process -Filter "Name = '$packageName.exe'" -ErrorAction SilentlyContinue |
            Where-Object { $_.ExecutablePath -like "$installDir*" } |
            ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }

        Get-Content $runtimeEnv | ForEach-Object {
            if ($_ -match '^([^#=]+)=(.*)$') { [Environment]::SetEnvironmentVariable($matches[1], $matches[2], "Process") }
        }
        $executablePath = Join-Path $installDir "$packageName.exe"
        $process = Start-Process -FilePath $executablePath -WorkingDirectory $installDir -WindowStyle Hidden -PassThru
        Set-Content -Encoding ASCII -Path (Join-Path $installDir ".diana.pid") -Value $process.Id

        $healthy = $false
        for ($attempt = 0; $attempt -lt 45; $attempt++) {
            try {
                Invoke-RestMethod -TimeoutSec 2 -Uri "http://127.0.0.1:$port/api/health" | Out-Null
                $healthy = $true
                break
            } catch {
                Start-Sleep -Seconds 1
            }
        }
        if (-not $healthy) {
            Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
            if ($hadPrevious) {
                foreach ($item in @("$packageName.exe", "run.bat", "frontend-next")) {
                    $current = Join-Path $installDir $item
                    $backup = Join-Path $runtimeBackup $item
                    if (Test-Path $current) { Remove-Item -Recurse -Force $current }
                    if (Test-Path $backup) { Move-Item -Force $backup $current }
                }
                foreach ($suffix in @("", "-wal", "-shm")) {
                    $backup = Join-Path $dataBackup "diana.db$suffix"
                    if (Test-Path $backup) { Copy-Item -Force $backup "$dbPath$suffix" }
                }
                $restored = Start-Process -FilePath (Join-Path $installDir "$packageName.exe") -WorkingDirectory $installDir -WindowStyle Hidden -PassThru
                Set-Content -Encoding ASCII -Path (Join-Path $installDir ".diana.pid") -Value $restored.Id
            }
            throw "Health check failed. The previous runtime was restored when available. See $backupDir."
        }
        Write-Host "==> Diana is healthy at http://127.0.0.1:$port"
    } else {
        Write-Host "==> Installation completed without starting Diana"
    }

    Write-Host "Installed: $installDir"
    Write-Host "Backup:    $backupDir"
    if ($generatedPassword) {
        Write-Host "Username:  $username"
        Write-Host "Password:  $generatedPassword"
        Write-Host "Credentials are stored in $runtimeEnv."
    }
} finally {
    if (Test-Path $tempDir) { Remove-Item -Recurse -Force $tempDir }
}
