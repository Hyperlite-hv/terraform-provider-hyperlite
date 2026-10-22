data "hyperlite_template" "debian" {
  name = "debian-12-base"
}

# The template's disk is the smallest a VM deployed from it can have.
output "system_disk_gb" {
  value = data.hyperlite_template.debian.disks[0].size_gb
}
