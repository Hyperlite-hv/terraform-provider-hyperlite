# By the VM's name; the same led by `<node>/` for one on another node of the cluster.
terraform import hyperlite_vm_backup_schedule.web web-1
terraform import hyperlite_vm_backup_schedule.web pve-b/web-1
