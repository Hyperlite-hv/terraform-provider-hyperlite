data "hyperlite_template" "debian" {
  name = "debian-12-base"
}

# A VM deployed from a template, customised at its first boot, as vSphere's clone { customize }.
resource "hyperlite_vm" "web" {
  name      = "web-1"
  vcpu      = 2
  memory_mb = 4096
  tags      = ["web", "prod"]

  clone {
    template = data.hyperlite_template.debian.name
  }

  customize {
    hostname = "web-1"
    domain   = "example.internal"
    dns      = ["10.0.20.53"]

    network {
      addresses = ["10.0.20.11/24"]
      gateway   = "10.0.20.1"
    }
  }

  # The first disk is the system disk; it only grows.
  disk { size_gb = data.hyperlite_template.debian.disks[0].size_gb + 10 }
  disk { size_gb = 50 }

  network_interface { network = "lan" }
}

# A VM made from Hyperlite's cloud image, with its account.
resource "hyperlite_vm" "worker" {
  name      = "worker-1"
  vcpu      = 1
  memory_mb = 2048

  cloud_init {
    user     = "ops"
    password = var.vm_password
    ssh_keys = [var.ssh_public_key]
  }

  disk { size_gb = 20 }
  network_interface { network = "lan" }
}

variable "vm_password" {
  type      = string
  sensitive = true
}

variable "ssh_public_key" {
  type = string
}

output "web_address" {
  value = hyperlite_vm.web.ip_address
}
