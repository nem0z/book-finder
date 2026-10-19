<#
.SYNOPSIS
Deploys Book Finder to Azure: infrastructure, images, then the apps. See docs/azure.md.

.DESCRIPTION
1. Creates or updates the infrastructure (deploy/azure/main.bicep) without the apps.
2. Builds the four images in the container registry (`az acr build`), so they are not uploaded from this machine.
3. Deploys the apps with those images.
Safe to run again: Azure only changes what differs.

.EXAMPLE
$env:BOOKFINDER_DB_PASSWORD = '<letters and digits>'
./deploy/azure/deploy.ps1
#>
param(
    [string]$ResourceGroup = 'bookfinder-rg',
    [string]$Location = 'francecentral',
    # Run the re-ranker on a serverless T4 GPU instead of CPU (scores differ from the local CPU build, see docs/azure.md).
    [bool]$RerankerGpu = $false,
    # Image tag: the current commit by default.
    [string]$Tag = (git rev-parse --short HEAD),
    # Redeploy the apps without rebuilding the images (they must exist with this tag).
    [switch]$SkipImages
)

$ErrorActionPreference = 'Stop'
$root = Resolve-Path "$PSScriptRoot/../.."
$template = Join-Path $PSScriptRoot 'main.bicep'

# Runs az and stops the script when it fails.
function Invoke-Az {
    $out = az @args
    if ($LASTEXITCODE -ne 0) { throw "az $($args[0..1] -join ' ') failed (exit code $LASTEXITCODE)" }
    $out
}

$password = $env:BOOKFINDER_DB_PASSWORD
if ($password -notmatch '^[A-Za-z0-9]{16,}$') {
    throw 'Set BOOKFINDER_DB_PASSWORD to the database password: at least 16 letters and digits. Keep it: every deployment needs the same one.'
}

$common = @('-g', $ResourceGroup, '-f', $template, '-p', "postgresPassword=$password", "rerankerGpu=$($RerankerGpu.ToString().ToLower())")

Write-Host "Infrastructure in $ResourceGroup ($Location)..."
Invoke-Az group create -n $ResourceGroup -l $Location -o none

# Switching the re-ranker between GPU and CPU: Azure refuses to change the environment's workload profiles while an
# app uses one, so the old re-ranker goes first. The apps step below recreates it.
$rerankerProfile = az containerapp show -g $ResourceGroup -n reranker --query properties.workloadProfileName -o tsv 2>$null
$wantedProfile = if ($RerankerGpu) { 'gpu-t4' } else { 'Consumption' }
if ($LASTEXITCODE -eq 0 -and $rerankerProfile -and $rerankerProfile -ne $wantedProfile) {
    Write-Host "Removing the re-ranker from $rerankerProfile..."
    Invoke-Az containerapp delete -g $ResourceGroup -n reranker --yes -o none
}

$outputs = Invoke-Az deployment group create -n main @common -p deployApps=false --query properties.outputs -o json | ConvertFrom-Json
$registry = $outputs.registryName.value

if (-not $SkipImages) {
    $teiTag = if ($RerankerGpu) { 'turing-1.9' } else { 'cpu-1.9' }
    $images = [ordered]@{
        'bookfinder-api'      = @{ Context = $root; Args = @() }
        'bookfinder-web'      = @{ Context = "$root/web"; Args = @() }
        'bookfinder-ollama'   = @{ Context = "$root/deploy/ollama"; Args = @() }
        'bookfinder-reranker' = @{ Context = "$root/deploy/reranker"; Args = @('--build-arg', "TEI_TAG=$teiTag") }
    }
    foreach ($name in $images.Keys) {
        Write-Host "Building $name`:$Tag in $registry..."
        $i = $images[$name]
        Invoke-Az acr build -r $registry -t "$name`:$Tag" @($i.Args) $i.Context --no-logs -o none
    }
}

Write-Host 'Apps...'
$outputs = Invoke-Az deployment group create -n main @common -p deployApps=true imageTag=$Tag --query properties.outputs -o json | ConvertFrom-Json
Write-Host "Deployed ${Tag}: $($outputs.url.value)"
