# Its disks must be on a shared (NFS) storage pool.
resource "hyperlite_vm_ha" "web" {
  vm = hyperlite_vm.web.name
}
