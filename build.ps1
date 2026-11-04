# Build FlexWSPR into dist\: the headless program (with its web page) for
# Debian x86-64, Raspberry Pi 64/32-bit and Windows, plus the Windows desktop
# app. The Linux desktop app must be built on Linux (see build.sh).
# Usage: .\build.ps1 [-Version 0.3.0]
param([string]$Version = "0.3.0-dev")
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot
New-Item -ItemType Directory -Force dist | Out-Null
$ld = "-s -w -X main.version=$Version"
$env:CGO_ENABLED = "0"
$env:GOARM = "7"
try {
    foreach ($t in "linux/amd64", "linux/arm64", "linux/arm", "windows/amd64") {
        $os, $arch = $t.Split("/")
        $out = "dist/flexwspr-$os-$arch"
        if ($os -eq "windows") { $out += ".exe" }
        Write-Host "building $out"
        $env:GOOS = $os
        $env:GOARCH = $arch
        go build -trimpath -ldflags $ld -o $out .
        if ($LASTEXITCODE -ne 0) { throw "build failed for $t" }
    }
    Write-Host "building dist/flexwspr-gui-windows-amd64.exe"
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    go build -trimpath -tags desktop,production -ldflags "$ld -H windowsgui" -o dist/flexwspr-gui-windows-amd64.exe ./cmd/flexwspr-gui
    if ($LASTEXITCODE -ne 0) { throw "build failed for the desktop app" }
} finally {
    Remove-Item Env:GOOS, Env:GOARCH, Env:GOARM, Env:CGO_ENABLED -ErrorAction SilentlyContinue
}
Copy-Item flexwspr.example.yaml, flexwspr.service dist/
