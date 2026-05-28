output "service_url" {
  description = "Cloud Run service URL (requires authentication)."
  value       = google_cloud_run_v2_service.svc.uri
}

output "redis_host" {
  description = "Memorystore host the service connects to."
  value       = google_redis_instance.cache.host
}

output "topic" {
  description = "Pub/Sub invalidation topic."
  value       = google_pubsub_topic.inval.name
}
