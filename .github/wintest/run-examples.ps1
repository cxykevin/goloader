# Windows test matrix for goloader (Go 1.27).
#
#   powershell -ExecutionPolicy Bypass -File .\run-examples.ps1 -Repo C:\path\to\goloader
#
param(
    [string]$Repo = "",
    [string]$Work = "$env:TEMP\goloader-win",
    [int]$Times = 2
)

$ErrorActionPreference = "Stop"
$env:GO111MODULE = "on"

if (-not $Repo) {
    # This script lives in <repo>\.github\wintest.
    $Repo = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
}

New-Item -ItemType Directory -Force -Path $Work | Out-Null

$goroot = (& go env GOROOT).Trim()
if (-not (Test-Path "$goroot\src\cmd\objfile")) {
    Write-Host "preparing cmd/objfile in $goroot"
    Copy-Item -Recurse "$goroot\src\cmd\internal" "$goroot\src\cmd\objfile"
}

Push-Location $Repo
& go build -o "$Work\loader.exe" .\examples\loader
if ($LASTEXITCODE -ne 0) { throw "loader build failed" }
Pop-Location

function Compile-Example([string]$name) {
    $src = Join-Path $Repo "examples\$name\$name.go"
    $cfg = Join-Path $Work "importcfg_$name"
    Push-Location (Split-Path $src)
    $file = Split-Path $src -Leaf
    (& go list -export -deps -f '{{if .Export}}packagefile {{.ImportPath}}={{.Export}}{{end}}' $file) |
        Set-Content -Encoding ascii $cfg
    & go tool compile -importcfg $cfg -o "$Work\$name.o" $file
    $rc = $LASTEXITCODE
    Pop-Location
    if ($rc -ne 0) { throw "compile $name failed" }
}

foreach ($name in @("const", "base", "gctest", "schedule", "inline")) {
    Compile-Example $name
    Write-Host "=== example: $name ==="
    if ($name -eq "inline") {
        # inline intentionally panics; the traceback must contain main.inline
        $out = & "$Work\loader.exe" -o "$Work\$name.o" -run main.main 2>&1 | Out-String
        Write-Host $out
        if ($out -notmatch "main\.inline") { throw "inline traceback missing loaded frame" }
    } else {
        & "$Work\loader.exe" -o "$Work\$name.o" -run main.main -times $Times
        if ($LASTEXITCODE -ne 0) { throw "example $name failed" }
    }
}

Write-Host "=== example: http ==="
Compile-Example "http"
$srv = Start-Process -FilePath "$Work\loader.exe" -ArgumentList @("-o", "$Work\http.o", "-run", "main.main") -PassThru -WindowStyle Hidden
try {
    Start-Sleep -Seconds 2
    $body = (Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:9090/" -TimeoutSec 5).Content
    $code = (Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:2300/" -TimeoutSec 5).StatusCode
    Write-Host "9090 body=$body 2300 status=$code"
    if ($body -notmatch "Hello goloader" -or $code -ne 200) { throw "http example failed" }
} finally {
    Stop-Process -Id $srv.Id -Force -ErrorAction SilentlyContinue
}

Write-Host "=== -parse ==="
& "$Work\loader.exe" -parse "$Work\const.o" | Select-Object -First 1

Write-Host "ALL EXAMPLE TESTS PASSED"
