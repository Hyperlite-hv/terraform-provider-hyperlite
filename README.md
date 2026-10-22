# Terraform / OpenTofu provider for Hyperlite

The official provider for [Hyperlite](https://github.com/Hyperlite-hv/Hyperlite), a self-hosted virtualization
manager (KVM and LXC). It drives Hyperlite's API v1 with a personal API token.

Its design, accepted by the maintainers, is Hyperlite's
[`docs/design/terraform-provider.md`](https://github.com/Hyperlite-hv/Hyperlite/blob/test/docs/design/terraform-provider.md).

**Status: under development.** Data sources `hyperlite_node`, `hyperlite_network` and `hyperlite_template` exist; the
VM resource and the others follow (design, section 15). Not published to the registries yet.

## Use

```hcl
terraform {
  required_providers {
    hyperlite = { source = "hyperlite-hv/hyperlite" }
  }
}

provider "hyperlite" {
  server    = "https://hyperlite.example.internal:8000" # or HYPERLITE_SERVER
  api_token = var.hyperlite_token                      # or HYPERLITE_TOKEN
}
```

Create the token in Hyperlite, **Account security > API tokens**. The provider checks at once that the server speaks
API v1 and that the token is accepted. A certificate the system does not trust (self-signed, internal CA) is pinned
with `fingerprint`, its SHA-256 as `hyperlite login` shows it.

## Develop

```bash
go build ./...
go test ./...   # drives the terraform (or TF_ACC_TERRAFORM_PATH) found on the PATH against a fake Hyperlite
```

The HTTP client is Hyperlite's own Go package, `github.com/Hyperlite-hv/Hyperlite/cli/hyperlite`, which
`hyperlite-cli` is built on too.

## License

Mozilla Public License 2.0 ([LICENSE](LICENSE)), as most Terraform providers. The Go library it uses, Hyperlite's `cli/`, is under the same license.

## Release

A tag `vX.Y.Z` runs `.github/workflows/release.yml`: GoReleaser builds the archives, signs the checksums with the
organisation's release key (protected `release` environment) and publishes a GitHub release, which
registry.terraform.io and registry.opentofu.org read.
