resource "hyperlite_iso" "debian" {
  name        = "debian-13.1.0-amd64-netinst.iso"
  source      = "${path.module}/debian-13.1.0-amd64-netinst.iso"
  source_hash = filesha256("${path.module}/debian-13.1.0-amd64-netinst.iso")
}
