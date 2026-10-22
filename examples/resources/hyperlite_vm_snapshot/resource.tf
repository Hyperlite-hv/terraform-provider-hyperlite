resource "hyperlite_vm_snapshot" "before_upgrade" {
  vm          = hyperlite_vm.web.name
  name        = "before-upgrade"
  description = "Taken before the 13.2 upgrade"
}
