# Deeper Windows tests: host<->module boundaries, GC, finalizers, pprof,
# load/unload stress, multi-module loads.
#
#   powershell -ExecutionPolicy Bypass -File .\run-deep.ps1 -Repo C:\path\to\goloader
#
param(
    [string]$Repo = "",
    [string]$Kit = $PSScriptRoot,
    [string]$Work = "$env:TEMP\goloader-deep"
)

$ErrorActionPreference = "Stop"
$env:GO111MODULE = "on"

if (-not $Repo) {
    # This script lives in <repo>\.github\wintest.
    $Repo = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
}
New-Item -ItemType Directory -Force -Path $Work | Out-Null

$repoFwd = $Repo.Replace('\', '/')

# Host program (module "host" plus local replace of goloader and hostkit).
Push-Location "$Kit\host"
@"
module host

go 1.21

require (
	github.com/pkujhd/goloader v0.0.0
	hostkit v0.0.0
)

replace github.com/pkujhd/goloader => $repoFwd

replace hostkit => ./hostkit
"@ | Set-Content -Encoding ascii go.mod
& go build -o "$Work\host.exe" .
if ($LASTEXITCODE -ne 0) { throw "host build failed" }
Pop-Location

function Compile-Module([string]$dir, [string]$pkg, [string]$file, [string]$out) {
    Push-Location $dir
    (& go list -export -deps -f '{{if .Export}}packagefile {{.ImportPath}}={{.Export}}{{end}}' .) |
        Set-Content -Encoding ascii "$Work\importcfg_$pkg"
    & go tool compile -p $pkg -importcfg "$Work\importcfg_$pkg" -o $out $file
    $rc = $LASTEXITCODE
    Pop-Location
    if ($rc -ne 0) { throw "compile $pkg failed" }
}

Compile-Module "$Kit\deep" "deepmod" "deep.go" "$Work\deep.o"
Compile-Module "$Kit\features" "main" "features.go" "$Work\features.o"

Write-Host "=== deep: cross-boundary, GC, finalizer, stack growth, pprof ==="
& "$Work\host.exe" -o "$Work\deep.o:deepmod" -run deepmod.Run -profile "$Work\cpu.prof" -profile-sym "deepmod."
if ($LASTEXITCODE -ne 0) { throw "deep test failed" }

Write-Host "=== deep with GOGC=1 and GC debug checks ==="
$env:GOGC = "1"
$env:GODEBUG = "clobberfree=1,gccheckmark=1,invalidptr=1"
& "$Work\host.exe" -o "$Work\deep.o:deepmod" -run deepmod.Run
if ($LASTEXITCODE -ne 0) { throw "deep GC test failed" }
Remove-Item Env:GOGC, Env:GODEBUG

Write-Host "=== features module ==="
& "$Work\host.exe" -o "$Work\features.o:main" -run main.main
if ($LASTEXITCODE -ne 0) { throw "features test failed" }

Write-Host "=== two modules in one link ==="
& "$Work\host.exe" -o "$Work\features.o:main" -o "$Work\deep.o:deepmod" -run main.main -run2 deepmod.Run
if ($LASTEXITCODE -ne 0) { throw "multi-module test failed" }

Write-Host "=== host recovers panic from loaded code ==="
& "$Work\host.exe" -o "$Work\deep.o:deepmod" -run deepmod.PanicBoom -recover
if ($LASTEXITCODE -ne 0) { throw "panic recovery test failed" }

Write-Host "=== load/unload stress (120x) ==="
& "$Work\host.exe" -o "$Work\deep.o:deepmod" -run deepmod.Quick -stress 120
if ($LASTEXITCODE -ne 0) { throw "stress test failed" }

Write-Host "ALL DEEP TESTS PASSED"
