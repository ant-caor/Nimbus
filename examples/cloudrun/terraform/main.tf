# Enable the APIs this example uses.
resource "google_project_service" "apis" {
  for_each = toset([
    "run.googleapis.com",
    "pubsub.googleapis.com",
    "redis.googleapis.com",
    "compute.googleapis.com",
  ])
  service            = each.value
  disable_on_destroy = false
}

# A VPC + subnet so Cloud Run can reach Memorystore over Direct VPC Egress
# (no Serverless VPC Access connector, which would bill like an always-on VM).
resource "google_compute_network" "vpc" {
  name                    = "${var.service_name}-vpc"
  auto_create_subnetworks = false
  depends_on              = [google_project_service.apis]
}

resource "google_compute_subnetwork" "subnet" {
  name          = "${var.service_name}-subnet"
  ip_cidr_range = "10.8.0.0/24"
  region        = var.region
  network       = google_compute_network.vpc.id
}

# Memorystore for Redis (the L2). NOTE: Basic tier never scales to zero; it bills
# continuously (~$35-50/month for 1GB, region dependent). Destroy when done.
resource "google_redis_instance" "cache" {
  name               = "${var.service_name}-l2"
  tier               = "BASIC"
  memory_size_gb     = 1
  region             = var.region
  authorized_network = google_compute_network.vpc.id
  connect_mode       = "DIRECT_PEERING"
  redis_version      = "REDIS_7_2"
  depends_on         = [google_project_service.apis]
}

resource "google_pubsub_topic" "inval" {
  name       = "${var.service_name}-invalidation"
  depends_on = [google_project_service.apis]
}

# Runtime identity for the service.
resource "google_service_account" "run" {
  account_id   = "${var.service_name}-run"
  display_name = "nimbus Cloud Run service"
}

# The service publishes invalidations to the topic.
resource "google_pubsub_topic_iam_member" "publisher" {
  topic  = google_pubsub_topic.inval.id
  role   = "roles/pubsub.publisher"
  member = "serviceAccount:${google_service_account.run.email}"
}

resource "google_cloud_run_v2_service" "svc" {
  name                = var.service_name
  location            = var.region
  deletion_protection = false
  ingress             = "INGRESS_TRAFFIC_ALL"

  template {
    service_account = google_service_account.run.email

    # Direct VPC Egress: reach Memorystore on the VPC, scales to zero, no connector.
    vpc_access {
      network_interfaces {
        network    = google_compute_network.vpc.id
        subnetwork = google_compute_subnetwork.subnet.id
      }
      egress = "PRIVATE_RANGES_ONLY"
    }

    containers {
      image = var.image
      ports {
        container_port = 8080
      }
      env {
        name  = "PROJECT_ID"
        value = var.project_id
      }
      env {
        name  = "REDIS_ADDR"
        value = "${google_redis_instance.cache.host}:${google_redis_instance.cache.port}"
      }
      env {
        name  = "PUBSUB_TOPIC"
        value = google_pubsub_topic.inval.name
      }
    }
  }

  depends_on = [google_project_service.apis]
}

# Identity Pub/Sub uses to authenticate push requests to the service (OIDC).
resource "google_service_account" "push" {
  account_id   = "${var.service_name}-push"
  display_name = "nimbus Pub/Sub push"
}

# Only the push identity may invoke the service (the endpoint is not public).
resource "google_cloud_run_v2_service_iam_member" "push_invoker" {
  name     = google_cloud_run_v2_service.svc.name
  location = var.region
  role     = "roles/run.invoker"
  member   = "serviceAccount:${google_service_account.push.email}"
}

# Push subscription delivering invalidations to the service, authenticated by OIDC.
resource "google_pubsub_subscription" "push" {
  name                 = "${var.service_name}-push"
  topic                = google_pubsub_topic.inval.id
  ack_deadline_seconds = 10

  push_config {
    push_endpoint = "${google_cloud_run_v2_service.svc.uri}/_ah/push"
    oidc_token {
      service_account_email = google_service_account.push.email
    }
  }

  depends_on = [google_cloud_run_v2_service_iam_member.push_invoker]
}
