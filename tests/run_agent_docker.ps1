#requires -Version 5.1
# Contract tests use fake native commands: no network, enrollment, or Docker changes.
$ErrorActionPreference = 'Stop'
$installer = Join-Path $PSScriptRoot '..\scripts\run-agent-docker.ps1'
$global:agentDockerCalls = [Collections.Generic.List[object]]::new()
$global:agentDockerScenario = ''
$global:agentDockerTokenPrompts = 0
$global:agentDockerWorkPath = $null

function docker.exe {
    process {
        $stdinValue = $_
        $commandArgs = @($args)
        $global:agentDockerCalls.Add($commandArgs)
        $global:LASTEXITCODE = 0
        switch ($commandArgs[0]) {
            'info' { 'linux/x86_64' }
            'container' { if ($global:agentDockerScenario -eq 'existing') { 'xmesh-agent' } }
            'build' { $global:agentDockerWorkPath = $commandArgs[-1] }
            'volume' { 'xmesh-agent-config' }
            'inspect' { if ($global:agentDockerScenario -eq 'crash') { 'true/1' } else { 'true/0' } }
            'run' {
                if ($commandArgs -contains 'version') { 'v0.3.7' }
                elseif ($commandArgs -contains 'enroll') {
                    if ($stdinValue -ne 'fake-enrollment-token') { throw 'Token missing from stdin.' }
                    if ($commandArgs -contains 'fake-enrollment-token') { throw 'Token leaked into argv.' }
                }
                elseif (($commandArgs[-1] -like 'if *') -and $global:agentDockerScenario -in @('reuse', 'mismatch')) {
                    $controllerUrl = 'https://xmesh.static.win7.win'
                    if ($global:agentDockerScenario -eq 'mismatch') { $controllerUrl = 'https://other.example.com' }
                    @{role='agent'; controller_url=$controllerUrl; node_id='saved-agent'; credential='fake-credential'} | ConvertTo-Json -Compress
                }
            }
        }
    }
}

function curl.exe {
    $global:LASTEXITCODE = 0
    $commandArgs = @($args)
    $outputIndex = [Array]::IndexOf($commandArgs, '--output')
    if ($outputIndex -lt 0) { return }
    $destination = $commandArgs[$outputIndex + 1]
    $global:agentDockerWorkPath = Split-Path -Parent $destination
    $archiveBytes = [Text.Encoding]::UTF8.GetBytes('fake-release-archive')
    if ($destination -like '*SHA256SUMS') {
        $hasher = [Security.Cryptography.SHA256]::Create()
        try { $hash = ([BitConverter]::ToString($hasher.ComputeHash($archiveBytes))).Replace('-', '').ToLowerInvariant() }
        finally { $hasher.Dispose() }
        if ($global:agentDockerScenario -eq 'checksum') { $hash = '0' * 64 }
        [IO.File]::WriteAllText($destination, "$hash  xmesh-v0.3.7-linux-amd64.tar.gz`n")
    } else {
        [IO.File]::WriteAllBytes($destination, $archiveBytes)
    }
}

function tar.exe {
    $global:LASTEXITCODE = 0
    [IO.File]::WriteAllText((Join-Path $args[3] 'xmesh'), 'fake-binary')
}

function Read-Host {
    param([string]$Prompt, [switch]$AsSecureString)
    $global:agentDockerTokenPrompts++
    ConvertTo-SecureString 'fake-enrollment-token' -AsPlainText -Force
}

function Start-Sleep { param([int]$Seconds) }
function Assert-Test([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw $Message }
}

foreach ($case in @('fresh', 'reuse', 'existing', 'checksum', 'mismatch', 'crash')) {
    $global:agentDockerScenario = $case
    $global:agentDockerCalls.Clear()
    $global:agentDockerTokenPrompts = 0
    $global:agentDockerWorkPath = $null
    $failure = $null
    try { & $installer }
    catch { $failure = $_.Exception.Message }

    $starts = @($global:agentDockerCalls | Where-Object { $_ -contains '--detach' })
    $enrollments = @($global:agentDockerCalls | Where-Object { $_ -contains 'enroll' })
    if ($case -in @('fresh', 'reuse', 'crash')) {
        if ($case -eq 'crash') {
            Assert-Test ($failure -like 'Agent did not start cleanly*') 'Restart loop must fail the startup check.'
        } else {
            Assert-Test (-not $failure) "Unexpected $case failure: $failure"
        }
        Assert-Test ($starts.Count -eq 1) 'Expected one background Agent container.'
        $start = $starts[0]
        $restartIndex = [Array]::IndexOf($start, '--restart')
        Assert-Test ($restartIndex -ge 0 -and $start[$restartIndex + 1] -eq 'always') 'restart=always is required.'
        Assert-Test (-not ($start -contains '--rm')) 'The deployed container must persist.'
        Assert-Test ($start -contains 'type=volume,source=xmesh-agent-config,target=/etc/xmesh,readonly') 'Config must persist in a volume.'
        $expectedEnrollments = 1
        if ($case -eq 'reuse') { $expectedEnrollments = 0 }
        Assert-Test ($enrollments.Count -eq $expectedEnrollments) 'Saved identity must bypass enrollment.'
        Assert-Test ($global:agentDockerTokenPrompts -eq $expectedEnrollments) 'Unexpected token prompt.'
    } else {
        Assert-Test ([bool]$failure) "Expected $case to fail."
        Assert-Test ($starts.Count -eq 0 -and $enrollments.Count -eq 0) 'Validation failure must prevent enrollment/start.'
        $expectedMessage = switch ($case) {
            'existing' { 'Container xmesh-agent already exists*' }
            'checksum' { 'SHA-256 mismatch*' }
            'mismatch' { 'Saved identity*does not match*' }
        }
        Assert-Test ($failure -like $expectedMessage) "Wrong $case failure: $failure"
    }
    Assert-Test (@($global:agentDockerCalls | Where-Object { $_[0] -eq 'rm' -or $_[0] -eq 'image' }).Count -eq 0) 'Do not remove deployed resources.'
    if ($global:agentDockerWorkPath) {
        Assert-Test (-not (Test-Path -LiteralPath $global:agentDockerWorkPath)) 'Build/download files must be cleaned up.'
    }
    Write-Host "${case}: PASS"
}
