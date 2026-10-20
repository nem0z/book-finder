// One container app with HTTP ingress, pulling its image from the registry with the shared identity.
param name string
param location string
param environmentId string
param identityId string
param registryServer string
param image string
param targetPort int

@description('Reachable from the internet (HTTPS). Internal apps are only reachable inside the environment, over plain HTTP at http://<name>.')
param external bool = false

@description('vCPUs, e.g. \'0.25\'. Consumption sizes go in pairs: 0.25 and 0.5Gi, 0.5 and 1Gi, 1 and 2Gi...')
param cpu string
param memory string
param env array = []
param secrets array = []
param minReplicas int = 0
param maxReplicas int = 1

@description('Seconds without requests before the last replica stops.')
param cooldownSeconds int = 300
param workloadProfile string = 'Consumption'

resource app 'Microsoft.App/containerApps@2026-01-01' = {
  name: name
  location: location
  identity: {
    type: 'UserAssigned'
    userAssignedIdentities: { '${identityId}': {} }
  }
  properties: {
    environmentId: environmentId
    workloadProfileName: workloadProfile
    configuration: {
      activeRevisionsMode: 'Single'
      ingress: {
        external: external
        targetPort: targetPort
        transport: 'http'
        // Internal apps call each other over plain HTTP inside the private network.
        allowInsecure: !external
      }
      registries: [
        { server: registryServer, identity: identityId }
      ]
      secrets: secrets
    }
    template: {
      containers: [
        {
          name: name
          image: image
          resources: { cpu: json(cpu), memory: memory }
          env: env
        }
      ]
      scale: {
        minReplicas: minReplicas
        maxReplicas: maxReplicas
        cooldownPeriod: cooldownSeconds
        rules: [
          { name: 'http', http: { metadata: { concurrentRequests: '10' } } }
        ]
      }
    }
  }
}

output fqdn string = app.properties.configuration.ingress.fqdn
