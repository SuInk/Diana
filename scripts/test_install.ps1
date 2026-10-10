# Parse the full installer and exercise options without downloads or installation.
$ErrorActionPreference = "Stop"
$source = Get-Content -LiteralPath (Join-Path $PSScriptRoot "install.ps1") -Raw
$tokens = $null
$errors = $null
[void][Management.Automation.Language.Parser]::ParseInput($source, [ref]$tokens, [ref]$errors)
if ($errors.Count) { throw ($errors | Out-String) }
$boundary = $source.IndexOf("# 绑定地址不是回环")
if ($boundary -lt 0) { throw "Installer initialization boundary missing." }
$init = [scriptblock]::Create($source.Substring(0, $boundary) + @'
[pscustomobject]@{
    Directory = $InstallDir
    HostValue = $BindHost
    PortValue = $Port
    Start = $startAfterInstall
    ExplicitHost = $hostExplicit
    ExplicitPort = $portExplicit
}
'@)
$temp = Join-Path ([IO.Path]::GetTempPath()) ("diana-options-" + [guid]::NewGuid())
$oldPort = $env:DIANA_PORT
try {
    $env:DIANA_PORT = "9999"
    $result = & $init -InstallDir $temp -BindHost "0.0.0.0" -Port 18081 -NoStart -Yes
    if ($result.Directory -ne $temp -or $result.HostValue -ne "0.0.0.0" -or
        $result.PortValue -ne 18081 -or $result.Start -or
        -not $result.ExplicitHost -or -not $result.ExplicitPort) {
        throw "Explicit installer options were not respected."
    }
    $result = & $init -InstallDir $temp -Yes
    if ($result.PortValue -ne 18080 -or $result.ExplicitPort -or -not $result.Start) {
        throw "Defaults must not read legacy environment configuration."
    }
    if (Test-Path -LiteralPath $temp) { throw "Options parsing created an installation." }
} finally {
    $env:DIANA_PORT = $oldPort
}
Write-Host "PowerShell installer syntax and option checks passed."
