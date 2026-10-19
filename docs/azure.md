# Running on Azure

Book Finder runs on [Azure Container Apps](https://learn.microsoft.com/azure/container-apps/) with a managed
PostgreSQL server, in **France Central**. Everything is defined in `deploy/azure/main.bicep`. The local Docker
setup (`docker-compose.yml`) keeps working for development and testing.

## Architecture

```mermaid
flowchart LR
  user([Browser]) -->|HTTPS| web
  subgraph vnet[Private network]
    subgraph env[Container Apps environment]
      web[web<br/>nginx, public] -->|/search| api
      api --> ollama[ollama<br/>query embedding, CPU]
      api --> reranker[reranker<br/>T4 GPU]
    end
    api --> db[(PostgreSQL 17<br/>+ pgvector, private)]
  end
  registry[(Container registry)] -.images.-> env
```

- **Only `web` is public.** `api`, `ollama` and `reranker` have internal ingress, reachable as `http://<name>`
  inside the environment. The database has no public address: it lives in its own subnet and is found through a
  private DNS zone.
- **Images** come from a Basic container registry, pulled with a managed identity (no registry password).
- **Models are built into the images** (see `deploy/ollama`, `deploy/reranker`), so containers that scale from zero
  don't download them.

| Resource | Size | Scales |
|---|---|---|
| `web` | 0.25 vCPU, 0.5 GiB | to zero, unless `webApiMinReplicas=1` |
| `api` | 0.25 vCPU, 0.5 GiB | to zero, unless `webApiMinReplicas=1` |
| `ollama` | 1 vCPU, 2 GiB | to zero after `modelCooldownSeconds` (30 min) |
| `reranker` | T4 GPU, 2 vCPU, 8 GiB (`rerankerGpu=true`) or 2 vCPU, 4 GiB on CPU | to zero after `modelCooldownSeconds` (30 min) |
| PostgreSQL | Flexible Server B1ms (1 vCore, 2 GiB), 32 GB, 7 days of backups | always on (can be stopped, see below) |

## Why France Central

The app is used from Ireland, France and the Netherlands. Of the European regions with serverless T4 GPUs
(Sweden Central, Italy North, France Central, West Europe), France Central is the cheapest for what we use and is
central for those users. North Europe (Ireland) has no serverless GPU. Prices compared on 2026-10-19 (USD list
prices):

| Region | T4 / hour | vCPU / hour | PostgreSQL B1ms / month |
|---|---|---|---|
| **France Central** | $0.33 | $0.09 | $13.87 |
| Sweden Central | $0.34 | $0.09 | $14.53 |
| West Europe | $0.34 | $0.12 | $14.53 |

## Cost

- **Fixed:** about **$20 a month**: PostgreSQL (~$14), its storage, and the registry (~$5). Apps scaled to zero
  cost nothing.
- **While in use:** the re-ranker replica costs about $0.55 an hour (T4 $0.33 plus its 2 vCPUs and 8 GiB), billed
  per second from when it starts until `modelCooldownSeconds` after the last search. GPU replicas are always
  billed at the active rate. Ollama, api and web add a few cents an hour, mostly covered by the monthly free
  allowance (180,000 vCPU-seconds and 360,000 GiB-seconds per subscription).

To pause the database between uses (it restarts automatically after 7 days):

```sh
az postgres flexible-server stop  -g bookfinder-rg -n <server>
az postgres flexible-server start -g bookfinder-rg -n <server>
```

## Cold starts

When an app has scaled to zero, the next request starts it. CPU apps start in seconds; a GPU app can take several
minutes the first time (pulling the image onto a GPU host). The cooldown keeps the models warm for 30 minutes after
the last search. A warm-up when the page opens, and a fallback to vector order while the re-ranker starts, are
planned.

## Parameters

| Parameter | Default | |
|---|---|---|
| `location` | resource group's | France Central in our setup |
| `prefix` | `bookfinder` | resource names |
| `postgresPassword` | – | letters and digits only (it goes into a URL) |
| `deployApps` | `true` | `false` on the first run, before the images are in the registry |
| `imageTag` | `latest` | |
| `rerankerGpu` | `true` | `false` runs the re-ranker on CPU: no GPU quota needed, ~10–15 s per search |
| `webApiMinReplicas` | `0` | `1` keeps web and api running |
| `modelCooldownSeconds` | `1800` | how long ollama and the re-ranker stay up after the last request |

## Check before deploying

```sh
az group create -n bookfinder-rg -l francecentral
az deployment group what-if -g bookfinder-rg -f deploy/azure/main.bicep -p postgresPassword=<password> deployApps=false
```

Building the images, deploying and copying the database are covered by the deployment script (next step).
