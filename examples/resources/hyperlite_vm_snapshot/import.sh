# By `<vm>/<snapshot>`; the same led by `<node>/` for one on another node of the cluster.
terraform import hyperlite_vm_snapshot.before_upgrade web-1/before-upgrade
terraform import hyperlite_vm_snapshot.before_upgrade pve-b/web-1/before-upgrade
