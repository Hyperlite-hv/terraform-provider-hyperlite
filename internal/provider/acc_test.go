package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/Hyperlite-hv/Hyperlite/cli/hyperlite"
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

func accVM(name, cpu, extra string) string {
	return accProvider + `
resource "hyperlite_vm" "acc" {
  name      = "` + name + `"
  vcpu      = ` + cpu + `
  memory_mb = 512
  tags      = ["tfacc"]

  cloud_init {
    user     = "tfacc"
    password = "Tfacc-pass-2026"
  }

  customize {
    hostname = "tfacc"
    network {
      addresses = ["192.168.199.240/24"]
      gateway   = "192.168.199.1"
    }
  }

  disk { size_gb = 3 }
` + extra + `
  network_interface { network = "hyperlite-isolated" }
}
`
}

// TestAccVM: a VM made from the cloud image, changed (which needs it stopped), renamed, imported and destroyed, on a
// real Hyperlite.
func TestAccVM(t *testing.T) {
	more := `
  disk { size_gb = 1 }
  network_interface { network = "hyperlite-isolated" }
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: factories,
		CheckDestroy:             accGone("tfacc-vm", "tfacc-vm2"),
		Steps: []resource.TestStep{
			{
				Config: accVM("tfacc-vm", "1", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperlite_vm.acc", "state", "active"),
					resource.TestCheckResourceAttr("hyperlite_vm.acc", "ip_address", "192.168.199.240"),
					resource.TestCheckResourceAttr("hyperlite_vm.acc", "disk.0.size_gb", "3"),
					resource.TestCheckResourceAttrSet("hyperlite_vm.acc", "network_interface.0.mac"),
					resource.TestCheckResourceAttr("hyperlite_vm.acc", "customize.hostname", "tfacc"),
				),
			},
			{Config: accVM("tfacc-vm", "1", ""), PlanOnly: true},
			{
				Config: accVM("tfacc-vm", "2", more),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperlite_vm.acc", "vcpu", "2"),
					resource.TestCheckResourceAttr("hyperlite_vm.acc", "disk.#", "2"),
					resource.TestCheckResourceAttr("hyperlite_vm.acc", "disk.1.size_gb", "1"),
					resource.TestCheckResourceAttr("hyperlite_vm.acc", "network_interface.#", "2"),
					resource.TestCheckResourceAttr("hyperlite_vm.acc", "state", "active"),
				),
			},
			{Config: accVM("tfacc-vm", "2", more), PlanOnly: true},
			{
				Config: accVM("tfacc-vm2", "2", more),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperlite_vm.acc", "name", "tfacc-vm2"),
					resource.TestCheckResourceAttr("hyperlite_vm.acc", "state", "active"),
				),
			},
			{
				ResourceName:            "hyperlite_vm.acc",
				ImportState:             true,
				ImportStateId:           "tfacc-vm2",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"cloud_init", "customize", "timeouts", "ip_address"},
			},
		},
	})
}

// accGone checks, after the destroy, that Hyperlite has none of these VMs left.
func accGone(names ...string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		c := accClient()
		for _, n := range names {
			if _, err := c.vmByName(context.Background(), n, ""); err == nil {
				return fmt.Errorf("the VM %s is still there after the destroy", n)
			} else if !errors.Is(err, errNotFound) {
				return err
			}
		}
		return nil
	}
}

func accClient() *Client {
	return &Client{API: &hyperlite.Client{URL: os.Getenv("HYPERLITE_SERVER"), Token: os.Getenv("HYPERLITE_TOKEN")}}
}
