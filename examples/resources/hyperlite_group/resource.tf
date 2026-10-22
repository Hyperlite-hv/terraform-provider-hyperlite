resource "hyperlite_group" "ops" {
  name    = "ops"
  members = [hyperlite_user.ci.username]
}
