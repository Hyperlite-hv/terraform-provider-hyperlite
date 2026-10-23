package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

func accNetwork(name, dhcpEnd string) string {
	return accProvider + `
resource "hyperlite_network" "acc" {
  name       = "` + name + `"
  mode       = "nat"
  address    = "10.123.0.1"
  dhcp_start = "10.123.0.10"
  dhcp_end   = "` + dhcpEnd + `"
}

resource "hyperlite_network_reservation" "acc" {
  network = hyperlite_network.acc.name
  mac     = "52:54:00:12:34:56"
  ip      = "10.123.0.5"
  name    = "tfacc-host"
}
`
}

// TestAccNetwork: a NAT network made, its DHCP range changed in place, renamed, a reservation on it, imported and
// destroyed, on a real Hyperlite.
func TestAccNetwork(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: factories,
		CheckDestroy: func(*terraform.State) error {
			for _, n := range []string{"tfacc-net", "tfacc-net2"} {
				if _, err := accClient().network(context.Background(), n); !errors.Is(err, errNotFound) {
					return fmt.Errorf("the network %s is still there after the destroy (%v)", n, err)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: accNetwork("tfacc-net", "10.123.0.100"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperlite_network.acc", "active", "true"),
					resource.TestCheckResourceAttr("hyperlite_network.acc", "netmask", "255.255.255.0"),
					resource.TestCheckResourceAttr("hyperlite_network_reservation.acc", "ip", "10.123.0.5"),
				),
			},
			{Config: accNetwork("tfacc-net", "10.123.0.100"), PlanOnly: true},
			{
				Config: accNetwork("tfacc-net", "10.123.0.150"),
				Check:  resource.TestCheckResourceAttr("hyperlite_network.acc", "dhcp_end", "10.123.0.150"),
			},
			{
				Config: accNetwork("tfacc-net2", "10.123.0.150"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperlite_network.acc", "id", "tfacc-net2"),
					resource.TestCheckResourceAttr("hyperlite_network_reservation.acc", "id", "tfacc-net2/52:54:00:12:34:56"),
				),
			},
			{Config: accNetwork("tfacc-net2", "10.123.0.150"), PlanOnly: true},
			{Config: accNetwork("tfacc-net", "10.123.0.150")},
			{
				ResourceName:      "hyperlite_network.acc",
				ImportState:       true,
				ImportStateId:     "tfacc-net",
				ImportStateVerify: true,
			},
			{
				ResourceName:      "hyperlite_network_reservation.acc",
				ImportState:       true,
				ImportStateId:     "tfacc-net/52:54:00:12:34:56",
				ImportStateVerify: true,
			},
		},
	})
}

const accAccess = accProvider + `
resource "hyperlite_user" "ci" {
  username = "tfacc-ci"
  password = "Harbor-Lantern-2026"
}

resource "hyperlite_group" "ops" {
  name    = "tfacc-ops"
  members = [hyperlite_user.ci.username]
}

resource "hyperlite_role" "tf" {
  name       = "tfacc-terraform"
  privileges = ["vm.allocate", "vm.power", "vm.hardware", "storage.allocate", "network.assign"]
}

resource "hyperlite_resource_pool" "web" {
  name        = "tfacc-web"
  description = "VMs of the acceptance tests"
}

resource "hyperlite_acl" "on_pool" {
  subject_type  = "group"
  subject       = hyperlite_group.ops.id
  role          = hyperlite_role.tf.key
  resource_type = "pool"
  resource      = hyperlite_resource_pool.web.id
}

resource "hyperlite_acl" "on_storage" {
  subject_type  = "user"
  subject       = hyperlite_user.ci.username
  role          = hyperlite_role.tf.key
  resource_type = "storage"
  resource      = "default"
}
`

// TestAccAccess: an account, a group with it, a custom role, a pool and two assignments, changed and imported, on a
// real Hyperlite.
func TestAccAccess(t *testing.T) {
	changed := strings.Replace(strings.Replace(accAccess, `"network.assign"]`, `"network.assign", "vm.options"]`, 1), `acceptance tests"`, `acceptance tests, changed"`, 1)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: accAccess,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperlite_user.ci", "role", "viewer"),
					resource.TestCheckResourceAttr("hyperlite_group.ops", "members.#", "1"),
					resource.TestMatchResourceAttr("hyperlite_role.tf", "key", regexp.MustCompile(`^custom:\d+$`)),
					resource.TestCheckResourceAttr("hyperlite_acl.on_storage", "resource_type", "storage"),
				),
			},
			{Config: accAccess, PlanOnly: true},
			{
				// The data sources find by name what the step above made, and the predefined roles.
				Config: accAccess + `
data "hyperlite_user" "ci" { username = "tfacc-ci" }
data "hyperlite_group" "ops" { name = "tfacc-ops" }
data "hyperlite_role" "tf" { name = "tfacc-terraform" }
data "hyperlite_role" "operator" { name = "operator" }
data "hyperlite_resource_pool" "web" { name = "tfacc-web" }
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.hyperlite_user.ci", "role", "viewer"),
					resource.TestCheckResourceAttrPair("data.hyperlite_group.ops", "id", "hyperlite_group.ops", "id"),
					resource.TestCheckTypeSetElemAttr("data.hyperlite_group.ops", "members.*", "tfacc-ci"),
					resource.TestCheckResourceAttrPair("data.hyperlite_role.tf", "key", "hyperlite_role.tf", "key"),
					resource.TestCheckResourceAttr("data.hyperlite_role.tf", "custom", "true"),
					resource.TestCheckResourceAttr("data.hyperlite_role.operator", "key", "operator"),
					resource.TestCheckTypeSetElemAttr("data.hyperlite_role.operator", "privileges.*", "vm.power"),
					resource.TestCheckResourceAttrPair("data.hyperlite_resource_pool.web", "id", "hyperlite_resource_pool.web", "id"),
				),
			},
			{
				Config: changed,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperlite_role.tf", "privileges.#", "6"),
					resource.TestCheckResourceAttr("hyperlite_resource_pool.web", "description", "VMs of the acceptance tests, changed"),
				),
			},
			{ResourceName: "hyperlite_user.ci", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"password"}},
			{ResourceName: "hyperlite_group.ops", ImportState: true, ImportStateVerify: true},
			{ResourceName: "hyperlite_role.tf", ImportState: true, ImportStateVerify: true},
			{ResourceName: "hyperlite_resource_pool.web", ImportState: true, ImportStateVerify: true},
			{ResourceName: "hyperlite_acl.on_pool", ImportState: true, ImportStateVerify: true},
		},
	})
}

const accStorage = accProvider + `
resource "hyperlite_storage_pool" "dir" {
  name = "tfacc-dir"
  type = "dir"
  path = "/var/lib/libvirt/tfacc-dir"
}

resource "hyperlite_storage_volume" "data" {
  storage_pool = hyperlite_storage_pool.dir.name
  name         = "tfacc-data"
  size_gb      = 1
}
`

// TestAccStorage: a directory pool and a volume on it, imported and destroyed, on a real Hyperlite.
func TestAccStorage(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: accStorage,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperlite_storage_pool.dir", "state", "active"),
					resource.TestCheckResourceAttr("hyperlite_storage_volume.data", "volume", "tfacc-data.qcow2"),
					resource.TestCheckResourceAttr("hyperlite_storage_volume.data", "size_gb", "1"),
				),
			},
			{Config: accStorage, PlanOnly: true},
			{
				Config: accStorage + `data "hyperlite_storage_pool" "dir" { name = "tfacc-dir" }`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.hyperlite_storage_pool.dir", "type", "dir"),
					resource.TestCheckResourceAttr("data.hyperlite_storage_pool.dir", "state", "active"),
					resource.TestCheckResourceAttr("data.hyperlite_storage_pool.dir", "path", "/var/lib/libvirt/tfacc-dir"),
					resource.TestCheckResourceAttrSet("data.hyperlite_storage_pool.dir", "uuid"),
				),
			},
			{
				Config: strings.Replace(accStorage, `name = "tfacc-dir"`, `name = "tfacc-dir2"`, 1),
				Check:  resource.TestCheckResourceAttr("hyperlite_storage_pool.dir", "id", "tfacc-dir2"),
			},
			{Config: strings.Replace(accStorage, `name = "tfacc-dir"`, `name = "tfacc-dir2"`, 1), PlanOnly: true},
			{Config: accStorage},
			{ResourceName: "hyperlite_storage_pool.dir", ImportState: true, ImportStateVerify: true},
			{ResourceName: "hyperlite_storage_volume.data", ImportState: true, ImportStateVerify: true},
		},
	})
}

func accBackup(retention, extra string) string {
	return accProvider + `
resource "hyperlite_vm" "small" {
  name        = "tfacc-bk"
  vcpu        = 1
  memory_mb   = 512
  tags        = ["tfacc-bk"]
  wait_for_ip = "0s"

  cloud_init {
    user     = "tfacc"
    password = "Tfacc-pass-2026"
  }

  disk { size_gb = 3 }
  network_interface { network = "hyperlite-isolated" }
}

resource "hyperlite_vm_backup_schedule" "small" {
  vm              = hyperlite_vm.small.name
  frequency       = "daily"
  time            = "03:15"
  retention_count = ` + retention + `
  keep_weeks      = 4
}

resource "hyperlite_backup_job" "tagged" {
  name      = "tfacc-job"
  selection = "tag"
  value     = "tfacc-bk"
  frequency = "weekly"
  time      = "23:45"
}

resource "hyperlite_vm_snapshot" "before" {
  vm          = hyperlite_vm.small.name
  name        = "tfacc-before"
  description = "taken by the acceptance tests"
}
` + extra
}

// TestAccBackup: a VM's schedule, a grouped job and a snapshot, changed, imported and destroyed; the HA protection
// of a VM whose disk is not on shared storage is refused with the server's reason.
func TestAccBackup(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: factories,
		CheckDestroy:             accGone("tfacc-bk"),
		Steps: []resource.TestStep{
			{
				Config: accBackup("7", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperlite_vm_backup_schedule.small", "id", "tfacc-bk"),
					resource.TestCheckNoResourceAttr("hyperlite_vm_backup_schedule.small", "keep_days"),
					resource.TestCheckResourceAttr("hyperlite_backup_job.tagged", "active", "true"),
					resource.TestCheckResourceAttr("hyperlite_backup_job.tagged", "excluded.#", "0"),
					resource.TestCheckResourceAttr("hyperlite_vm_snapshot.before", "id", "tfacc-bk/tfacc-before"),
					resource.TestCheckResourceAttrSet("hyperlite_vm_snapshot.before", "created_at"),
				),
			},
			{Config: accBackup("7", ""), PlanOnly: true},
			{
				Config: accBackup("3", ""),
				Check:  resource.TestCheckResourceAttr("hyperlite_vm_backup_schedule.small", "retention_count", "3"),
			},
			{
				Config: accBackup("3", `data "hyperlite_vm" "small" { name = "tfacc-bk" }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("data.hyperlite_vm.small", "id", "hyperlite_vm.small", "id"),
					resource.TestCheckResourceAttr("data.hyperlite_vm.small", "node", "local"),
					resource.TestCheckResourceAttr("data.hyperlite_vm.small", "vcpu", "1"),
					resource.TestCheckResourceAttrPair("data.hyperlite_vm.small", "network_interface.0.mac", "hyperlite_vm.small", "network_interface.0.mac"),
				),
			},
			{ResourceName: "hyperlite_vm_backup_schedule.small", ImportState: true, ImportStateVerify: true},
			{ResourceName: "hyperlite_backup_job.tagged", ImportState: true, ImportStateVerify: true},
			{ResourceName: "hyperlite_vm_snapshot.before", ImportState: true, ImportStateVerify: true},
			{
				Config:      accBackup("3", `resource "hyperlite_vm_ha" "small" { vm = hyperlite_vm.small.name }`),
				ExpectError: regexp.MustCompile(`not on a shared\s+storage\s+pool`),
			},
		},
	})
}

func accTemplate(name string) string {
	return accProvider + `
resource "hyperlite_vm" "src" {
  name        = "tfacc-tplsrc"
  vcpu        = 1
  memory_mb   = 512
  wait_for_ip = "0s"

  cloud_init {
    user     = "tfacc"
    password = "Tfacc-pass-2026"
  }

  disk { size_gb = 3 }
  network_interface { network = "hyperlite-isolated" }
}

resource "hyperlite_template" "t" {
  name = "` + name + `"
  vm   = hyperlite_vm.src.name
}

data "hyperlite_template" "t" {
  name       = hyperlite_template.t.name
  depends_on = [hyperlite_template.t]
}
`
}

// TestAccTemplate: a template copied from a running VM (stopped for the copy, started again), read by the data
// source, renamed, imported and deleted, on a real Hyperlite.
func TestAccTemplate(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: factories,
		CheckDestroy: func(s *terraform.State) error {
			if err := accGone("tfacc-tplsrc")(s); err != nil {
				return err
			}
			for _, n := range []string{"tfacc-tpl", "tfacc-tpl2"} {
				if _, err := (&templateResource{}).template(context.Background(), accClient(), n); !errors.Is(err, errNotFound) {
					return fmt.Errorf("the template %s is still there after the destroy (%v)", n, err)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: accTemplate("tfacc-tpl"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperlite_template.t", "id", "tfacc-tpl"),
					resource.TestCheckResourceAttr("hyperlite_template.t", "keep_vm", "true"),
					resource.TestCheckResourceAttr("hyperlite_template.t", "vcpu", "1"),
					resource.TestCheckResourceAttr("hyperlite_template.t", "memory_mb", "512"),
					resource.TestCheckResourceAttr("data.hyperlite_template.t", "vm_source", "tfacc-tplsrc"),
					resource.TestCheckResourceAttr("data.hyperlite_template.t", "firmware", "bios"),
					resource.TestCheckResourceAttr("data.hyperlite_template.t", "disks.0.size_gb", "3"),
					func(*terraform.State) error {
						vm, err := accClient().vmByName(context.Background(), "tfacc-tplsrc", "")
						if err != nil {
							return err
						}
						if vm.State != "active" {
							return fmt.Errorf("the source VM is %s after the copy, not started again", vm.State)
						}
						return nil
					},
				),
			},
			{Config: accTemplate("tfacc-tpl"), PlanOnly: true},
			{
				Config: accTemplate("tfacc-tpl2"),
				Check:  resource.TestCheckResourceAttr("hyperlite_template.t", "id", "tfacc-tpl2"),
			},
			{Config: accTemplate("tfacc-tpl2"), PlanOnly: true},
			{ResourceName: "hyperlite_template.t", ImportState: true, ImportStateVerify: true},
		},
	})
}

// TestAccISO: an image uploaded to the library (the server checks its name, not its content), read by the data
// source, renamed, imported and deleted, on a real Hyperlite.
func TestAccISO(t *testing.T) {
	src := filepath.Join(t.TempDir(), "tfacc.iso")
	if err := os.WriteFile(src, make([]byte, 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	config := func(name string) string {
		return accProvider + `
resource "hyperlite_iso" "i" {
  name   = "` + name + `"
  source = "` + src + `"
}

data "hyperlite_iso" "i" {
  name       = hyperlite_iso.i.name
  depends_on = [hyperlite_iso.i]
}
`
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: factories,
		CheckDestroy: func(*terraform.State) error {
			for _, n := range []string{"tfacc-1.0.iso", "tfacc-1.1.iso"} {
				if _, err := (&isoResource{}).iso(context.Background(), accClient(), n); !errors.Is(err, errNotFound) {
					return fmt.Errorf("the ISO %s is still there after the destroy (%v)", n, err)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: config("tfacc-1.0.iso"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperlite_iso.i", "size_mb", "1"),
					resource.TestCheckResourceAttrPair("data.hyperlite_iso.i", "added_at", "hyperlite_iso.i", "added_at"),
				),
			},
			{Config: config("tfacc-1.0.iso"), PlanOnly: true},
			{
				Config: config("tfacc-1.1.iso"),
				Check:  resource.TestCheckResourceAttr("hyperlite_iso.i", "id", "tfacc-1.1.iso"),
			},
			{ResourceName: "hyperlite_iso.i", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"source"}},
		},
	})
}

const accHA = accProvider + `
resource "hyperlite_storage_pool" "nfs" {
  name            = "tfacc-nfs"
  type            = "netfs"
  nfs_host        = "127.0.0.1"
  nfs_export_path = "/srv/tfacc-nfs"
}

resource "hyperlite_vm" "ha" {
  name         = "tfacc-ha"
  vcpu         = 1
  memory_mb    = 512
  storage_pool = hyperlite_storage_pool.nfs.name
  wait_for_ip  = "0s"

  cloud_init {
    user     = "tfacc"
    password = "Tfacc-pass-2026"
  }

  disk { size_gb = 3 }
  network_interface { network = "hyperlite-isolated" }
}

resource "hyperlite_vm_ha" "ha" {
  vm = hyperlite_vm.ha.name
}
`

// TestAccHA: a VM whose disk is on an NFS pool (the runner exports one, .github/workflows/acceptance.yml) is
// protected, imported and unprotected, on a real Hyperlite: the refusal on local storage is TestAccBackup's.
func TestAccHA(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: factories,
		CheckDestroy:             accGone("tfacc-ha"),
		Steps: []resource.TestStep{
			{
				Config: accHA,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperlite_storage_pool.nfs", "state", "active"),
					resource.TestCheckResourceAttr("hyperlite_vm_ha.ha", "id", "tfacc-ha"),
					resource.TestCheckResourceAttr("hyperlite_vm_ha.ha", "node", "local"),
				),
			},
			{Config: accHA, PlanOnly: true},
			{ResourceName: "hyperlite_vm_ha.ha", ImportState: true, ImportStateVerify: true},
		},
	})
}
