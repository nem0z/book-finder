// Book Finder on Azure: container apps (web, api, ollama, and the reranker unless Voyage AI re-ranks) in a private
// network, with a managed PostgreSQL server that has no public address. Only the web app is reachable from the
// internet. See docs/azure.md.

@description('Azure region; defaults to the resource group\'s (France Central in docs/azure.md). A serverless T4 re-ranker needs a region that offers it.')
param location string = resourceGroup().location

@description('Prefix for resource names.')
@minLength(3)
@maxLength(12)
param prefix string = 'bookfinder'

@description('Password of the PostgreSQL administrator. Letters and digits only, since it goes into a connection URL.')
@secure()
param postgresPassword string

@description('Deploy the container apps. Set to false on the first run, before the images are in the registry.')
param deployApps bool = true

@description('Tag of the images in the registry.')
param imageTag string = 'latest'

@description('Who re-ranks the search results: voyage (Voyage AI\'s API, with the key in the key vault, see docs/azure.md), or the local model in a reranker app on cpu or on a T4 gpu (the T4 scores differently).')
@allowed(['voyage', 'cpu', 'gpu'])
param reranker string = 'voyage'

@description('GitHub Copilot model that reads search queries before the vector search, billed to the GitHub account of the token in the key vault (see docs/azure.md); empty to search with the raw query.')
param queryModel string = 'claude-haiku-5.5'

@description('Minimum replicas of web and api. 1 keeps them running (a few dollars a month each), 0 lets them stop when idle.')
@allowed([0, 1])
param webApiMinReplicas int = 0

@description('Seconds without requests before ollama and the re-ranker stop.')
param modelCooldownSeconds int = 1800

var suffix = uniqueString(resourceGroup().id)
var dbName = 'bookfinder'
var dbUser = 'bookfinder'
var gpuProfile = 'gpu-t4'
var rerankerGpu = reranker == 'gpu'
var useVoyage = reranker == 'voyage'
var acrPullRole = subscriptionResourceId('Microsoft.Authorization/roleDefinitions', '7f951dda-4ed3-4680-a7ca-43fe172d538d')
var secretsUserRole = subscriptionResourceId('Microsoft.Authorization/roleDefinitions', '4633458b-17de-408a-b874-0445c86b69e6')
var secretsOfficerRole = subscriptionResourceId('Microsoft.Authorization/roleDefinitions', 'b86a8fe4-44ce-4948-aee5-eccb2c155cd7')
var voyageSecret = 'voyage-api-key'
var useQueryModel = !empty(queryModel)
var copilotSecret = 'copilot-github-token'

resource logs 'Microsoft.OperationalInsights/workspaces@2023-09-01' = {
  name: '${prefix}-logs'
  location: location
  properties: {
    sku: { name: 'PerGB2018' }
    retentionInDays: 30
  }
}

// The apps pull their images with this identity: no registry passwords.
resource identity 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' = {
  name: '${prefix}-apps'
  location: location
}

resource registry 'Microsoft.ContainerRegistry/registries@2023-07-01' = {
  name: '${prefix}${suffix}'
  location: location
  sku: { name: 'Basic' }
  properties: { adminUserEnabled: false }
}

resource registryPull 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(registry.id, identity.id, acrPullRole)
  scope: registry
  properties: {
    roleDefinitionId: acrPullRole
    principalId: identity.properties.principalId
    principalType: 'ServicePrincipal'
  }
}

// Holds the Voyage AI API key, which you set yourself (docs/azure.md), so it never goes through a deployment. The
// apps read it with their identity; whoever deploys can write it.
resource vault 'Microsoft.KeyVault/vaults@2023-07-01' = {
  name: '${take(prefix, 8)}kv${suffix}'
  location: location
  properties: {
    tenantId: subscription().tenantId
    sku: { family: 'A', name: 'standard' }
    enableRbacAuthorization: true
  }
}

resource vaultRead 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(vault.id, identity.id, secretsUserRole)
  scope: vault
  properties: {
    roleDefinitionId: secretsUserRole
    principalId: identity.properties.principalId
    principalType: 'ServicePrincipal'
  }
}

resource vaultWrite 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(vault.id, deployer().objectId, secretsOfficerRole)
  scope: vault
  properties: {
    roleDefinitionId: secretsOfficerRole
    principalId: deployer().objectId
  }
}

resource vnet 'Microsoft.Network/virtualNetworks@2024-05-01' = {
  name: '${prefix}-vnet'
  location: location
  properties: {
    addressSpace: { addressPrefixes: ['10.20.0.0/16'] }
    subnets: [
      {
        name: 'apps'
        properties: {
          addressPrefix: '10.20.0.0/23'
          delegations: [{ name: 'apps', properties: { serviceName: 'Microsoft.App/environments' } }]
        }
      }
      {
        name: 'db'
        properties: {
          addressPrefix: '10.20.2.0/28'
          delegations: [{ name: 'db', properties: { serviceName: 'Microsoft.DBforPostgreSQL/flexibleServers' } }]
        }
      }
    ]
  }
}

// Resolves the database's private address inside the network.
resource dbDns 'Microsoft.Network/privateDnsZones@2024-06-01' = {
  name: '${prefix}.private.postgres.database.azure.com'
  location: 'global'
}

resource dbDnsLink 'Microsoft.Network/privateDnsZones/virtualNetworkLinks@2024-06-01' = {
  parent: dbDns
  name: 'vnet'
  location: 'global'
  properties: {
    virtualNetwork: { id: vnet.id }
    registrationEnabled: false
  }
}

resource postgres 'Microsoft.DBforPostgreSQL/flexibleServers@2025-08-01' = {
  name: '${prefix}-db-${suffix}'
  location: location
  sku: { name: 'Standard_B1ms', tier: 'Burstable' }
  properties: {
    version: '17'
    administratorLogin: dbUser
    administratorLoginPassword: postgresPassword
    storage: { storageSizeGB: 32, autoGrow: 'Enabled' }
    backup: { backupRetentionDays: 7, geoRedundantBackup: 'Disabled' }
    highAvailability: { mode: 'Disabled' }
    network: {
      delegatedSubnetResourceId: vnet.properties.subnets[1].id
      privateDnsZoneArmResourceId: dbDns.id
      publicNetworkAccess: 'Disabled'
    }
  }
  dependsOn: [dbDnsLink]
}

// Extensions the schema uses must be allowed first.
resource extensions 'Microsoft.DBforPostgreSQL/flexibleServers/configurations@2025-08-01' = {
  parent: postgres
  name: 'azure.extensions'
  properties: { value: 'VECTOR,PG_TRGM', source: 'user-override' }
}

resource database 'Microsoft.DBforPostgreSQL/flexibleServers/databases@2025-08-01' = {
  parent: postgres
  name: dbName
  properties: { charset: 'UTF8', collation: 'en_US.utf8' }
  // The server accepts one change at a time.
  dependsOn: [extensions]
}

resource environment 'Microsoft.App/managedEnvironments@2026-01-01' = {
  name: '${prefix}-env'
  location: location
  properties: {
    appLogsConfiguration: {
      destination: 'log-analytics'
      logAnalyticsConfiguration: {
        customerId: logs.properties.customerId
        sharedKey: logs.listKeys().primarySharedKey
      }
    }
    vnetConfiguration: {
      infrastructureSubnetId: vnet.properties.subnets[0].id
      internal: false
    }
    workloadProfiles: concat(
      [{ name: 'Consumption', workloadProfileType: 'Consumption' }],
      rerankerGpu ? [{ name: gpuProfile, workloadProfileType: 'Consumption-GPU-NC8as-T4' }] : []
    )
  }
}

var registryServer = registry.properties.loginServer

module ollama 'app.bicep' = if (deployApps) {
  name: 'app-ollama'
  params: {
    location: location
    environmentId: environment.id
    identityId: identity.id
    registryServer: registryServer
    name: 'ollama'
    image: '${registryServer}/bookfinder-ollama:${imageTag}'
    targetPort: 11434
    cpu: '1.0'
    memory: '2Gi'
    cooldownSeconds: modelCooldownSeconds
  }
  dependsOn: [registryPull]
}

module rerankerApp 'app.bicep' = if (deployApps && !useVoyage) {
  name: 'app-reranker'
  params: {
    location: location
    environmentId: environment.id
    identityId: identity.id
    registryServer: registryServer
    name: 'reranker'
    image: '${registryServer}/bookfinder-reranker:${imageTag}'
    targetPort: 80
    cpu: rerankerGpu ? '2.0' : '4.0'
    memory: '8Gi'
    workloadProfile: rerankerGpu ? gpuProfile : 'Consumption'
    cooldownSeconds: modelCooldownSeconds
  }
  dependsOn: [registryPull]
}

module api 'app.bicep' = if (deployApps) {
  name: 'app-api'
  params: {
    location: location
    environmentId: environment.id
    identityId: identity.id
    registryServer: registryServer
    name: 'api'
    image: '${registryServer}/bookfinder-api:${imageTag}'
    targetPort: 8080
    // The Copilot runtime, which reads the queries, needs more than the api alone.
    cpu: useQueryModel ? '0.5' : '0.25'
    memory: useQueryModel ? '1Gi' : '0.5Gi'
    minReplicas: webApiMinReplicas
    secrets: concat(
      [
        {
          name: 'database-url'
          value: 'postgres://${dbUser}:${postgresPassword}@${postgres.properties.fullyQualifiedDomainName}:5432/${dbName}?sslmode=require'
        }
      ],
      // Keys are read from the vault (the latest version, refreshed by Container Apps).
      useVoyage ? [{ name: voyageSecret, keyVaultUrl: '${vault.properties.vaultUri}secrets/${voyageSecret}', identity: identity.id }] : [],
      useQueryModel ? [{ name: copilotSecret, keyVaultUrl: '${vault.properties.vaultUri}secrets/${copilotSecret}', identity: identity.id }] : []
    )
    env: concat(
      [
        { name: 'DATABASE_URL', secretRef: 'database-url' }
        { name: 'OLLAMA_URL', value: 'http://ollama' }
      ],
      useVoyage ? [{ name: 'VOYAGE_API_KEY', secretRef: voyageSecret }] : [{ name: 'RERANKER_URL', value: 'http://reranker' }],
      useQueryModel
        ? [
            { name: 'LLM_PROVIDER', value: 'copilot' }
            { name: 'LLM_MODEL', value: queryModel }
            { name: 'LLM_API_KEY', secretRef: copilotSecret }
          ]
        : []
    )
  }
  dependsOn: [registryPull, vaultRead, database]
}

module web 'app.bicep' = if (deployApps) {
  name: 'app-web'
  params: {
    location: location
    environmentId: environment.id
    identityId: identity.id
    registryServer: registryServer
    name: 'web'
    image: '${registryServer}/bookfinder-web:${imageTag}'
    targetPort: 8080
    external: true
    cpu: '0.25'
    memory: '0.5Gi'
    minReplicas: webApiMinReplicas
    env: [
      { name: 'API_URL', value: 'http://api' }
    ]
  }
  dependsOn: [registryPull]
}

output registryName string = registry.name
output registryServer string = registryServer
output postgresServer string = postgres.name
output environmentName string = environment.name
output vaultName string = vault.name
output voyageSecretName string = voyageSecret
output copilotSecretName string = copilotSecret
output url string = deployApps ? 'https://${web!.outputs.fqdn}' : ''
