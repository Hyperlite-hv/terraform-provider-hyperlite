# A template copied from a VM (the VM stays, as vSphere's Clone to Template). A running VM is shut down for the copy
# and started again.
resource "hyperlite_template" "golden" {
  name = "debian-golden"
  vm   = hyperlite_vm.base.name
}
