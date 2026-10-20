<#
.SYNOPSIS
Deploys Book Finder to Azure: infrastructure, images, then the apps. See docs/azure.md.

.DESCRIPTION
1. Creates or updates the infrastructure (deploy/azure/main.bicep) without the apps.
2. Builds the images in the container registry (`az acr build`), so they are not uploaded from this machine.
3. Deploys the apps with those images.
Safe to run again: Azure only changes what differs.

.EXAMPLE
$env:BOOKFINDER_DB_PASSWORD = '<letters and digits>'
./deploy/azure/deploy.ps1
#>
param(
    [string]$ResourceGroup = 'bookfinder-rg',
    [string]$Location = 'francecentral',
    # Who re-ranks: Voyage AI's API (its key in the key vault, see docs/azure.md), or the local model in a reranker
    # app on CPU or on a serverless T4 GPU (the T4 scores differently).
    [ValidateSet('voyage', 'cpu', 'gpu')]
    [string]$Reranker = 'voyage',
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

$common = @('-g', $ResourceGroup, '-f', $template, '-p', "postgresPassword=$password", "reranker=$Reranker")

Write-Host "Infrastructure in $ResourceGroup ($Location)..."
Invoke-Az group create -n $ResourceGroup -l $Location -o none

# Switching re-rankers: the template doesn't remove a reranker app that is no longer needed, and Azure refuses to
# change the environment's workload profiles while an app uses one, so the old reranker app goes first. The apps
# step below recreates it when it is still needed.
$rerankerProfile = az containerapp show -g $ResourceGroup -n reranker --query properties.workloadProfileName -o tsv 2>$null
$wantedProfile = @{ voyage = ''; cpu = 'Consumption'; gpu = 'gpu-t4' }[$Reranker]
if ($LASTEXITCODE -eq 0 -and $rerankerProfile -and $rerankerProfile -ne $wantedProfile) {
    Write-Host "Removing the re-ranker from $rerankerProfile..."
    Invoke-Az containerapp delete -g $ResourceGroup -n reranker --yes -o none
}

$outputs = Invoke-Az deployment group create -n main @common -p deployApps=false --query properties.outputs -o json | ConvertFrom-Json
$registry = $outputs.registryName.value

# The Voyage AI key is not part of the deployment: you put it in the key vault yourself, once.
if ($Reranker -eq 'voyage') {
    $vault = $outputs.vaultName.value
    $secret = $outputs.voyageSecretName.value
    az keyvault secret show --vault-name $vault -n $secret --query id -o tsv 2>$null | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw @"
The Voyage AI key is not in the key vault yet. Put it there (it asks for the key without showing it), then run this
script again:
  `$key = Read-Host 'Voyage API key' -AsSecureString
  az keyvault secret set --vault-name $vault -n $secret -o none --value ([Net.NetworkCredential]::new('', `$key).Password)
"@
    }
}

if (-not $SkipImages) {
    $images = [ordered]@{
        'bookfinder-api'    = @{ Context = $root; Args = @() }
        'bookfinder-web'    = @{ Context = "$root/web"; Args = @() }
        'bookfinder-ollama' = @{ Context = "$root/deploy/ollama"; Args = @() }
    }
    if ($Reranker -ne 'voyage') {
        $teiTag = if ($Reranker -eq 'gpu') { 'turing-1.9' } else { 'cpu-1.9' }
        $images['bookfinder-reranker'] = @{ Context = "$root/deploy/reranker"; Args = @('--build-arg', "TEI_TAG=$teiTag") }
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
