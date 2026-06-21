# Nimbus on Cloud Run

A deployable reference: a Nimbus service with an in-process L1, a Memorystore
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

## Push endpoint authentication (defense-in-depth)

The `/_ah/push` endpoint is protected by **two independent layers**:

1. **IAM (network layer).** The push subscription authenticates with an OIDC
   token minted for the `*-push` service account, and only that account holds
   `roles/run.invoker` on the service. Cloud Run rejects any caller without it,
   so the endpoint is not publicly invocable.
2. **In-process OIDC verification (application layer).** The handler additionally
   verifies the Pub/Sub OIDC JWT itself via `gcppubsub.WithPushAuth`: it checks
   the Google signature/issuer, the `aud` claim against `PUSH_AUDIENCE`, and the
   `email` claim against the `PUSH_SA_EMAIL` allowlist. It returns **401** for a
   missing/invalid token, **403** for a wrong audience or non-allowlisted
   account, and **204** only after verification passes.

The Terraform sets an **explicit `audience`** on the subscription's `oidc_token`
(`var.push_audience`, a stable string) and passes the same value plus the push
service-account email to the service as `PUSH_AUDIENCE` / `PUSH_SA_EMAIL`. A
stable custom audience is used instead of the auto-generated Cloud Run URL to
avoid a Terraform self-reference cycle (the service's env var cannot depend on
the service's own computed URI). Layer 2 is defense-in-depth: even if the IAM
binding were ever misconfigured, a forged or replayed request without a valid
Pub/Sub-signed token for the right audience and account is rejected in-process.

## Deploy

```sh
# 1. Build and push the image (from the repo root).
gcloud builds submit --tag REGION-docker.pkg.dev/PROJECT/REPO/nimbus:latest \
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
| POST | `/_ah/push` | Pub/Sub push endpoint (IAM + in-process OIDC verification) |
| GET | `/healthz` | liveness |

The `loadItem` function in `main.go` is a placeholder; replace it with your real
backend and return `nimbus.ErrNotFound` for missing items to enable negative
caching.

## Cleanup

```sh
terraform destroy
```
