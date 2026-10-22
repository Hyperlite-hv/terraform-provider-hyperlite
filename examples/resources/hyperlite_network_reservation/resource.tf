resource "hyperlite_network_reservation" "web" {
  network = hyperlite_network.lan.name
  mac     = hyperlite_vm.web.network_interface[0].mac
  ip      = "10.0.20.50"
  name    = "web-1"
}
