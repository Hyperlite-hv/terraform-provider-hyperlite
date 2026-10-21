package provider

import (
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// Acceptance tests run against the Hyperlite named by HYPERLITE_SERVER and HYPERLITE_TOKEN, with TF_ACC=1
// (.github/workflows/acceptance.yml starts one on the runner). They create real objects, named tfacc-*.

const accProvider = `provider "hyperlite" {}
`

func accPreCheck(t *testing.T) {
	t.Helper()
	for _, v := range []string{"HYPERLITE_SERVER", "HYPERLITE_TOKEN"} {
		if os.Getenv(v) == "" {
			t.Fatalf("%s must be set for acceptance tests", v)
		}
	}
}

func TestAccDataSources(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config: accProvider + `
data "hyperlite_node" "local" { name = "local" }
data "hyperlite_network" "isolated" { name = "hyperlite-isolated" }
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.hyperlite_node.local", "status", "online"),
				resource.TestCheckResourceAttr("data.hyperlite_network.isolated", "address", "192.168.199.1"),
				resource.TestCheckResourceAttr("data.hyperlite_network.isolated", "active", "true"),
				resource.TestCheckResourceAttrSet("data.hyperlite_network.isolated", "uuid"),
			),
		}},
	})
}
