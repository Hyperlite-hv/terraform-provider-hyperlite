resource "hyperlite_user" "ci" {
  username = "ci"
  password = var.ci_password
}

variable "ci_password" {
  type      = string
  sensitive = true
}
