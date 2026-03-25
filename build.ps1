# build.ps1 — 交互式交叉编译脚本（在 Windows 上编译 Linux / macOS / Windows）
# 用法:
#   .\build.ps1                  # 交互式选择
#   .\build.ps1 -OS linux -Arch amd64   # 直接指定

param(
    [ValidateSet("linux", "darwin", "windows", "")]
    [string]$OS = "",

    [ValidateSet("amd64", "arm64", "")]
    [string]$Arch = ""
)

$ErrorActionPreference = "Stop"

# ---------- 交互式选择 ----------
if (-not $OS) {
    Write-Host ""
    Write-Host "=== Mini-Agent Build ===" -ForegroundColor Cyan
    Write-Host "  1) linux"
    Write-Host "  2) darwin (macOS)"
    Write-Host "  3) windows"
    Write-Host ""
    $choice = Read-Host "选择目标平台 [1/2/3]"
    switch ($choice) {
        "1" { $OS = "linux"   }
        "2" { $OS = "darwin"  }
        "3" { $OS = "windows" }
        default { Write-Error "无效选择"; exit 1 }
    }
}

if (-not $Arch) {
    Write-Host ""
    Write-Host "  1) amd64 (x86_64)"
    Write-Host "  2) arm64 (Apple Silicon / ARM)"
    Write-Host ""
    $choice = Read-Host "选择目标架构 [1/2]"
    switch ($choice) {
        "1" { $Arch = "amd64" }
        "2" { $Arch = "arm64" }
        default { Write-Error "无效选择"; exit 1 }
    }
}

# ---------- 编译 ----------
$env:GOOS   = $OS
$env:GOARCH = $Arch
$env:CGO_ENABLED = "0"

$ext = if ($OS -eq "windows") { ".exe" } else { "" }
$outName = "agent-${OS}-${Arch}${ext}"
$outDir  = Join-Path $PSScriptRoot "dist"

if (-not (Test-Path $outDir)) {
    New-Item -ItemType Directory -Path $outDir | Out-Null
}

$outPath = Join-Path $outDir $outName

Push-Location (Join-Path $PSScriptRoot "src")
try {
    Write-Host ""
    Write-Host "Building $outName ..." -ForegroundColor Cyan
    go build -trimpath -ldflags="-s -w" -o $outPath ./cmd/agent/
    if ($LASTEXITCODE -ne 0) { throw "go build failed" }
    Write-Host "OK -> $outPath" -ForegroundColor Green
} finally {
    Pop-Location
    Remove-Item Env:\GOOS
    Remove-Item Env:\GOARCH
    Remove-Item Env:\CGO_ENABLED
}
