# Releasing the provider

A tag `vX.Y.Z` on `main` runs `.github/workflows/release.yml`: GoReleaser builds the provider for every platform,
writes the `SHA256SUMS` file and signs it with the release key. The Terraform Registry and the OpenTofu Registry both
read that GitHub release; neither holds a copy of the code.

## Once: the signing key (a maintainer)

The registries check every release against one public key. Its private half is the maintainer's: made on a
maintainer's own machine, kept there (and in the organisation's password manager), never in a chat or a ticket.

```bash
gpg --full-generate-key            # RSA 4096, no expiry or a long one, name "Hyperlite release", a passphrase
gpg --list-secret-keys --keyid-format=long          # note the key's id (the part after rsa4096/)
gpg --armor --export-secret-keys <KEY_ID> > private.asc
gpg --armor --export <KEY_ID>                        # the public key, given to the registries below
```

In this repository, **Settings > Environments > New environment** named `release`:

- Deployment branches and tags: tags matching `v*` only.
- Required reviewers: the maintainers (a release then waits for one of them to approve it).
- Environment secrets: `GPG_PRIVATE_KEY` (the content of `private.asc`) and `PASSPHRASE`.

Delete `private.asc` once it is stored.

## Once: the registries (a maintainer)

- **Terraform Registry**: sign in at <https://registry.terraform.io> with the GitHub account of a member of
  `Hyperlite-hv`, **Publish > Provider**, add the public key under **User settings > Signing keys** for the
  `hyperlite-hv` namespace, then choose this repository. The registry then reads each new release by itself.
- **OpenTofu Registry**: open an issue in <https://github.com/opentofu/registry> with the **Submit new provider** form
  (`hyperlite-hv/hyperlite`), and one with the **Submit new provider signing key** form with the public key. Once
  merged, the registry reads each new release by itself.

## Each release

1. `main` is green: Test (unit, docs, terraform 1.5.7 and 1.13.4, tofu) and Acceptance against the Hyperlite the
   release needs.
2. The README says which Hyperlite version it needs.
3. Tag and push: `git tag v1.0.0 && git push origin v1.0.0`.
4. Approve the `release` deployment when GitHub asks. The release appears with its archives, `SHA256SUMS` and
   `SHA256SUMS.sig`; both registries list it within an hour.

A version `vX.Y.Z-rc.N` is a pre-release: the registries list it, but a configuration gets it only by naming that exact
version, never through a constraint such as `~> 1.0`. It is how a release is tried before everyone gets it.
