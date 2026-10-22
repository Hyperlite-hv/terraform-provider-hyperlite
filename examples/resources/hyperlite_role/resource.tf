# What a deployment pipeline needs to create VMs in a pool, without being an administrator.
resource "hyperlite_role" "deployer" {
  name       = "deployer"
  privileges = ["vm.allocate", "vm.power", "vm.hardware", "storage.allocate", "network.assign"]
}
