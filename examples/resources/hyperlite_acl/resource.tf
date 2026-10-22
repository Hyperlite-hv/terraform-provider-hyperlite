# The group may create and run VMs in the pool...
resource "hyperlite_acl" "ops_on_web" {
  subject_type  = "group"
  subject       = hyperlite_group.ops.id
  role          = hyperlite_role.deployer.key
  resource_type = "pool"
  resource      = hyperlite_resource_pool.web.id
}

# ...and put their disks in this storage pool.
resource "hyperlite_acl" "ops_on_fast" {
  subject_type  = "group"
  subject       = hyperlite_group.ops.id
  role          = hyperlite_role.deployer.key
  resource_type = "storage"
  resource      = hyperlite_storage_pool.fast.name
}
