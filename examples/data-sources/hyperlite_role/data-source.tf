data "hyperlite_role" "operator" {
  name = "operator"
}

resource "hyperlite_acl" "alice_operates_web" {
  subject_type  = "user"
  subject       = "alice"
  role          = data.hyperlite_role.operator.key
  resource_type = "vm"
  resource      = "web-1"
}
