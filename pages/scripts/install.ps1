# HomeCloud installer for Windows (PowerShell 5+).
#   irm https://homecloud.pages.dev/scripts/install.ps1 | iex
# Set $env:HOMECLOUD_VERSION (e.g. v0.1.0) to pin a release.
# The download is checked against the release's SHA-256 checksums, and, when
# cosign is installed, the checksums against the release's Sigstore signature.
$ErrorActionPreference = "Stop"
$repo = "solinode/homecloud"

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
$releases = if ($env:HOMECLOUD_RELEASES_URL) { $env:HOMECLOUD_RELEASES_URL } else { "https://github.com/$repo/releases/download" }
$base = "$releases/$version"
$tmp = Join-Path $env:TEMP "homecloud-install"
New-Item -ItemType Directory -Force -Path $tmp | Out-Null
Write-Host "Downloading HomeCloud $version for windows/$arch..."
Invoke-WebRequest "$base/$name.zip" -OutFile "$tmp\$name.zip"
Invoke-WebRequest "$base/checksums.txt" -OutFile "$tmp\checksums.txt"

# Releases from v0.5.0 on sign checksums.txt with Sigstore (cosign keyless, by
# the release workflow). With cosign installed the signature is verified;
# without it, only the SHA-256 checksum is.
$v = ($version.TrimStart("v") -split "-")[0] -split "\."
# Unparseable versions count as unsigned, as in install.sh.
$major = 0; $minor = 0
$signedRelease = $false
if ($v.Count -ge 2 -and [int]::TryParse($v[0], [ref]$major) -and [int]::TryParse($v[1], [ref]$minor)) {
    $signedRelease = ($major -gt 0) -or ($minor -ge 5)
}
if (Get-Command cosign -ErrorAction SilentlyContinue) {
    $bundle = "$tmp\checksums.txt.sigstore.json"
    $haveBundle = $true
    try { Invoke-WebRequest "$base/checksums.txt.sigstore.json" -OutFile $bundle } catch { $haveBundle = $false }
    if ($haveBundle) {
        & cosign verify-blob "$tmp\checksums.txt" --bundle $bundle `
            --certificate-identity "https://github.com/$repo/.github/workflows/release.yml@refs/tags/$version" `
            --certificate-oidc-issuer "https://token.actions.githubusercontent.com"
        if ($LASTEXITCODE -ne 0) {
            Write-Error "Signature verification of $version FAILED: not installing."
            exit 1
        }
        Write-Host "Verified the release signature with cosign."
    } elseif ($signedRelease) {
        Write-Error "The signature of $version (checksums.txt.sigstore.json) could not be downloaded: not installing."
        exit 1
    } else {
        Write-Host "Note: $version is not signed (releases before v0.5.0 were not); verifying its SHA-256 checksum only."
    }
} else {
    Write-Host "Note: cosign is not installed, so only the SHA-256 checksum is verified (install cosign to verify the release signature too)."
}
$expected = $null
foreach ($line in Get-Content "$tmp\checksums.txt") {
    $f = $line -split "\s+"
    if ($f.Count -eq 2 -and $f[1].TrimStart("*") -eq "$name.zip") { $expected = $f[0] }
}
if (-not $expected) {
    Write-Error "checksums.txt has no entry for $name.zip."
    exit 1
}
$actual = (Get-FileHash -Algorithm SHA256 "$tmp\$name.zip").Hash.ToLower()
if ($actual -ne $expected.ToLower()) {
    Write-Error "Checksum mismatch for $name.zip: the download is corrupt or was tampered with."
    exit 1
}
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
