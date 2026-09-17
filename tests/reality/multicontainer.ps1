param(
    [switch]$SkipBuild
)

$ErrorActionPreference = 'Stop'
$repoRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..\..')).Path
$suffix = [guid]::NewGuid().ToString('N').Substring(0, 10)
$networkName = "xmesh-reality-$suffix"
$fixtureDir = Join-Path $repoRoot "tmp\reality-multi-$suffix"
$imageName = 'xmesh-reality-deployment:local'
$containerIds = [System.Collections.Generic.List[string]]::new()
$networkCreated = $false
$passed = $false

function Assert-LastExit([string]$action) {
    if ($LASTEXITCODE -ne 0) { throw "$action failed with exit code $LASTEXITCODE" }
}

function Start-TestContainer([string]$role, [string[]]$options, [string[]]$commandArgs) {
    $containerName = "$networkName-$role"
    $id = & docker run -d --name $containerName --network $networkName --network-alias $role @options $imageName @commandArgs
    Assert-LastExit "start $role"
    $containerIds.Add(($id | Select-Object -Last 1).Trim())
    Write-Host "$role container: $containerName"
}

try {
    New-Item -ItemType Directory -Path $fixtureDir -Force | Out-Null
    if (-not $SkipBuild) {
        & docker build -f (Join-Path $repoRoot 'tests\reality\deployment.Dockerfile') -t $imageName $repoRoot
        Assert-LastExit 'build deployment image'
    }
    & docker run --rm --entrypoint /usr/local/bin/deploycheck --mount "type=bind,source=$fixtureDir,target=/fixture" $imageName setup /fixture
    Assert-LastExit 'generate isolated test configuration'
    New-Item -ItemType Directory -Path (Join-Path $fixtureDir 'gateway-data') -Force | Out-Null
    New-Item -ItemType Directory -Path (Join-Path $fixtureDir 'gateway2-data') -Force | Out-Null

    & docker network create $networkName | Out-Null
    Assert-LastExit 'create isolated Docker network'
    $networkCreated = $true

    Start-TestContainer 'target' @('--entrypoint', '/usr/local/bin/deploycheck') @('target')
    Start-TestContainer 'controller' @(
        '--mount', "type=bind,source=$(Join-Path $fixtureDir 'controller.json'),target=/etc/xmesh/controller.json,readonly",
        '--mount', "type=bind,source=$(Join-Path $fixtureDir 'controller-data'),target=/var/lib/xmesh"
    ) @('controller', '-config', '/etc/xmesh/controller.json')
    & docker run --rm --network $networkName --entrypoint /usr/local/bin/deploycheck $imageName health
    Assert-LastExit 'Controller health'

    Start-TestContainer 'gateway' @(
        '--mount', "type=bind,source=$(Join-Path $fixtureDir 'gateway.json'),target=/etc/xmesh/node.json,readonly",
        '--mount', "type=bind,source=$(Join-Path $fixtureDir 'gateway-data'),target=/var/lib/xmesh"
    ) @('gateway', '-config', '/etc/xmesh/node.json')
    Start-TestContainer 'gateway2' @(
        '--mount', "type=bind,source=$(Join-Path $fixtureDir 'gateway2.json'),target=/etc/xmesh/node.json,readonly",
        '--mount', "type=bind,source=$(Join-Path $fixtureDir 'gateway2-data'),target=/var/lib/xmesh"
    ) @('gateway', '-config', '/etc/xmesh/node.json')
    Start-TestContainer 'agent' @(
        '--mount', "type=bind,source=$(Join-Path $fixtureDir 'agent.json'),target=/etc/xmesh/node.json,readonly"
    ) @('agent', '-config', '/etc/xmesh/node.json')
    Start-TestContainer 'client' @(
        '--entrypoint', '/usr/local/lib/xmesh/xray',
        '--mount', "type=bind,source=$(Join-Path $fixtureDir 'client-xray.json'),target=/etc/xmesh/client-xray.json,readonly"
    ) @('run', '-config', '/etc/xmesh/client-xray.json')
    Start-TestContainer 'client2' @(
        '--entrypoint', '/usr/local/lib/xmesh/xray',
        '--mount', "type=bind,source=$(Join-Path $fixtureDir 'client2-xray.json'),target=/etc/xmesh/client-xray.json,readonly"
    ) @('run', '-config', '/etc/xmesh/client-xray.json')

    & docker run --rm --network $networkName --entrypoint /usr/local/bin/deploycheck $imageName probe
    Assert-LastExit 'multi-container TCP/UDP probe'
    & docker run --rm --entrypoint /usr/local/bin/deploycheck --mount "type=bind,source=$(Join-Path $fixtureDir 'controller-data'),target=/state,readonly" $imageName verify-usage /state/controller-state.json
    Assert-LastExit 'Gateway Link and user usage reporting'
    $passed = $true
    Write-Host 'Two-Gateway one-Agent REALITY deployment: PASS'
}
finally {
    if (-not $passed) {
        foreach ($id in $containerIds) {
            Write-Host "Container log: $id"
            & docker logs --tail 80 $id 2>&1 | Out-Host
        }
    }
    foreach ($id in $containerIds) { & docker rm -f $id | Out-Null }
    if ($networkCreated) { & docker network rm $networkName | Out-Null }
    if ($passed) {
        $tmpRoot = (Resolve-Path -LiteralPath (Join-Path $repoRoot 'tmp')).Path
        $resolvedFixture = (Resolve-Path -LiteralPath $fixtureDir).Path
        if (-not $resolvedFixture.StartsWith($tmpRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
            throw "refusing to remove unexpected fixture path: $resolvedFixture"
        }
        Remove-Item -LiteralPath $resolvedFixture -Recurse -Force
    } else {
        Write-Host "Retained test configuration for debugging: $fixtureDir"
    }
}
