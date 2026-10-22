terraform {
  required_providers {
    hyperlite = {
      source = "hyperlite-hv/hyperlite"
    }
  }
}

# The address and the token can also come from HYPERLITE_SERVER and HYPERLITE_TOKEN.
provider "hyperlite" {
  server    = "https://hyperlite.example.internal:8000"
  api_token = var.hyperlite_token
  # The certificate's SHA-256, when the system does not trust it (shown by `hyperlite login`).
  # fingerprint = "AB12..."
}

variable "hyperlite_token" {
  type      = string
  sensitive = true
}
