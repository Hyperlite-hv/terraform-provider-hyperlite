# By `<storage_pool>/<volume>`; the same led by `<node>/` for one on another node of the cluster.
terraform import hyperlite_storage_volume.data fast/data.qcow2
terraform import hyperlite_storage_volume.data pve-b/fast/data.qcow2
