# runcache on Cloud Run

A deployable reference: a runcache service with an in-process L1, a Memorystore
(Redis) L2, and a **Pub/Sub push** invalidation bus. Push delivery is
throttle-safe under Cloud Run's default request-only CPU allocation.

> ⚠️ **Cost.** This provisions a **Memorystore Basic instance (1 GB)** that
> **never scales to zero** and bills continuously (roughly **$35-50/month**,
> region dependent), plus Pub/Sub and Cloud Run usage. Cloud Run itself and
> Direct VPC Egress scale to zero. **Run `terraform destroy` when you are done.**

## Architecture

```
   Pub/Sub topic ──push(OIDC)──► Cloud Run service ──Direct VPC Egress──► Memorystore
        ▲                          (N instances, L1 each)
        └────────── publish on Set / Invalidate ──────────┘
```

Each instance publishes invalidations to the topic; Pub/Sub pushes them back to
the service (load-balanced to one instance, which evicts its L1). Instances that
do not receive a given push converge on their next L2 read, because **L2 is the
source of truth**. Direct VPC Egress (not a Serverless VPC Access connector) is
used so the data path scales to zero and avoids the connector's standing cost.

## Deploy

```sh
# 1. Build and push the image (from the repo root).
gcloud builds submit --tag REGION-docker.pkg.dev/PROJECT/REPO/runcache:latest \
  --config /dev/stdin <<'EOF'
steps:
  - name: gcr.io/cloud-builders/docker
    args: ["build", "-f", "examples/cloudrun/Dockerfile", "-t", "$_IMAGE", "."]
images: ["$_IMAGE"]
EOF
# (or: docker build -f examples/cloudrun/Dockerfile -t <image> . && docker push <image>)

# 2. Provision everything.
cd examples/cloudrun/terraform
cp terraform.tfvars.example terraform.tfvars   # edit project_id + image
terraform init
terraform apply

# 3. Exercise it (the endpoint requires auth).
URL=$(terraform output -raw service_url)
TOKEN=$(gcloud auth print-identity-token)
curl -H "Authorization: Bearer $TOKEN" "$URL/items/42"
```

## Endpoints

| Method | Path | Purpose |
|---|---|---|
| GET | `/items/{id}` | read-through (L1 -> L2 -> loader) |
| PUT | `/items/{id}` | write a value (publishes an invalidation) |
| DELETE | `/items/{id}` | invalidate a key (publishes an invalidation) |
| POST | `/_ah/push` | Pub/Sub push endpoint (OIDC-authenticated) |
| GET | `/healthz` | liveness |

The `loadItem` function in `main.go` is a placeholder; replace it with your real
backend and return `runcache.ErrNotFound` for missing items to enable negative
caching.

## Cleanup

```sh
terraform destroy
```
