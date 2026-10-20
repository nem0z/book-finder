// Book Finder on Azure: container apps (web, api, ollama, and the reranker unless Voyage AI re-ranks) in a private
// network, with a managed PostgreSQL server that has no public address, and an Azure OpenAI account. Only the web
// app is reachable from the internet. See docs/azure.md.

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

@description('Who re-ranks the search results: voyage (Voyage AI\'s API, needs voyageApiKey), or the local model in a reranker app on cpu or on a T4 gpu (the T4 scores differently, see docs/azure.md).')
@allowed(['voyage', 'cpu', 'gpu'])
param reranker string = 'voyage'

@description('Voyage AI API key, for reranker=voyage.')
@secure()
param voyageApiKey string = ''

@description('Model deployments of the Azure OpenAI account, shared by the features that use an LLM. Each: name, model, version, sku (DataZoneStandard keeps the data in the EU), capacity (thousands of tokens per minute).')
param openAIDeployments array = [
  { name: 'gpt-4.1-mini', model: 'gpt-4.1-mini', version: '2025-04-14', sku: 'DataZoneStandard', capacity: 50 }
]

@description('Deployment (in openAIDeployments) that reads search queries before the vector search; empty to search with the raw query.')
param queryModel string = 'gpt-4.1-mini'

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
var openAIUserRole = subscriptionResourceId('Microsoft.Authorization/roleDefinitions', '5e0bd9bd-7b93-4f28-af87-19fc36ad61bd')
var openAIName = '${prefix}-ai-${suffix}'

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

// Azure OpenAI. The api calls it with its managed identity, so no key is involved on Azure.
resource openAI 'Microsoft.CognitiveServices/accounts@2024-10-01' = {
  name: openAIName
  location: location
  kind: 'OpenAI'
  sku: { name: 'S0' }
  properties: {
    customSubDomainName: openAIName
    publicNetworkAccess: 'Enabled'
  }
}

// An account takes one deployment change at a time.
@batchSize(1)
resource openAIModels 'Microsoft.CognitiveServices/accounts/deployments@2024-10-01' = [
  for d in openAIDeployments: {
    parent: openAI
    name: d.name
    sku: { name: d.sku, capacity: d.capacity }
    properties: {
      model: { format: 'OpenAI', name: d.model, version: d.version }
    }
  }
]

resource openAIUser 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(openAI.id, identity.id, openAIUserRole)
  scope: openAI
  properties: {
    roleDefinitionId: openAIUserRole
    principalId: identity.properties.principalId
    principalType: 'ServicePrincipal'
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
    cpu: '0.25'
    memory: '0.5Gi'
    minReplicas: webApiMinReplicas
    secrets: concat(
      [
        {
          name: 'database-url'
          value: 'postgres://${dbUser}:${postgresPassword}@${postgres.properties.fullyQualifiedDomainName}:5432/${dbName}?sslmode=require'
        }
      ],
      useVoyage ? [{ name: 'voyage-api-key', value: voyageApiKey }] : []
    )
    env: concat(
      [
        { name: 'DATABASE_URL', secretRef: 'database-url' }
        { name: 'OLLAMA_URL', value: 'http://ollama' }
      ],
      useVoyage ? [{ name: 'VOYAGE_API_KEY', secretRef: 'voyage-api-key' }] : [{ name: 'RERANKER_URL', value: 'http://reranker' }],
      empty(queryModel)
        ? []
        : [
            { name: 'LLM_URL', value: 'https://${openAIName}.openai.azure.com/openai/v1' }
            { name: 'LLM_MODEL', value: queryModel }
            // Picks the user-assigned identity for the Azure OpenAI token.
            { name: 'AZURE_CLIENT_ID', value: identity.properties.clientId }
          ]
    )
  }
  dependsOn: [registryPull, database, openAIModels, openAIUser]
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
output openAIAccount string = openAI.name
output openAIEndpoint string = 'https://${openAIName}.openai.azure.com/openai/v1'
output url string = deployApps ? 'https://${web!.outputs.fqdn}' : ''
