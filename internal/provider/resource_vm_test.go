package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func vmConfig(url, extra string) string {
	return providerBlock(url, "hlt_ok") + `
resource "hyperlite_vm" "web" {
` + extra + `
}
`
}

const cloudVM = `
  name      = "web-1"
  vcpu      = 2
  memory_mb = 2048
  tags      = ["web", "prod"]
  autostart = true

  cloud_init {
    user     = "debian"
    password = "s3cret-pass"
    ssh_keys = ["ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey ana@laptop"]
  }

  customize {
    hostname = "web-1"
    dns      = ["10.0.0.53"]
    network {
      interface = 1
      addresses = ["10.0.20.11/24"]
      gateway   = "10.0.20.1"
    }
  }

  disk { size_gb = 10 }
  disk { size_gb = 20 }

  network_interface { network = "default" }
  network_interface {
    network  = "lan"
    vlan_tag = 20
  }
`

func TestVMFromTheCloudImageIsMadeCustomisedAndStarted(t *testing.T) {
	f, srv := newFakeHyperlite(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: vmConfig(srv.URL, cloudVM),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperlite_vm.web", "state", "active"),
					resource.TestCheckResourceAttr("hyperlite_vm.web", "node", "local"),
					resource.TestCheckResourceAttr("hyperlite_vm.web", "disk.#", "2"),
					resource.TestCheckResourceAttr("hyperlite_vm.web", "disk.1.size_gb", "20"),
					resource.TestCheckResourceAttr("hyperlite_vm.web", "disk.0.storage_pool", "default"),
					resource.TestCheckResourceAttr("hyperlite_vm.web", "network_interface.#", "2"),
					resource.TestCheckResourceAttr("hyperlite_vm.web", "network_interface.1.vlan_tag", "20"),
					resource.TestCheckResourceAttrSet("hyperlite_vm.web", "network_interface.1.mac"),
					resource.TestCheckResourceAttr("hyperlite_vm.web", "autostart", "true"),
					resource.TestCheckResourceAttr("hyperlite_vm.web", "tags.#", "2"),
					// The first interface uses DHCP: its lease is the address.
					resource.TestCheckResourceAttr("hyperlite_vm.web", "ip_address", "192.168.199.10"),
					func(s *terraform.State) error {
						vm := f.vms["web-1"]
						if vm == nil {
							return fmt.Errorf("no VM web-1")
						}
						net := vm.CloudInit["network"].([]any)
						if len(net) != 1 || net[0].(map[string]any)["mac"] != vm.Ifaces[1].MAC {
							return fmt.Errorf("the static address went to %v, not to the second interface %s", net, vm.Ifaces[1].MAC)
						}
						if vm.CloudInit["hostname"] != "web-1" || vm.CloudInit["user"] != "debian" {
							return fmt.Errorf("cloud-init %v", vm.CloudInit)
						}
						if f.called("POST /vms?wait=false") != 1 {
							return fmt.Errorf("the creation was not a task: %v", f.calls)
						}
						return nil
					},
				),
			},
			// Nothing changed: a second plan is empty (the read gives back exactly what was written).
			{Config: vmConfig(srv.URL, cloudVM), PlanOnly: true},
		},
	})
}

func TestVMChangesThatNeedItStoppedStopItAndStartItAgain(t *testing.T) {
	f, srv := newFakeHyperlite(t)
	grown := regexp.MustCompile(`vcpu      = 2\n  memory_mb = 2048`).ReplaceAllString(cloudVM, "vcpu      = 4\n  memory_mb = 4096")
	grown = regexp.MustCompile(`disk \{ size_gb = 20 \}`).ReplaceAllString(grown, "disk { size_gb = 30 }\n  disk { size_gb = 5 }")
	grown = regexp.MustCompile(`network  = "lan"`).ReplaceAllString(grown, `network  = "dmz"`)
	renamed := regexp.MustCompile(`name      = "web-1"`).ReplaceAllString(grown, `name      = "web-2"`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{Config: vmConfig(srv.URL, cloudVM)},
			{
				Config: vmConfig(srv.URL, grown),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperlite_vm.web", "vcpu", "4"),
					resource.TestCheckResourceAttr("hyperlite_vm.web", "memory_mb", "4096"),
					resource.TestCheckResourceAttr("hyperlite_vm.web", "disk.#", "3"),
					resource.TestCheckResourceAttr("hyperlite_vm.web", "disk.1.size_gb", "30"),
					resource.TestCheckResourceAttr("hyperlite_vm.web", "disk.2.target", "sdc"),
					resource.TestCheckResourceAttr("hyperlite_vm.web", "network_interface.1.network", "dmz"),
					resource.TestCheckResourceAttr("hyperlite_vm.web", "state", "active"),
					func(s *terraform.State) error {
						mac := s.RootModule().Resources["hyperlite_vm.web"].Primary.Attributes["network_interface.1.mac"]
						if f.vms["web-1"].Ifaces[1].MAC != mac || f.called("PUT /vms/web-1/network?mac="+mac) != 1 {
							return fmt.Errorf("the second interface was not moved in place: %v", f.calls)
						}
						return nil
					},
				),
			},
			{
				Config: vmConfig(srv.URL, renamed),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperlite_vm.web", "name", "web-2"),
					resource.TestCheckResourceAttr("hyperlite_vm.web", "state", "active"),
				),
			},
			// A disk never shrinks: refused at plan, nothing applied.
			{
				Config:      vmConfig(srv.URL, regexp.MustCompile(`disk \{ size_gb = 30 \}`).ReplaceAllString(renamed, "disk { size_gb = 25 }")),
				ExpectError: regexp.MustCompile(`A disk does not shrink`),
			},
			// A disk removed is detached and its volume deleted.
			{
				Config: vmConfig(srv.URL, regexp.MustCompile(`\n  disk \{ size_gb = 5 \}`).ReplaceAllString(renamed, "")),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperlite_vm.web", "disk.#", "2"),
					func(s *terraform.State) error {
						if len(f.volumes) != 0 {
							return fmt.Errorf("volumes left: %v", f.volumes)
						}
						return nil
					},
				),
			},
		},
		CheckDestroy: func(s *terraform.State) error {
			if len(f.vms) != 0 {
				return fmt.Errorf("VMs left after destroy: %v", f.vms)
			}
			return nil
		},
	})
}

func TestVMFromATemplateAndItsImport(t *testing.T) {
	f, srv := newFakeHyperlite(t)
	config := vmConfig(srv.URL, `
  name      = "app-1"
  vcpu      = 2
  memory_mb = 2048
  resource_pool = 7
  clone { template = "debian-12-base" }
  cloud_init { user = "debian" }
  disk { size_gb = 16 }
  network_interface { network = "lan" }
`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperlite_vm.web", "disk.0.size_gb", "16"),
					resource.TestCheckResourceAttr("hyperlite_vm.web", "network_interface.0.network", "lan"),
					func(s *terraform.State) error {
						if f.called("POST /templates/debian-12-base/deploy?wait=false") != 1 {
							return fmt.Errorf("not deployed from the template: %v", f.calls)
						}
						if got := f.pools[7]; len(got) != 1 || got[0] != "app-1" {
							return fmt.Errorf("pool 7 holds %v", got)
						}
						return nil
					},
				),
			},
			{
				ResourceName:            "hyperlite_vm.web",
				ImportState:             true,
				ImportStateId:           "local/app-1",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"clone", "cloud_init", "resource_pool", "timeouts"},
			},
		},
	})
}

func TestVMConfigurationsThatCannotWorkAreRefusedAtPlan(t *testing.T) {
	_, srv := newFakeHyperlite(t)
	cases := map[string]string{
		`An account is needed`: `
  name = "x"
  vcpu = 1
  memory_mb = 512
  disk { size_gb = 3 }
  network_interface { network = "default" }`,
		`No cloud-init on an ISO installation`: `
  name = "x"
  vcpu = 1
  memory_mb = 512
  iso = "debian.iso"
  customize { hostname = "x" }
  disk { size_gb = 3 }
  network_interface { network = "default" }`,
		`No such interface`: `
  name = "x"
  vcpu = 1
  memory_mb = 512
  cloud_init {
    user = "u"
    password = "pass1234"
  }
  customize {
    network {
      interface = 3
      addresses = ["10.0.0.2/24"]
    }
  }
  disk { size_gb = 3 }
  network_interface { network = "default" }`,
		`Not a duration`: `
  name = "x"
  vcpu = 1
  memory_mb = 512
  shutdown_timeout = "soon"
  iso = "debian.iso"
  disk { size_gb = 3 }
  network_interface { network = "default" }`,
	}
	for want, body := range cases {
		t.Run(want, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: factories,
				Steps:                    []resource.TestStep{{Config: vmConfig(srv.URL, body), PlanOnly: true, ExpectError: regexp.MustCompile(want)}},
			})
		})
	}
}
