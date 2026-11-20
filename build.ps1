# Builds ShareMile-Portable.exe + ShareMile-Setup.exe and keeps every version side by side in builds\vX.Y.Z
# Usage:  .\build.ps1 -Version 1.0.0
param([string]$Version = "1.0.0")
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot
$env:Path = [Environment]::GetEnvironmentVariable("Path","Machine") + ";" + [Environment]::GetEnvironmentVariable("Path","User")

$v4 = "$Version.0"
$winres = Join-Path (go env GOPATH) "bin\go-winres.exe"
if (-not (Test-Path $winres)) { go install github.com/tc-hib/go-winres@latest }
& $winres make --in winres\winres.json --arch amd64 --product-version $v4 --file-version $v4
if ($LASTEXITCODE -ne 0) { throw "go-winres failed" }

New-Item -ItemType Directory -Force build, dist | Out-Null
go build -trimpath -ldflags "-s -w -H windowsgui -X main.appVersion=$Version" -o build\ShareMile.exe .
if ($LASTEXITCODE -ne 0) { throw "go build failed" }

$iscc = @("$env:LOCALAPPDATA\Programs\Inno Setup 6\ISCC.exe", "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe") | Where-Object { Test-Path $_ } | Select-Object -First 1
if (-not $iscc) { throw "Inno Setup 6 not found" }
& $iscc /Q "/DAppVersion=$Version" installer\ShareMile.iss
if ($LASTEXITCODE -ne 0) { throw "installer build failed" }
# The installed app is still ShareMile.exe; only the stand-alone download says Portable.
Remove-Item dist\ShareMile.exe -ErrorAction SilentlyContinue
Copy-Item build\ShareMile.exe dist\ShareMile-Portable.exe -Force

# Versioned build history: never overwrite an existing version folder.
$dest = "builds\v$Version"
if (Test-Path $dest) {
  Write-Host "builds\v$Version already exists; left untouched. Bump the version to keep a new build." -ForegroundColor Yellow
} else {
  New-Item -ItemType Directory -Force $dest | Out-Null
  Copy-Item dist\ShareMile-Portable.exe, dist\ShareMile-Setup.exe $dest
  Write-Host "Saved build to $dest"
}
Get-ChildItem dist | Select-Object Name, @{n="MB";e={[math]::Round($_.Length/1MB,2)}}
