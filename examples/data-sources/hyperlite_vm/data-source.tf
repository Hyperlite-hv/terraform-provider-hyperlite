data "hyperlite_vm" "legacy" {
  name = "legacy-app"
}

# Reserve the address of a VM made elsewhere.
resource "hyperlite_network_reservation" "legacy" {
  network = data.hyperlite_vm.legacy.network_interface[0].network
  mac     = data.hyperlite_vm.legacy.network_interface[0].mac
  ip      = "10.0.20.60"
}
