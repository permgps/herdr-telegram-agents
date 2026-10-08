# Downloads the release binary for this host into bin\herdr-tg.exe and
# checks its SHA-256 against the release checksums file. Run by herdr as
# the plugin's [[build]] step on windows; it gets no HERDR_* variables.
# $env:HERDR_TG_BASE_URL overrides the download location; it must be
# https:// unless $env:HERDR_TG_ALLOW_INSECURE_BASE is "1". The plugin's
# updater sets $env:HERDR_TG_EXPECTED_SHA256 to the checksum the owner
# approved: the binary must match it as well as checksums.txt.
#
# Signed releases also carry release.txt and release.txt.sig (SSHSIG by a key
# in scripts\signing\allowed_signers). When ssh-keygen can verify SSHSIG
# (proven by the self-test fixture in scripts\signing\), a bad signature
# refuses the install; otherwise the script warns and trusts checksums.txt.
# The binary runs (`herdr-tg version`) only after a verified signature or a
# matching approved checksum. bin\install-receipt records sha256, approved
# and signature for the plugin's updater. Mirrors scripts/install.sh.
$ErrorActionPreference = "Stop"

# -LiteralPath: a checkout under a folder such as plugin[1] must not be read
# as a wildcard pattern. Every later path is relative to this directory.
Set-Location -LiteralPath (Join-Path $PSScriptRoot "..")
Write-Host "install: working in $((Get-Location).ProviderPath)"

$repo = "permgps/herdr-telegram-agents"

# Get-FileHash is a script function that Windows PowerShell 5.1 autoloads
# from its Utility module. When Herdr was started from PowerShell 7, this
# process inherits pwsh's module paths first and cannot load it, so hash
# through .NET, which needs no module.
function Get-Sha256Hex([string]$Path) {
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try {
        $stream = [System.IO.File]::OpenRead((Resolve-Path -LiteralPath $Path -ErrorAction Stop).ProviderPath)
        try {
            return ([System.BitConverter]::ToString($sha.ComputeHash($stream)) -replace '-', '').ToLower()
        } finally {
            $stream.Dispose()
        }
    } finally {
        $sha.Dispose()
    }
}

# Get-Optional downloads a release file and returns $false when the release
# has no such asset (HTTP 404); any other failure is thrown. Windows
# PowerShell 5.1 has no -SkipHttpErrorCheck, so the 404 is read from the
# exception's response.
function Get-Optional([string]$Uri, [string]$OutFile) {
    try {
        Invoke-WebRequest -Uri $Uri -OutFile $OutFile -UseBasicParsing -TimeoutSec 60 -MaximumRedirection 5
        return $true
    } catch {
        $response = $_.Exception.Response
        if ($response -and [int]$response.StatusCode -eq 404) { return $false }
        throw
    }
}

# Invoke-SshVerify runs ssh-keygen -Y verify with the message file's exact
# bytes on stdin. A PowerShell pipe into a native program would re-encode
# the text, and cmd /c quoting is fragile, so the process is driven directly.
function Invoke-SshVerify([string]$KeyGen, [string[]]$Arguments, [string]$MessagePath) {
    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = $KeyGen
    $psi.Arguments = ($Arguments | ForEach-Object { '"' + $_ + '"' }) -join ' '
    $psi.WorkingDirectory = (Get-Location).ProviderPath
    $psi.UseShellExecute = $false
    $psi.CreateNoWindow = $true
    $psi.RedirectStandardInput = $true
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    $process = [System.Diagnostics.Process]::Start($psi)
    try {
        $bytes = [System.IO.File]::ReadAllBytes((Resolve-Path -LiteralPath $MessagePath).ProviderPath)
        $process.StandardInput.BaseStream.Write($bytes, 0, $bytes.Length)
        $process.StandardInput.Close()
        $stderr = $process.StandardError.ReadToEndAsync()
        $null = $process.StandardOutput.ReadToEnd()
        $null = $stderr.Result
        $process.WaitForExit()
        return $process.ExitCode
    } finally {
        $process.Dispose()
    }
}

# Test-CanVerify is true when this ssh-keygen verifies the bundled self-test
# signature, so a later failure means a bad signature, not an old tool.
function Test-CanVerify([string]$KeyGen) {
    if (-not $KeyGen) { return $false }
    try {
        $code = Invoke-SshVerify $KeyGen @("-Y", "verify", "-f", "scripts\signing\selftest_signers", "-I", "herdr-tg-selftest", "-n", "herdr-tg-selftest", "-s", "scripts\signing\selftest.txt.sig") "scripts\signing\selftest.txt"
        return $code -eq 0
    } catch {
        return $false
    }
}

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
if (-not $base.StartsWith("https://") -and $env:HERDR_TG_ALLOW_INSECURE_BASE -ne "1") {
    throw "install: HERDR_TG_BASE_URL must be https:// (set HERDR_TG_ALLOW_INSECURE_BASE=1 for a local snapshot)"
}
Write-Host "install: herdr-tg $version for windows/$arch"

New-Item -ItemType Directory -Force -Path bin | Out-Null
# Unique names: a run killed half-way must not break the next one.
$suffix = [System.Guid]::NewGuid().ToString("N")
$tmp = "bin\herdr-tg.$suffix.tmp"
$sums = "bin\checksums.$suffix.txt"
$stmt = "bin\release.$suffix.txt"
$sig = "bin\release.$suffix.sig"
$receipt = "bin\receipt.$suffix.tmp"
$signature = "absent"

try {
    Write-Host "install: downloading $asset"
    Invoke-WebRequest -Uri "$base/$asset" -OutFile $tmp -UseBasicParsing -TimeoutSec 120 -MaximumRedirection 5
    Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile $sums -UseBasicParsing -TimeoutSec 60 -MaximumRedirection 5

    $lines = @(Select-String -Path $sums -Pattern "\s$([regex]::Escape($asset))$")
    if ($lines.Count -eq 0) { throw "install: $asset is missing from checksums.txt" }
    if ($lines.Count -ne 1) { throw "install: $asset is listed $($lines.Count) times in checksums.txt" }
    $expected = ($lines[0].Line -split '\s+')[0].ToLower()
    $actual = Get-Sha256Hex $tmp
    if ($expected -ne $actual) {
        throw "install: checksum mismatch for $asset (expected $expected, got $actual)"
    }
    $approved = "$env:HERDR_TG_EXPECTED_SHA256".ToLower()
    if ($approved -and $approved -ne $actual) {
        throw "install: $asset differs from the approved checksum (approved $approved, got $actual)"
    }
    Write-Host "install: checksum ok"

    if ((Get-Optional "$base/release.txt" $stmt) -and (Get-Optional "$base/release.txt.sig" $sig)) {
        $keygen = (Get-Command ssh-keygen -ErrorAction SilentlyContinue | Select-Object -First 1).Source
        if (Test-CanVerify $keygen) {
            $code = Invoke-SshVerify $keygen @("-Y", "verify", "-f", "scripts\signing\allowed_signers", "-I", "herdr-tg-release", "-n", "herdr-tg-release", "-s", $sig) $stmt
            if ($code -ne 0) { throw "install: release signature does not verify" }
            $text = [System.IO.File]::ReadAllText((Resolve-Path -LiteralPath $stmt).ProviderPath)
            if (($text -split "`n")[0] -ne "tag v$version") { throw "install: the signed statement is not for v$version" }
            $signedLines = @(Select-String -Path $stmt -Pattern "\s$([regex]::Escape($asset))$")
            if ($signedLines.Count -ne 1) { throw "install: the signed statement does not list $asset exactly once" }
            $signed = ($signedLines[0].Line -split '\s+')[0].ToLower()
            if ($signed -ne $expected) { throw "install: the signed checksum for $asset differs from checksums.txt" }
            $signature = "verified"
            Write-Host "install: release signature ok"
        } else {
            $signature = "unverifiable"
            Write-Warning "install: cannot check the release signature (needs OpenSSH 8.1 or newer); trusting checksums.txt"
        }
    } else {
        Write-Warning "install: release v$version is not signed; trusting checksums.txt"
    }

    $approvedText = if ($approved) { $approved } else { "none" }
    $receiptPath = Join-Path (Get-Location).ProviderPath $receipt
    [System.IO.File]::WriteAllText($receiptPath, "sha256 $actual`napproved $approvedText`nsignature $signature`n", (New-Object System.Text.UTF8Encoding($false)))
    Move-Item -Force -LiteralPath $tmp -Destination "bin\herdr-tg.exe"
    Move-Item -Force -LiteralPath $receipt -Destination "bin\install-receipt"
    Write-Host "install: installed bin\herdr-tg.exe"
} finally {
    foreach ($leftover in @($tmp, $sums, $stmt, $sig, $receipt)) {
        if (Test-Path -LiteralPath $leftover) { Remove-Item -Force -LiteralPath $leftover }
    }
}

if ($signature -eq "verified" -or $approved) {
    & ".\bin\herdr-tg.exe" version
} else {
    Write-Host "install: skipped running an unverified binary"
}
