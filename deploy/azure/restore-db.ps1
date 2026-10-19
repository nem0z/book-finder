<#
.SYNOPSIS
Copies the local database (docker compose service "db") into the Azure PostgreSQL server. See docs/azure.md.

.DESCRIPTION
The Azure server has no public address, so the copy runs inside the private network:
1. pg_dump of the local database (about 730 MB).
2. An image with the dump is built in the container registry (the dump is uploaded once).
3. A one-off Container Apps job restores it, then prints the number of works and vectors.
4. The job and the image are deleted.
Replaces whatever a previous restore put in the database.

.EXAMPLE
$env:BOOKFINDER_DB_PASSWORD = '<same password as for deploy.ps1>'
./deploy/azure/restore-db.ps1
#>
param(
    [string]$ResourceGroup = 'bookfinder-rg',
    # Local Postgres container (docker compose).
    [string]$LocalContainer = 'bf-analysis-db-1'
)

$ErrorActionPreference = 'Stop'

function Invoke-Az {
    $out = az @args
    if ($LASTEXITCODE -ne 0) { throw "az $($args[0..1] -join ' ') failed (exit code $LASTEXITCODE)" }
    $out
}

$password = $env:BOOKFINDER_DB_PASSWORD
if (-not $password) { throw 'Set BOOKFINDER_DB_PASSWORD to the database password used by deploy.ps1.' }

$registry = Invoke-Az acr list -g $ResourceGroup --query '[0].name' -o tsv
$server = Invoke-Az postgres flexible-server list -g $ResourceGroup --query '[0].fullyQualifiedDomainName' -o tsv
$environment = Invoke-Az containerapp env list -g $ResourceGroup --query '[0].name' -o tsv
$identity = Invoke-Az identity show -g $ResourceGroup -n bookfinder-apps --query id -o tsv
$tag = Get-Date -Format 'yyyyMMddHHmm'
$image = "$registry.azurecr.io/bookfinder-restore:$tag"
$job = 'restore-db'

$context = Join-Path ([IO.Path]::GetTempPath()) "bookfinder-restore-$tag"
New-Item -ItemType Directory $context | Out-Null
try {
    Write-Host 'Dumping the local database...'
    docker exec $LocalContainer pg_dump -U bookfinder -d bookfinder -Fc -Z 6 -f /tmp/bookfinder.dump
    if ($LASTEXITCODE -ne 0) { throw 'pg_dump failed' }
    docker cp "$LocalContainer`:/tmp/bookfinder.dump" "$context/bookfinder.dump"
    docker exec $LocalContainer rm /tmp/bookfinder.dump
    Copy-Item "$PSScriptRoot/restore/Dockerfile" $context

    Write-Host "Uploading the dump and building $image..."
    Invoke-Az acr build -r $registry -t "bookfinder-restore:$tag" $context --no-logs -o none
}
finally {
    Remove-Item -Recurse -Force $context
}

Write-Host 'Restoring inside the private network...'
Invoke-Az containerapp job create -g $ResourceGroup -n $job --environment $environment `
    --trigger-type Manual --replica-timeout 7200 --replica-retry-limit 0 --parallelism 1 --replica-completion-count 1 `
    --image $image --registry-server "$registry.azurecr.io" --registry-identity $identity --mi-user-assigned $identity `
    --cpu 1 --memory 2Gi --secrets "pgpassword=$password" `
    --env-vars PGHOST=$server PGUSER=bookfinder PGDATABASE=bookfinder PGSSLMODE=require 'PGPASSWORD=secretref:pgpassword' `
    'PGOPTIONS=-c maintenance_work_mem=512MB' -o none
try {
    $execution = Invoke-Az containerapp job start -g $ResourceGroup -n $job --query name -o tsv
    do {
        Start-Sleep 30
        $status = Invoke-Az containerapp job execution show -g $ResourceGroup -n $job --job-execution-name $execution --query properties.status -o tsv
        Write-Host "  $status"
    } while ($status -in 'Running', 'Processing')
    if ($status -ne 'Succeeded') {
        throw "Restore $status. Logs: Log Analytics workspace bookfinder-logs, table ContainerAppConsoleLogs_CL, job $job."
    }
    Write-Host 'Restored. The job printed the number of works and vectors in its logs.'
}
finally {
    Invoke-Az containerapp job delete -g $ResourceGroup -n $job --yes -o none
    Invoke-Az acr repository delete -n $registry --image "bookfinder-restore:$tag" --yes -o none
}
