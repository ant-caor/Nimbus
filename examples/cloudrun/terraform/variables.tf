variable "project_id" {
  type        = string
  description = "GCP project ID."
}

variable "region" {
  type        = string
  description = "Region for Cloud Run, Memorystore, and the VPC subnet."
  default     = "europe-west1"
}

variable "service_name" {
  type        = string
  description = "Cloud Run service name (also used to name related resources)."
  default     = "nimbus"
}

variable "image" {
  type        = string
  description = "Container image to deploy, e.g. REGION-docker.pkg.dev/PROJECT/REPO/nimbus:latest"
}

variable "push_audience" {
  type        = string
  description = <<-EOT
    Explicit OIDC audience for the Pub/Sub push token. The service verifies the
    token's "aud" claim matches this exact string (in-process, on top of the
    run.invoker IAM binding). A stable custom value is used instead of the
    auto-generated Cloud Run URL to avoid a Terraform self-reference cycle
    (the service's env var cannot depend on the service's own computed URI).
    Override only if you also change PUSH_AUDIENCE in the service.
  EOT
  default     = "nimbus-invalidation-push"
}
