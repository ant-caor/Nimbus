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
  default     = "runcache"
}

variable "image" {
  type        = string
  description = "Container image to deploy, e.g. REGION-docker.pkg.dev/PROJECT/REPO/runcache:latest"
}
