# Runs Pitstop's checks on Windows and writes everything they print
# to test-results.txt in the project's root.
#
#   powershell -ExecutionPolicy Bypass -File tools\windows\test.ps1
#
# 1. The unit tests of Pitstop and of chrome_update, the library it builds
#    against.
# 2. The live tests, against Google's and GitHub's real servers: protocol 4
#    for every channel and architecture, then TestLive, which sets up an older
#    Chrome in a scratch folder, updates it, rolls back while Chrome runs,
#    verifies Google's signatures and resumes a download that was killed.
#    TestLive downloads Chrome three times.
param(
    [switch]$SkipUnit,
    [switch]$SkipLive
)

$ErrorActionPreference = "Continue"
$Root = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$Log = Join-Path $Root "test-results.txt"
$script:Failures = @()

Start-Transcript -Path $Log -Force | Out-Null

function Section($name) {
    Write-Host ""
    Write-Host "=== $name ===" -ForegroundColor Cyan
}

function Check($name, $ok) {
    if ($ok) {
        Write-Host "PASS  $name" -ForegroundColor Green
    } else {
        Write-Host "FAIL  $name" -ForegroundColor Red
        $script:Failures += $name
    }
}

Section "System"
$os = Get-CimInstance Win32_OperatingSystem
Write-Host "$($os.Caption) $($os.Version) build $($os.BuildNumber), $env:PROCESSOR_ARCHITECTURE"
Write-Host "UI language: $((Get-Culture).Name), $((Get-UICulture).Name)"
try {
    $mp = Get-MpComputerStatus -ErrorAction Stop
    Write-Host "Defender real-time protection: $($mp.RealTimeProtectionEnabled)"
} catch {
    Write-Host "Defender status unavailable"
}
Write-Host "Project: $Root"

if (-not $SkipUnit) {
    Section "Unit tests (go test)"
    go version
    foreach ($pkgs in "./...", "github.com/Bush2021/chrome_update/...") {
        go -C $Root test $pkgs 2>&1 | ForEach-Object { "$_" }
        Check "go test $pkgs" ($LASTEXITCODE -eq 0)
    }
}

if (-not $SkipLive) {
    $env:CHROME_UPDATE_LIVE = "1"
    $env:PITSTOP_LIVE = "1"
    Section "Live: protocol 4, every channel and architecture"
    go -C $Root test -count=1 -run Live -v github.com/Bush2021/chrome_update/omaha 2>&1 | ForEach-Object { "$_" }
    Check "protocol 4, every channel and architecture" ($LASTEXITCODE -eq 0)
    Section "Live: TestLive"
    go -C $Root test -count=1 -run '^TestLive$' -v -timeout 60m ./internal/pitstop 2>&1 | ForEach-Object { "$_" }
    Check "TestLive" ($LASTEXITCODE -eq 0)
    Remove-Item Env:\CHROME_UPDATE_LIVE, Env:\PITSTOP_LIVE
}

Section "Summary"
if ($script:Failures.Count -eq 0) {
    Write-Host "Everything passed." -ForegroundColor Green
} else {
    Write-Host "Failed: $($script:Failures -join '; ')" -ForegroundColor Red
}
Write-Host "The log is $Log"
Stop-Transcript | Out-Null
if ($script:Failures.Count -gt 0) {
    exit 1
}
