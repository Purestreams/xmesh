#requires -Version 5.1
<#
.SYNOPSIS
Deploy an XMesh Agent using Docker Desktop Linux containers and restart=always.
.EXAMPLE
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\run-agent-docker.ps1
.EXAMPLE
.\scripts\run-agent-docker.ps1 -Controller https://panel.example.com -Version v0.3.7
.NOTES
Enter an Agent enrollment token from the Controller when prompted. The token is
sent through stdin, and node credentials persist in a named Docker volume.
The Agent runs in the background after this script exits. No host updater is
installed. Enable Docker Desktop startup at sign-in to resume after reboot.
To send Docker traffic directly while v2rayN TUN is enabled, import the sibling
v2rayn-docker-direct.json rule into the active routing profile, move it to the
top, and use rule mode. This affects all Docker Desktop containers. Container
network flags alone do not bypass a host TUN.
#>
[CmdletBinding()]
param(
    [string]$Controller = 'https://xmesh.static.win7.win',
    [ValidatePattern('^[a-zA-Z0-9._-]+$')]
    [string]$Version = 'v0.3.7',
    [string]$ReleaseBaseUrl = 'https://xmesh.static.win7.win/releases',
    [ValidatePattern('^[a-zA-Z0-9][a-zA-Z0-9_.-]*$')]
    [string]$ContainerName = 'xmesh-agent'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
# Check native exit codes explicitly on both Windows PowerShell and PowerShell 7.
$PSNativeCommandUseErrorActionPreference = $false

function Assert-NativeExit([string]$Action) {
    if ($LASTEXITCODE -ne 0) {
        throw "$Action failed (exit code $LASTEXITCODE)."
    }
}

function Assert-HttpsUrl([string]$Value, [string]$ParameterName) {
    $parsed = $null
    if (-not [Uri]::TryCreate($Value, [UriKind]::Absolute, [ref]$parsed) -or
        $parsed.Scheme -ne 'https' -or -not $parsed.Host -or
        $parsed.UserInfo -or $parsed.Query -or $parsed.Fragment) {
        throw "$ParameterName must be an HTTPS URL without credentials, query, or fragment."
    }
}

function Get-ReleaseFile([string]$Url, [string]$Destination) {
    & curl.exe --fail --location --retry 3 --connect-timeout 20 --max-time 600 `
        --proto '=https' --proto-redir '=https' --output $Destination $Url
    Assert-NativeExit "Download $Url"
}

Assert-HttpsUrl $Controller 'Controller'
Assert-HttpsUrl $ReleaseBaseUrl 'ReleaseBaseUrl'
$Controller = $Controller.TrimEnd('/')
$ReleaseBaseUrl = $ReleaseBaseUrl.TrimEnd('/')

foreach ($tool in @('docker.exe', 'curl.exe', 'tar.exe')) {
    if (-not (Get-Command $tool -ErrorAction SilentlyContinue)) {
        throw "Missing $tool. Install Docker Desktop and use Windows with curl.exe and tar.exe."
    }
}
$dockerInfo = & docker.exe info --format '{{.OSType}}/{{.Architecture}}'
Assert-NativeExit 'Connect to Docker Desktop; ensure Docker Desktop is running'
switch (($dockerInfo | Out-String).Trim()) {
    'linux/x86_64' { $arch = 'amd64' }
    'linux/amd64'  { $arch = 'amd64' }
    'linux/aarch64' { $arch = 'arm64' }
    'linux/arm64' { $arch = 'arm64' }
    default { throw "Docker must use Linux amd64 or arm64 containers; reported: $dockerInfo" }
}

# Refuse to replace an existing container or disrupt a running Agent.
$existingNames = @(& docker.exe container ls --all --format '{{.Names}}')
Assert-NativeExit 'Check existing containers'
if ($existingNames -contains $ContainerName) {
    throw "Container $ContainerName already exists. Use 'docker start $ContainerName' or choose another -ContainerName."
}

& curl.exe --fail --silent --show-error --connect-timeout 10 --max-time 20 `
    --proto '=https' --proto-redir '=https' "$Controller/healthz" | Out-Null
Assert-NativeExit 'Controller HTTPS health check'

$suffix = [guid]::NewGuid().ToString('N')
$volumeName = "$ContainerName-config"
$imageName = "xmesh-agent-local:$Version-$arch-$suffix"
$tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
$workDir = Join-Path $tempRoot "xmesh-agent-$suffix"
$secureToken = $null
$plainToken = $null
$tokenPointer = [IntPtr]::Zero

try {
    New-Item -ItemType Directory -Path $workDir | Out-Null
    $archiveName = "xmesh-$Version-linux-$arch.tar.gz"
    $archivePath = Join-Path $workDir $archiveName
    $checksumPath = Join-Path $workDir 'SHA256SUMS'
    $baseUrl = "$ReleaseBaseUrl/$Version"
    Get-ReleaseFile "$baseUrl/SHA256SUMS" $checksumPath
    Get-ReleaseFile "$baseUrl/$archiveName" $archivePath

    $pattern = '^([a-fA-F0-9]{64})  ' + [regex]::Escape($archiveName) + '$'
    $entries = @(Get-Content -LiteralPath $checksumPath | Where-Object { $_ -match $pattern })
    if ($entries.Count -ne 1) {
        throw "SHA256SUMS must contain exactly one checksum for $archiveName."
    }
    $expectedHash = [regex]::Match($entries[0], $pattern).Groups[1].Value
    $actualHash = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash
    if ($actualHash -ne $expectedHash) {
        throw "SHA-256 mismatch for $archiveName; refusing to run it."
    }
    Write-Host 'Release archive SHA-256 verified.'

    # Agent embeds Xray-core, so only the xmesh executable is needed.
    & tar.exe -xzf $archivePath -C $workDir xmesh
    Assert-NativeExit 'Extract xmesh'
    if (-not (Test-Path -LiteralPath (Join-Path $workDir 'xmesh') -PathType Leaf)) {
        throw 'The release archive does not contain xmesh.'
    }
    $dockerfile = @'
FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY xmesh /usr/local/bin/xmesh
RUN chmod 0755 /usr/local/bin/xmesh
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/xmesh"]
'@
    [IO.File]::WriteAllText((Join-Path $workDir 'Dockerfile'), $dockerfile.Replace("`r`n", "`n"), [Text.UTF8Encoding]::new($false))
    [IO.File]::WriteAllText((Join-Path $workDir '.dockerignore'), "*`n!Dockerfile`n!xmesh`n", [Text.UTF8Encoding]::new($false))
    & docker.exe build --platform "linux/$arch" --tag $imageName $workDir
    Assert-NativeExit 'Build Agent image'
    $binaryVersion = & docker.exe run --rm --network none $imageName version
    Assert-NativeExit 'Check Agent executable'
    if (($binaryVersion | Out-String).Trim() -ne $Version) {
        throw "Expected binary version $Version; found: $binaryVersion"
    }

    & docker.exe volume create $volumeName | Out-Null
    Assert-NativeExit 'Create persistent config volume'
    $configMount = "type=volume,source=$volumeName,target=/etc/xmesh"
    & docker.exe run --rm --network none --user 0:0 --mount $configMount `
        --entrypoint /bin/sh $imageName -c 'chown 10001:10001 /etc/xmesh && chmod 0700 /etc/xmesh'
    Assert-NativeExit 'Set config volume permissions'

    # Read only to validate a saved identity; never print its credentials.
    $savedConfig = & docker.exe run --rm --network none --mount "$configMount,readonly" `
        --entrypoint /bin/sh $imageName -c 'if [ -f /etc/xmesh/node.json ]; then cat /etc/xmesh/node.json; fi'
    Assert-NativeExit 'Check saved node identity'
    if (($savedConfig | Out-String).Trim()) {
        $identity = ($savedConfig | Out-String) | ConvertFrom-Json
        if ($identity.role -ne 'agent' -or $identity.controller_url -ne $Controller -or
            -not $identity.node_id -or -not $identity.credential) {
            throw "Saved identity in $volumeName does not match this Agent/Controller. Choose another -ContainerName."
        }
        Write-Host "Reusing saved Agent identity: $($identity.node_id)"
        $identity = $null
        $savedConfig = $null
    } else {
        $secureToken = Read-Host 'Paste the one-time Agent enrollment token' -AsSecureString
        $tokenPointer = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secureToken)
        $plainToken = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($tokenPointer).Trim()
        if (-not $plainToken -or $plainToken.Length -gt 4096) {
            throw 'Enrollment token must contain 1 to 4096 characters.'
        }
        # Pass the token only through stdin, never in argv, env, or the image.
        $enrollArgs = @(
            'run', '--rm', '--interactive', '--mount', $configMount,
            $imageName, 'enroll', '--controller', $Controller, '--role', 'agent',
            '--token-stdin', '--output', '/etc/xmesh/node.json'
        )
        $plainToken | & docker.exe @enrollArgs
        Assert-NativeExit 'Enroll Agent'
        $plainToken = $null
        [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($tokenPointer)
        $tokenPointer = [IntPtr]::Zero
        $secureToken.Dispose()
        $secureToken = $null
    }

    $runArgs = @(
        'run', '--detach', '--restart', 'always', '--name', $ContainerName,
        '--mount', "$configMount,readonly",
        '--read-only', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges',
        '--tmpfs', '/tmp:rw,noexec,nosuid,size=16m,mode=1777',
        '--log-opt', 'max-size=10m', '--log-opt', 'max-file=3',
        $imageName, 'agent', '--config', '/etc/xmesh/node.json'
    )
    & docker.exe @runArgs | Out-Null
    Assert-NativeExit 'Start Agent in the background'
    Start-Sleep -Seconds 3
    $state = & docker.exe inspect --format '{{.State.Running}}/{{.RestartCount}}' $ContainerName
    Assert-NativeExit 'Check Agent container state'
    if (($state | Out-String).Trim() -ne 'true/0') {
        & docker.exe logs --tail 50 $ContainerName | Out-Host
        throw "Agent did not start cleanly. Inspect 'docker logs $ContainerName'; config remains in $volumeName."
    }
    Write-Host "Agent is running in the background: $ContainerName (restart=always)."
    Write-Host "Persistent config: $volumeName"
    Write-Host "Logs: docker logs -f $ContainerName"
    Write-Host 'Confirm node online status and Link readiness in the Controller panel.'
}
finally {
    $plainToken = $null
    if ($tokenPointer -ne [IntPtr]::Zero) {
        [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($tokenPointer)
    }
    if ($null -ne $secureToken) { $secureToken.Dispose() }
    # Keep the container, image and config volume; remove only download/build files.
    if (Test-Path -LiteralPath $workDir) {
        $resolvedWork = (Resolve-Path -LiteralPath $workDir).Path
        $expectedParent = $tempRoot.TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar
        if (-not $resolvedWork.StartsWith($expectedParent, [StringComparison]::OrdinalIgnoreCase) -or
            [IO.Path]::GetFileName($resolvedWork) -ne "xmesh-agent-$suffix") {
            throw "Refusing to remove unexpected temporary path: $resolvedWork"
        }
        Remove-Item -LiteralPath $resolvedWork -Recurse -Force
    }
}
