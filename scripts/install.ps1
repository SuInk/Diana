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
# DIANA_CONFIG_FILE 指向一份 YAML 片段,内容原样并进生成的 config.yaml。
$extraConfigFile = $env:DIANA_CONFIG_FILE
if ($extraConfigFile -and -not (Test-Path $extraConfigFile)) {
    throw "DIANA_CONFIG_FILE does not exist: $extraConfigFile"
}
# 这些配置在装的时候填好比装完再进 WebUI 改一遍省事,尤其是无人值守部署。
# 键名沿用环境变量的写法只是为了让调用方式不变,实际写进的是 config.yaml。
$optionalSections = @(
    @{ Section = "storage"; Keys = @{ "DIANA_LOCAL_MEDIA_BASE_URL" = "local_media_base_url" } },
    @{ Section = "napcat"; Keys = [ordered]@{ "DIANA_NAPCAT_WEBUI_URL" = "webui_url"; "DIANA_NAPCAT_WEBUI_TOKEN" = "webui_token" } },
    @{ Section = "llm"; Keys = [ordered]@{ "LLM_API_KEY" = "api_key"; "LLM_BASE_URL" = "base_url"; "LLM_MODEL" = "model"; "LLM_API_FORMAT" = "api_format"; "LLM_IMAGE_MODEL" = "image_model" } }
)

# 绑定地址不是回环时,健康检查要打到真正在听的地址;0.0.0.0 是通配符,
# 本机仍从回环探测。
# 注意 elseif/else 必须跟在右花括号同一行:换行写会让 PowerShell 把 if 当成
# 已结束的语句,后面的 elseif 直接语法错误。
$healthHost = if ($bindHost -in @("", "0.0.0.0", "::", "*")) { "127.0.0.1" } elseif ($bindHost -like "*:*") { "[$bindHost]" } else { $bindHost }

# ConvertTo-DianaYamlScalar 把值包成单引号 YAML 标量,内部单引号按 YAML 规则翻倍。
function ConvertTo-DianaYamlScalar {
    param([string]$Value)
    return "'" + ($Value -replace "'", "''") + "'"
}

# Get-DianaOptionalConfigLines 生成可选配置段。一个都没传的段整段不写。
function Get-DianaOptionalConfigLines {
    $lines = @()
    foreach ($group in $optionalSections) {
        $written = $false
        foreach ($key in $group.Keys.Keys) {
            $value = [Environment]::GetEnvironmentVariable($key)
            if (-not $value) { continue }
            if (-not $written) { $lines += "$($group.Section):"; $written = $true }
            $lines += "  $($group.Keys[$key]): $(ConvertTo-DianaYamlScalar $value)"
        }
    }
    if ($extraConfigFile) {
        # 原样并入:这段由部署者自己写,内容必须是合法 YAML 顶层段。
        $lines += Get-Content $extraConfigFile
    }
    return $lines
}

# Set-DianaYamlValue 改写指定顶层段下的一个键,段内没有就追加到段末。
function Set-DianaYamlValue {
    param([string]$Path, [string]$Section, [string]$Key, [string]$Value)
    $scalar = ConvertTo-DianaYamlScalar $Value
    $result = @()
    $inSection = $false
    $done = $false
    foreach ($line in @(Get-Content $Path)) {
        if ($line -match '^[a-z_]+:\s*$') {
            if ($inSection -and -not $done) { $result += "  $($Key): $scalar"; $done = $true }
            $inSection = ($line.Trim() -eq "$($Section):")
            $result += $line
            continue
        }
        if ($inSection -and -not $done -and $line -match "^\s+$($Key):") {
            $result += "  $($Key): $scalar"
            $done = $true
            continue
        }
        $result += $line
    }
    if (-not $done) {
        if (-not $inSection) { $result += "$($Section):" }
        $result += "  $($Key): $scalar"
    }
    [IO.File]::WriteAllLines($Path, $result, (New-Object System.Text.UTF8Encoding($false)))
}

# Get-DianaYamlValue 读回指定顶层段下的一个键,用于重装时保留已生成的凭据。
function Get-DianaYamlValue {
    param([string]$Path, [string]$Section, [string]$Key)
    $inSection = $false
    foreach ($line in @(Get-Content $Path)) {
        if ($line -match '^[a-z_]+:\s*$') { $inSection = ($line.Trim() -eq "$($Section):"); continue }
        if ($inSection -and $line -match "^\s+$($Key):\s*(.*)$") {
            $raw = $matches[1].Trim()
            if ($raw.StartsWith("'") -and $raw.EndsWith("'") -and $raw.Length -ge 2) {
                return $raw.Substring(1, $raw.Length - 2) -replace "''", "'"
            }
            return $raw
        }
    }
    return ""
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
    $backupsRoot = Join-Path $installDir ".installer\backups"
    if (Test-Path -LiteralPath $backupsRoot) {
        Get-ChildItem -LiteralPath $backupsRoot -Directory -Force | ForEach-Object {
            Remove-Item -LiteralPath $_.FullName -Recurse -Force -ErrorAction Stop
        }
    }
    $backupDir = Join-Path $installDir ".installer\backups\$timestamp"
    $runtimeBackup = Join-Path $backupDir "runtime"
    $dataBackup = Join-Path $backupDir "data"
    New-Item -ItemType Directory -Force -Path $runtimeBackup, $dataBackup | Out-Null

    $hadPrevious = $false
    foreach ($item in @($binaryName, "$packageName.exe", "run.bat", "uninstall.ps1", "frontend-next")) {
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
    $configFile = Join-Path $installDir "config.yaml"
    $generatedPassword = $null
    $generatedUsername = $null
    if (-not (Test-Path $configFile)) {
        $username = if ($env:DIANA_ADMIN_USERNAME) { $env:DIANA_ADMIN_USERNAME } else { "diana#$(New-DianaRandomHex 8)" }
        $generatedPassword = if ($env:DIANA_ADMIN_PASSWORD) { $env:DIANA_ADMIN_PASSWORD } else { New-DianaRandomHex 16 }
        $configLines = @(
            "# Diana 配置。基础设施段每次启动生效;bot / llm 段只在数据库为空时播种一次,",
            "# 之后以 WebUI 里的配置为准。完整字段见仓库里的 config.example.yaml。",
            "server:",
            "  host: $(ConvertTo-DianaYamlScalar $bindHost)",
            "  port: $(ConvertTo-DianaYamlScalar ([string]$port))",
            "  frontend_dist: $(ConvertTo-DianaYamlScalar (Join-Path $installDir 'frontend-next\dist'))",
            "storage:",
            "  db_path: $(ConvertTo-DianaYamlScalar $dbPath)",
            "  log_path: $(ConvertTo-DianaYamlScalar (Join-Path $installDir 'logs\diana.log'))",
            "admin:",
            "  username: $(ConvertTo-DianaYamlScalar $username)",
            "  password: $(ConvertTo-DianaYamlScalar $generatedPassword)"
        )
        $configLines += Get-DianaOptionalConfigLines
        [IO.File]::WriteAllLines($configFile, $configLines, (New-Object System.Text.UTF8Encoding($false)))
    } else {
        # 重装时显式传的绑定地址要生效,否则「改成 0.0.0.0 再跑一遍安装」不起作用。
        if ($hostExplicit) {
            Set-DianaYamlValue -Path $configFile -Section "server" -Key "host" -Value $bindHost
            # 绑定地址是进程启动时读的:默认路径后面会重启服务;不启动时要说清楚
            # 还没生效,否则改完连不上会以为是配置没写进去。
            if ($startAfterInstall) {
                Write-Host "==> Configuration -> bind address set to $bindHost"
            } else {
                Write-Host "==> Configuration -> bind address set to $bindHost (restart Diana to apply)"
            }
        }
        if ($env:DIANA_PORT) { Set-DianaYamlValue -Path $configFile -Section "server" -Key "port" -Value ([string]$port) }
        $existingUsername = Get-DianaYamlValue -Path $configFile -Section "admin" -Key "username"
        if ($existingUsername -in @("diana#admin", "diana#admin0000") -or $existingUsername -notmatch '^diana#[A-Za-z0-9]{8,}$') {
            $generatedUsername = "diana#$(New-DianaRandomHex 8)"
            Set-DianaYamlValue -Path $configFile -Section "admin" -Key "username" -Value $generatedUsername
            Write-Host "==> Configuration -> repaired invalid administrator username"
        }
    }
    Set-Content -Encoding ASCII -Path (Join-Path $installDir ".installed-version") -Value $version

    $commandDir = Join-Path $env:LOCALAPPDATA "Microsoft\WindowsApps"
    $commandShim = Join-Path $commandDir "diana.cmd"
    if ((Test-Path $commandDir) -and (Test-Path (Join-Path $installDir "uninstall.ps1"))) {
        "@echo off`r`n`"$installDir\$binaryName`" %*" | Set-Content -Encoding ASCII $commandShim
    }

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

        # 唯一需要传给进程的环境变量:配置文件在哪。其余配置都在 config.yaml 里。
        [Environment]::SetEnvironmentVariable("DIANA_CONFIG", $configFile, "Process")
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
                foreach ($item in @($binaryName, "$packageName.exe", "run.bat", "uninstall.ps1", "frontend-next")) {
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
        try {
            Remove-Item -LiteralPath $backupDir -Recurse -Force -ErrorAction Stop
            $backupDir = $null
        } catch {
            Write-Warning "Diana is healthy, but backup cleanup failed: $_"
        }
        if ($bindHost -in @("127.0.0.1", "localhost", "::1")) {
            # 只绑回环是「装完打不开」的头号原因。默认不改,但要让人知道开关在哪。
            Write-Host "Access:    local only (bound to $bindHost)."
            Write-Host "           To reach it from another machine, reinstall with DIANA_HOST=0.0.0.0,"
            Write-Host "           or set server.host in $configFile and restart."
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
    if (Test-Path $commandShim) { Write-Host "Command:   diana" }
    if ($backupDir) {
        Write-Host "Backup:    $backupDir"
    } else {
        Write-Host "Backup:    removed after successful health check"
    }
    if ($generatedPassword) {
        Write-Host "Username:  $username"
        Write-Host "Password:  $generatedPassword"
        Write-Host "Credentials are stored in $configFile."
    }
    if ($generatedUsername) {
        Write-Host "Username:  $generatedUsername"
        Write-Host "The existing password remains stored in $configFile."
    }
} finally {
    if (Test-Path $tempDir) { Remove-Item -Recurse -Force $tempDir }
}
