# HomeCloud installer for Windows (PowerShell 5+).
#   irm https://raw.githubusercontent.com/homecloudhq/homecloud/main/scripts/install.ps1 | iex
# Set $env:HOMECLOUD_VERSION (e.g. v0.1.0) to pin a release.
$ErrorActionPreference = "Stop"
$repo = "homecloudhq/homecloud"

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    Write-Host "HomeCloud runs every service on Docker, which is not installed."
    Write-Host "Install Docker Desktop from https://www.docker.com/products/docker-desktop, start it, then re-run this script."
    exit 1
}

$arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
$version = $env:HOMECLOUD_VERSION
if (-not $version) {
    $version = (Invoke-RestMethod "https://api.github.com/repos/$repo/releases?per_page=1")[0].tag_name
}

$name = "homecloud-windows-$arch"
$tmp = Join-Path $env:TEMP "homecloud-install"
New-Item -ItemType Directory -Force -Path $tmp | Out-Null
Write-Host "Downloading HomeCloud $version for windows/$arch..."
Invoke-WebRequest "https://github.com/$repo/releases/download/$version/$name.zip" -OutFile "$tmp\$name.zip"
Expand-Archive -Force "$tmp\$name.zip" -DestinationPath $tmp

$dest = Join-Path $env:LOCALAPPDATA "Programs\HomeCloud"
New-Item -ItemType Directory -Force -Path $dest | Out-Null
Copy-Item -Force "$tmp\$name\homecloud.exe" "$dest\homecloud.exe"

$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$dest*") {
    [Environment]::SetEnvironmentVariable("Path", "$userPath;$dest", "User")
    Write-Host "Added $dest to your PATH (open a new terminal to use it)."
}
Remove-Item -Recurse -Force $tmp

& "$dest\homecloud.exe" version
Write-Host ""
Write-Host "Start your cloud with:   homecloud serve"
Write-Host "Then open:               http://127.0.0.1:8080"
