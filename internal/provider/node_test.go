package provider

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func clusterConfig(url, node string) string {
	return providerBlock(url, "hlt_ok") + `
resource "hyperlite_vm" "web" {
  name      = "web-1"
  node      = "` + node + `"
  vcpu      = 1
  memory_mb = 512
  disk { size_gb = 3 }
  network_interface { network = "default" }
  cloud_init {
    user     = "debian"
    password = "s3cret-pass"
  }
}

resource "hyperlite_vm_backup_schedule" "s" {
  vm        = hyperlite_vm.web.name
  node      = hyperlite_vm.web.node
  frequency = "daily"
  time      = "03:00"
}

data "hyperlite_iso" "debian" {
  name = "debian.iso"
  node = "pve-b"
}
`
}

// Every object is made, read and changed on the member its `node` names, through the node the provider talks to; a
// VM moved to another member is migrated, not made again; `local` and that node's own name are one node, so writing
// one for the other moves nothing (Terraform still shows the new spelling: a configured value is planned as written).
func TestEachObjectIsOnTheMemberItsNodeNames(t *testing.T) {
	members, srv := newFakeCluster(t, "pve-b")
	here, there := members["hv-a"], members["pve-b"]
	there.isos["debian.iso"] = []byte("ISO")
	var uuid string
	on := func(member *fakeHyperliteAPI, other *fakeHyperliteAPI) resource.TestCheckFunc {
		return func(*terraform.State) error {
			vm := member.vms["web-1"]
			if vm == nil || other.vms["web-1"] != nil {
				return fmt.Errorf("web-1 is not on %s alone", member.self)
			}
			if vm.Schedule == nil {
				return fmt.Errorf("web-1 has no backup schedule on %s", member.self)
			}
			if uuid == "" {
				uuid = vm.UUID
			} else if vm.UUID != uuid {
				return fmt.Errorf("web-1 was made again instead of migrated")
			}
			return nil
		}
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: clusterConfig(srv.URL, "pve-b"),
				Check: resource.ComposeAggregateTestCheckFunc(
					on(there, here),
					resource.TestCheckResourceAttr("hyperlite_vm.web", "node", "pve-b"),
					resource.TestCheckResourceAttr("hyperlite_vm_backup_schedule.s", "node", "pve-b"),
					resource.TestCheckResourceAttr("data.hyperlite_iso.debian", "id", "debian.iso"),
					resource.TestCheckResourceAttr("data.hyperlite_iso.debian", "node", "pve-b"),
				),
			},
			{ResourceName: "hyperlite_vm_backup_schedule.s", ImportState: true, ImportStateId: "pve-b/web-1", ImportStateVerify: true},
			{
				Config: clusterConfig(srv.URL, "hv-a"),
				Check: resource.ComposeAggregateTestCheckFunc(
					on(here, there),
					resource.TestCheckResourceAttr("hyperlite_vm.web", "node", "hv-a"),
				),
			},
			{
				Config: clusterConfig(srv.URL, "local"),
				Check: resource.ComposeAggregateTestCheckFunc(
					on(here, there),
					resource.TestCheckResourceAttr("hyperlite_vm.web", "node", "local"),
					func(*terraform.State) error {
						// One migration, the one sent to pve-b; none run on hv-a itself.
						if n, sent := here.called("POST /vms/web-1/migrate"), here.called("POST /vms/web-1/migrate?node=pve-b"); n != 1 || sent != 1 {
							return fmt.Errorf("%d migrations, %d of them sent to pve-b: 1 expected, sent to pve-b", n, sent)
						}
						return nil
					},
				),
			},
		},
	})
}

func TestANodeThatIsNoMemberIsNamed(t *testing.T) {
	members, srv := newFakeCluster(t, "pve-b")
	members["pve-b"].isos["debian.iso"] = []byte("ISO")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config:      clusterConfig(srv.URL, "pve-z"),
			ExpectError: regexp.MustCompile(`pve-z is not a member`),
		}},
	})
}

// A Hyperlite before 1.3 ignores `?node=` on most routes and would act on itself: such a call is refused, nothing is
// sent.
func TestANodeIsRefusedByAHyperliteThatWouldActOnItself(t *testing.T) {
	f, srv := newFakeHyperlite(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config:      clusterConfig(srv.URL, "pve-b"),
			ExpectError: regexp.MustCompile(`needs Hyperlite 1.3 or later`),
		}},
	})
	if n := f.called("GET /isos") + f.called("POST /vms"); n != 0 {
		t.Fatalf("%d calls sent that the server would have run on itself", n)
	}
}

// Every call names the client's member: one made on the bare API client would reach the node the provider talks to,
// whatever the object's `node`.
func TestEveryCallGoesThroughTheMembersClient(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	bare := regexp.MustCompile(`\.API\.(Call|WaitTask|Get\w*|HTTPClient)\(|\.API\.URL\b`)
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") || name == "provider.go" {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if bare.MatchString(line) {
				t.Errorf("%s:%d calls the API directly; use Client.call, Client.post or Client.waitTask", name, i+1)
			}
		}
	}
}

// An image is uploaded to the member `node` names, through the node the provider talks to, and renamed there.
func TestAnImageIsUploadedToItsMember(t *testing.T) {
	members, srv := newFakeCluster(t, "pve-b")
	here, there := members["hv-a"], members["pve-b"]
	source := filepath.Join(t.TempDir(), "debian.iso")
	if err := os.WriteFile(source, []byte("ISO image"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := func(name string) string {
		return providerBlock(srv.URL, "hlt_ok") + fmt.Sprintf(`
resource "hyperlite_iso" "debian" {
  name   = %q
  node   = "pve-b"
  source = %q
}
`, name, source)
	}
	only := func(name string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			if string(there.isos[name]) != "ISO image" || len(there.isos) != 1 || len(here.isos) != 0 {
				return fmt.Errorf("%s is not on pve-b alone: pve-b has %d, hv-a %d", name, len(there.isos), len(here.isos))
			}
			return nil
		}
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{Config: config("debian.iso"), Check: resource.ComposeAggregateTestCheckFunc(only("debian.iso"),
				resource.TestCheckResourceAttr("hyperlite_iso.debian", "node", "pve-b"))},
			{
				ResourceName: "hyperlite_iso.debian", ImportState: true, ImportStateId: "pve-b/debian.iso",
				ImportStateVerify: true, ImportStateVerifyIgnore: []string{"source"},
			},
			{Config: config("debian-13.iso"), Check: only("debian-13.iso")},
		},
	})
}
