resource "hyperlite_storage_pool" "fast" {
  name = "fast"
  type = "dir"
  path = "/srv/fast"
}

resource "hyperlite_storage_pool" "nas" {
  name            = "nas"
  type            = "netfs"
  nfs_host        = "nas.example.internal"
  nfs_export_path = "/export/vms"
}
