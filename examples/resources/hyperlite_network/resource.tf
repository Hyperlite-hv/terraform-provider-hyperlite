resource "hyperlite_network" "lan" {
  name       = "lan"
  mode       = "nat"
  address    = "10.0.20.1"
  dhcp_start = "10.0.20.100"
  dhcp_end   = "10.0.20.200"
}

resource "hyperlite_network" "dmz" {
  name   = "dmz"
  mode   = "bridge"
  bridge = "br0"
}
