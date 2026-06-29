$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$logDir = Join-Path $repoRoot '.codex\run-logs'

New-Item -ItemType Directory -Force -Path $logDir | Out-Null

function Test-PortListening {
    param(
        [Parameter(Mandatory = $true)]
        [int]$Port
    )

    $conn = Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue | Select-Object -First 1
    return $null -ne $conn
}

function Start-BackgroundProcess {
    param(
        [Parameter(Mandatory = $true)]
        [string]$FilePath,

        [Parameter(Mandatory = $true)]
        [string[]]$ArgumentList,

        [Parameter(Mandatory = $true)]
        [string]$WorkingDirectory,

        [Parameter(Mandatory = $true)]
        [string]$StdoutPath,

        [Parameter(Mandatory = $true)]
        [string]$StderrPath
    )

    return Start-Process `
        -FilePath $FilePath `
        -ArgumentList $ArgumentList `
        -WorkingDirectory $WorkingDirectory `
        -RedirectStandardOutput $StdoutPath `
        -RedirectStandardError $StderrPath `
        -WindowStyle Hidden `
        -PassThru
}

function Wait-ForHttp {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Url,

        [int]$TimeoutSeconds = 30
    )

    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        try {
            Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec 3 | Out-Null
            return $true
        } catch {
            Start-Sleep -Milliseconds 500
        }
    }

    return $false
}

Write-Host '== Sub2API local startup ==' -ForegroundColor Cyan
Write-Host "Repo: $repoRoot"

$redisPort = 6379
$backendPort = 3000
$frontendPort = 5173

if (Test-PortListening -Port $redisPort) {
    Write-Host "Redis already listening on $redisPort"
} else {
    Write-Host "Starting dev Redis on $redisPort..."
    $redisProc = Start-BackgroundProcess `
        -FilePath 'go' `
        -ArgumentList @('run', './cmd/devredis') `
        -WorkingDirectory (Join-Path $repoRoot 'backend') `
        -StdoutPath (Join-Path $logDir 'devredis.stdout.log') `
        -StderrPath (Join-Path $logDir 'devredis.stderr.log')

    Start-Sleep -Seconds 2
    if (-not (Test-PortListening -Port $redisPort)) {
        throw "dev Redis failed to start. Check $logDir\devredis.stderr.log"
    }
    Write-Host "dev Redis started (PID $($redisProc.Id))"
}

if (Test-PortListening -Port $backendPort) {
    Write-Host "Backend already listening on $backendPort"
} else {
    Write-Host "Starting backend on $backendPort..."
    $backendProc = Start-BackgroundProcess `
        -FilePath 'go' `
        -ArgumentList @('run', './cmd/server') `
        -WorkingDirectory (Join-Path $repoRoot 'backend') `
        -StdoutPath (Join-Path $logDir 'backend.stdout.log') `
        -StderrPath (Join-Path $logDir 'backend.stderr.log')

    if (-not (Wait-ForHttp -Url "http://127.0.0.1:$backendPort/health" -TimeoutSeconds 45)) {
        throw "Backend failed to start. Check $logDir\backend.stderr.log"
    }
    Write-Host "Backend started (PID $($backendProc.Id))"
}

if (Test-PortListening -Port $frontendPort) {
    Write-Host "Frontend already listening on $frontendPort"
} else {
    Write-Host "Starting frontend on $frontendPort..."
    $frontendProc = Start-BackgroundProcess `
        -FilePath 'npm.cmd' `
        -ArgumentList @('run', 'dev', '--', '--host', '127.0.0.1', '--port', "$frontendPort") `
        -WorkingDirectory (Join-Path $repoRoot 'frontend') `
        -StdoutPath (Join-Path $logDir 'frontend.stdout.log') `
        -StderrPath (Join-Path $logDir 'frontend.stderr.log')

    if (-not (Wait-ForHttp -Url "http://127.0.0.1:$frontendPort" -TimeoutSeconds 45)) {
        throw "Frontend failed to start. Check $logDir\frontend.stderr.log"
    }
    Write-Host "Frontend started (PID $($frontendProc.Id))"
}

Write-Host ''
Write-Host 'Ready:' -ForegroundColor Green
Write-Host "  Frontend: http://127.0.0.1:$frontendPort"
Write-Host "  Backend:  http://127.0.0.1:$backendPort"
Write-Host "  Logs:     $logDir"
