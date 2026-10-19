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
    # Run the re-ranker on a serverless T4 GPU; $false runs it on CPU.
    [bool]$RerankerGpu = $true,
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
