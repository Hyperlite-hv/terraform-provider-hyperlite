resource "hyperlite_storage_volume" "data" {
  storage_pool = hyperlite_storage_pool.fast.name
  name         = "data"
  size_gb      = 100
}
