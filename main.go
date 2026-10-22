// The official Terraform / OpenTofu provider for Hyperlite.
//
// The documentation in docs/ is generated from the schemas and examples/ by `go generate ./...`.
//
//go:generate go tool tfplugindocs generate --provider-name hyperlite --rendered-provider-name Hyperlite
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/Hyperlite-hv/terraform-provider-hyperlite/internal/provider"
)

// version is set by GoReleaser at release time.
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run with support for debuggers such as delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/hyperlite-hv/hyperlite",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
