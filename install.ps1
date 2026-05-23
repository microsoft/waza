$ErrorActionPreference = 'Stop'

# install.ps1 - Download and install the latest native Windows waza binary.
# Usage: irm https://raw.githubusercontent.com/microsoft/waza/main/install.ps1 | iex

$Repo = 'microsoft/waza'
$BinaryName = 'waza'
$TempDir = Join-Path ([System.IO.Path]::GetTempPath()) ("waza-install-{0}" -f ([System.Guid]::NewGuid()))
$Scheduled = $false

function Get-WazaArchitecture {
    $arch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
    switch -Regex ($arch) {
        'ARM64' { return 'arm64' }
        'AMD64|IA64|x64' { return 'amd64' }
        default { throw "Unsupported architecture: $arch" }
    }
}

function Get-InstallDirectory {
    if ($env:WAZA_INSTALL_DIR) {
        New-Item -ItemType Directory -Path $env:WAZA_INSTALL_DIR -Force | Out-Null
        return $env:WAZA_INSTALL_DIR
    }

    $existing = Get-Command "$BinaryName.exe" -ErrorAction SilentlyContinue
    if ($existing -and $existing.Source) {
        return Split-Path -Parent $existing.Source
    }

    $dir = Join-Path $env:LOCALAPPDATA 'Microsoft\Waza'
    New-Item -ItemType Directory -Path $dir -Force | Out-Null
    return $dir
}

function Get-LatestReleaseTag {
    $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" -Headers @{ 'User-Agent' = 'waza-installer' }
    if (-not $release -or -not ($release.tag_name -like 'v*')) {
        throw 'Could not determine latest release.'
    }
    return $release.tag_name
}

function ConvertTo-SingleQuotedPowerShellLiteral([string] $Value) {
    return "'" + $Value.Replace("'", "''") + "'"
}

function Install-Binary {
    param(
        [string] $Source,
        [string] $Destination,
        [string] $Version,
        [string] $ParentPid
    )

    if ($ParentPid) {
        $script:Scheduled = $true
        $sourceLiteral = ConvertTo-SingleQuotedPowerShellLiteral $Source
        $destinationLiteral = ConvertTo-SingleQuotedPowerShellLiteral $Destination
        $tempDirLiteral = ConvertTo-SingleQuotedPowerShellLiteral $TempDir
        $versionLiteral = ConvertTo-SingleQuotedPowerShellLiteral $Version
        $moveScript = @"
`$ErrorActionPreference = 'Stop'
while (Get-Process -Id $ParentPid -ErrorAction SilentlyContinue) {
    Start-Sleep -Milliseconds 250
}
Move-Item -LiteralPath $sourceLiteral -Destination $destinationLiteral -Force
Remove-Item -LiteralPath $tempDirLiteral -Recurse -Force -ErrorAction SilentlyContinue
Write-Host ''
Write-Host "Installed waza $versionLiteral to $destinationLiteral"
"@
        $encoded = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($moveScript))
        $powerShellPath = (Get-Process -Id $PID).Path
        Start-Process -FilePath $powerShellPath -ArgumentList @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-EncodedCommand', $encoded) | Out-Null
        Write-Host 'Update scheduled. It will finish after the current waza process exits.'
        return
    }

    Move-Item -LiteralPath $Source -Destination $Destination -Force
    Write-Host ''
    Write-Host "Installed waza $Version to $Destination"
}

try {
    if (-not $IsWindows -and $PSVersionTable.PSEdition -eq 'Core') {
        throw 'install.ps1 is only for native Windows. Use install.sh on macOS, Linux, or WSL.'
    }

    $arch = Get-WazaArchitecture
    $os = 'windows'
    Write-Host "Detected platform: $os/$arch"

    $tag = Get-LatestReleaseTag
    $version = $tag.TrimStart('v')
    Write-Host "Latest version: $version ($tag)"

    $assetName = "$BinaryName-$os-$arch.exe"
    New-Item -ItemType Directory -Path $TempDir -Force | Out-Null
    $assetPath = Join-Path $TempDir $assetName
    $checksumsPath = Join-Path $TempDir 'checksums.txt'

    Write-Host "Downloading $assetName..."
    Invoke-WebRequest -Uri "https://github.com/$Repo/releases/download/$tag/$assetName" -OutFile $assetPath -UseBasicParsing

    Write-Host 'Downloading checksums...'
    Invoke-WebRequest -Uri "https://github.com/$Repo/releases/download/$tag/checksums.txt" -OutFile $checksumsPath -UseBasicParsing

    Write-Host 'Verifying checksum...'
    $checksumLine = Get-Content $checksumsPath | Where-Object { $_ -match [Regex]::Escape($assetName) } | Select-Object -First 1
    if (-not $checksumLine) {
        throw "No checksum entry found for $assetName."
    }
    $expectedHash = ($checksumLine -split '\s+')[0].ToUpperInvariant()
    $actualHash = (Get-FileHash -Algorithm SHA256 -Path $assetPath).Hash.ToUpperInvariant()
    if ($actualHash -ne $expectedHash) {
        throw "Checksum mismatch for $assetName."
    }
    Write-Host 'Checksum verified.'

    $installDir = Get-InstallDirectory
    $destination = Join-Path $installDir "$BinaryName.exe"
    Install-Binary -Source $assetPath -Destination $destination -Version $version -ParentPid $env:WAZA_UPDATE_PARENT_PID

    $existingCommand = Get-Command "$BinaryName.exe" -ErrorAction SilentlyContinue
    $existingDir = if ($existingCommand -and $existingCommand.Source) { Split-Path -Parent $existingCommand.Source } else { $null }
    if ($installDir -ne $existingDir) {
        Write-Host "Note: Add $installDir to your PATH if waza is not found after installation."
    }
}
finally {
    if (-not $Scheduled -and (Test-Path $TempDir)) {
        Remove-Item -LiteralPath $TempDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}
