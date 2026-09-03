# Downloads the release binary for this host into bin\herdr-tg.exe and
# checks its SHA-256 against the release checksums file. Run by herdr as
# the plugin's [[build]] step on windows; it gets no HERDR_* variables.
# $env:HERDR_TG_BASE_URL overrides the download location.
$ErrorActionPreference = "Stop"

Set-Location (Join-Path $PSScriptRoot "..")

$repo = "permgps/herdr-telegram-agents"

$match = Select-String -Path herdr-plugin.toml -Pattern '^version\s*=\s*"(.*)"' | Select-Object -First 1
if (-not $match) { throw "install: no version in herdr-plugin.toml" }
$version = $match.Matches[0].Groups[1].Value

$arch = "amd64"
if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq "Arm64") {
    Write-Host "install: no windows/arm64 build; using the amd64 binary under emulation"
}

$asset = "herdr-tg_windows_$arch.exe"
$base = $env:HERDR_TG_BASE_URL
if (-not $base) { $base = "https://github.com/$repo/releases/download/v$version" }
Write-Host "install: herdr-tg $version for windows/$arch"

New-Item -ItemType Directory -Force -Path bin | Out-Null
$tmp = "bin\herdr-tg.tmp"
$sums = "bin\checksums.txt"

try {
    Write-Host "install: downloading $asset"
    Invoke-WebRequest -Uri "$base/$asset" -OutFile $tmp -UseBasicParsing
    Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile $sums -UseBasicParsing

    $line = Select-String -Path $sums -Pattern "\s$([regex]::Escape($asset))$" | Select-Object -First 1
    if (-not $line) { throw "install: $asset is missing from checksums.txt" }
    $expected = ($line.Line -split '\s+')[0].ToLower()
    $actual = (Get-FileHash -Algorithm SHA256 $tmp).Hash.ToLower()
    if ($expected -ne $actual) {
        throw "install: checksum mismatch for $asset (expected $expected, got $actual)"
    }
    Write-Host "install: checksum ok"

    Move-Item -Force $tmp "bin\herdr-tg.exe"
    Write-Host "install: installed bin\herdr-tg.exe"
} finally {
    if (Test-Path $tmp) { Remove-Item -Force $tmp }
    if (Test-Path $sums) { Remove-Item -Force $sums }
}

& ".\bin\herdr-tg.exe" version
