data "hyperlite_storage_pool" "default" {
  name = "default"
}

output "free_gb" {
  value = data.hyperlite_storage_pool.default.available_gb
}
