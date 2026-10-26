# Builds the Windows binaries and the setup executable into dist\.
#   powershell -ExecutionPolicy Bypass -File scripts\build-windows.ps1 [-Version 0.1.0]
param([string]$Version)

$ErrorActionPreference = 'Stop'
Set-Location (Split-Path -Parent $PSScriptRoot)

function Invoke-Checked {
    param([string]$Exe, [string[]]$Arguments)
    & $Exe @Arguments
    if ($LASTEXITCODE -ne 0) { throw "$Exe failed with exit code $LASTEXITCODE" }
}

if (-not $Version) {
    $described = (git describe --tags --always 2>$null)
    if ($described -match '^v?(\d+\.\d+\.\d+.*)$') { $Version = $Matches[1] } else { $Version = '0.1.0-dev' }
}
$numeric = ([regex]::Match($Version, '^\d+\.\d+\.\d+')).Value
if (-not $numeric) { throw "Version '$Version' is not x.y.z" }
Write-Host "Version $Version"

# WinFsp: pinned release, verified by hash.
$winfspVersion = '2.1.25156'
$winfspUrl = 'https://github.com/winfsp/winfsp/releases/download/v2.1/winfsp-2.1.25156.msi'
$winfspSha256 = '073A70E00F77423E34BED98B86E600DEF93393BA5822204FAC57A29324DB9F7A'
$winfspMsi = "build\cache\winfsp-$winfspVersion.msi"

New-Item -ItemType Directory -Force build\cache, dist | Out-Null
if (-not (Test-Path $winfspMsi)) {
    Write-Host "Downloading WinFsp $winfspVersion"
    Invoke-WebRequest -Uri $winfspUrl -OutFile $winfspMsi -UseBasicParsing
}
if ((Get-FileHash $winfspMsi -Algorithm SHA256).Hash -ne $winfspSha256) {
    throw "WinFsp installer hash mismatch: $winfspMsi"
}

# License texts of everything compiled in, shipped with the app.
Invoke-Checked go @('run', './tools/notices', '-o', 'build\THIRD-PARTY-NOTICES.txt')

# Icons, version info and manifest for the executables and the installer.
Invoke-Checked go @('run', './tools/mkwinres', '-version', $Version, '-assets', 'build')

# rclone reports this in its User-Agent and our log; without it, "-DEV".
$rcloneVersion = (go list -m -f '{{.Version}}' github.com/rclone/rclone).Trim()
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
Invoke-Checked go @('build', '-tags', 'cmount', '-trimpath',
    '-ldflags', "-s -w -H windowsgui -X main.Version=$Version -X github.com/rclone/rclone/fs.Version=$rcloneVersion",
    '-o', 'dist\proto-easy-mount-app.exe', './cmd/proto-easy-mount-app')
Invoke-Checked go @('build', '-tags', 'cmount', '-trimpath',
    '-ldflags', "-s -w -X github.com/protomothis/proto-easy-mount/internal/cli.Version=$Version -X github.com/rclone/rclone/fs.Version=$rcloneVersion",
    '-o', 'dist\proto-easy-mount.exe', './cmd/proto-easy-mount')

$iscc = $null
$found = Get-Command iscc.exe -ErrorAction SilentlyContinue
if ($found) { $iscc = $found.Source }
foreach ($candidate in @("$env:LOCALAPPDATA\Programs\Inno Setup 6\ISCC.exe", "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe", "$env:ProgramFiles\Inno Setup 6\ISCC.exe")) {
    if (-not $iscc -and (Test-Path $candidate)) { $iscc = $candidate }
}
if (-not $iscc) { throw 'Inno Setup 6 not found (winget install JRSoftware.InnoSetup)' }

$wizardImages = (Get-Content build\wizard-small.txt -Raw).Trim().Split(',') | ForEach-Object { "..\build\$_" }
Invoke-Checked $iscc @('/Qp',
    "/DAppVersion=$Version",
    "/DAppVersionNumeric=$numeric.0",
    "/DWinFspMsi=..\$winfspMsi",
    "/DWizardSmallImages=$($wizardImages -join ',')",
    'installer\proto-easy-mount.iss')

Get-ChildItem dist | Format-Table Name, Length
