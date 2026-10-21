data "hyperlite_template" "debian" {
  name = "debian-12-base"
}

output "template_memory_mb" {
  value = data.hyperlite_template.debian.memory_mb
}
