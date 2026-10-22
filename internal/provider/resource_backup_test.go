package provider

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// A value the server would store otherwise (a lowercased tag, a dropped value, a name it refuses) is refused at
// plan: Terraform would report the difference as an error of the provider after the apply.
func TestValuesTheServerWouldChangeAreRefusedAtPlan(t *testing.T) {
	_, srv := newFakeHyperlite(t)
	job := func(selection, value string) string {
		return `
resource "hyperlite_backup_job" "j" {
  name      = "j"
  selection = "` + selection + `"
  value     = ` + value + `
  frequency = "daily"
  time      = "01:00"
}`
	}
	cases := map[string]string{
		`Invalid tag`:    job("tag", `"Prod"`),
		`Value not used`: job("all", `"x"`),
		`Invalid pool`:   job("pool", `"web"`),
		`HH:MM, on 24 hours`: `resource "hyperlite_vm_backup_schedule" "s" {
  vm        = "a"
  frequency = "daily"
  time      = "3:00"
}`,
		`must be between 1 and 120`: `resource "hyperlite_vm_backup_schedule" "s" {
  vm          = "a"
  frequency   = "daily"
  time        = "03:00"
  keep_months = 200
}`,
		`2 to 63 letters, digits or dashes`: `resource "hyperlite_vm_snapshot" "s" {
  vm   = "a"
  name = "before_upgrade"
}`,
		`1 to 32 lowercase letters`: `resource "hyperlite_vm" "v" {
  name      = "vm-x"
  vcpu      = 1
  memory_mb = 512
  iso       = "debian.iso"
  tags      = ["Web"]
  disk { size_gb = 3 }
  network_interface { network = "default" }
}`,
	}
	customized := func(customize string) string {
		return `resource "hyperlite_vm" "v" {
  name      = "vm-x"
  vcpu      = 1
  memory_mb = 512
  cloud_init {
    user     = "ops"
    password = "Harbor-Lantern-2026"
  }
  customize {
    ` + customize + `
  }
  disk { size_gb = 3 }
  network_interface { network = "default" }
}`
	}
	cases["as a DNS label"] = customized(`hostname = "Web01"`)
	cases["without a final dot"] = customized(`domain = "example.org."`)
	cases["Lowercase DNS labels"] = customized(`dns_search = ["Corp.Example"]`)
	cases["Write it as `2001:db8::1`"] = customized(`dns = ["2001:db8:0::1"]`)
	cases["An IPv4 or IPv6 address"] = customized(`dns = ["dns.example"]`)
	cases["with its prefix length"] = customized(`network { addresses = ["10.0.20.11"] }`)
	cases["Gateway out of reach"] = customized(`network {
      addresses = ["10.0.20.11/24"]
      gateway   = "10.0.30.1"
    }`)
	cases["a MAC address"] = `resource "hyperlite_network_reservation" "r" {
  network = "lab"
  mac     = "52-54-00-12-34-56"
  ip      = "10.0.20.50"
}`
	cases["An IPv4 address is expected"] = `resource "hyperlite_network" "n" {
  name       = "lab"
  mode       = "nat"
  address    = "10.0.20.1"
  dhcp_start = "10.0.20.100"
  dhcp_end   = "fd00::1"
}`
	for want, body := range cases {
		t.Run(want, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: factories,
				Steps: []resource.TestStep{{
					Config:      providerBlock(srv.URL, "hlt_ok") + body,
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(want),
				}},
			})
		})
	}
}

// The server reports the directory it writes in, the default one included: a schedule or a job without target_dir
// plans no change once applied, and a directory removed from the configuration goes back to the default one.
func TestTargetDirLeftOutIsTheServersDefault(t *testing.T) {
	f, srv := newFakeHyperlite(t)
	f.newVM("a", 1, 512, []float64{3}, "default", "default", false)
	config := func(dir string) string {
		return providerBlock(srv.URL, "hlt_ok") + `
resource "hyperlite_vm_backup_schedule" "s" {
  vm         = "a"
  frequency  = "daily"
  time       = "03:00"
  ` + dir + `
}

resource "hyperlite_backup_job" "j" {
  name      = "nightly"
  selection = "all"
  frequency = "daily"
  time      = "01:00"
  ` + dir + `
}`
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: config(""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperlite_vm_backup_schedule.s", "target_dir", fakeBackupDir),
					resource.TestCheckResourceAttr("hyperlite_backup_job.j", "target_dir", fakeBackupDir),
				),
			},
			{
				Config: config(`target_dir = "/mnt/nas/backups"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperlite_vm_backup_schedule.s", "target_dir", "/mnt/nas/backups"),
					resource.TestCheckResourceAttr("hyperlite_backup_job.j", "target_dir", "/mnt/nas/backups"),
				),
			},
			{
				Config: config(""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperlite_vm_backup_schedule.s", "target_dir", fakeBackupDir),
					resource.TestCheckResourceAttr("hyperlite_backup_job.j", "target_dir", fakeBackupDir),
				),
			},
			{ResourceName: "hyperlite_backup_job.j", ImportState: true, ImportStateVerify: true},
			{ResourceName: "hyperlite_vm_backup_schedule.s", ImportState: true, ImportStateVerify: true},
		},
	})
}

// The main rules of the server's password policy are refused at plan.
func TestPasswordsThePolicyRefusesAreRefusedAtPlan(t *testing.T) {
	_, srv := newFakeHyperlite(t)
	cases := map[string]string{
		`at least 12 characters`:       "Short-2026",
		`must not contain the account`: "Tfacc-Ci-Harbor-2026",
		`too repetitive`:               "abababababab",
	}
	for want, password := range cases {
		t.Run(want, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: factories,
				Steps: []resource.TestStep{{
					Config: providerBlock(srv.URL, "hlt_ok") + `
resource "hyperlite_user" "u" {
  username = "tfacc-ci"
  password = "` + password + `"
}`,
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(want),
				}},
			})
		})
	}
}

// An ISO image is uploaded from a local file (streamed, multipart), renamed in place, uploaded again when its
// source_hash changes, and imported without being uploaded again.
func TestISOIsUploadedRenamedAndImported(t *testing.T) {
	f, srv := newFakeHyperlite(t)
	src := filepath.Join(t.TempDir(), "netinst.iso")
	if err := os.WriteFile(src, []byte("an ISO image"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := func(name, hash string) string {
		return providerBlock(srv.URL, "hlt_ok") + `
resource "hyperlite_iso" "d" {
  name        = "` + name + `"
  source      = "` + src + `"
  source_hash = "` + hash + `"
}`
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: config("debian-13.iso", "a"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperlite_iso.d", "id", "debian-13.iso"),
					func(*terraform.State) error {
						if string(f.isos["debian-13.iso"]) != "an ISO image" {
							return fmt.Errorf("the library holds %q", f.isos["debian-13.iso"])
						}
						return nil
					},
				),
			},
			{Config: config("debian-13.iso", "a"), PlanOnly: true},
			{
				Config: config("debian-13.1.iso", "a"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("hyperlite_iso.d", "id", "debian-13.1.iso"),
					func(*terraform.State) error {
						if f.called("POST /isos?") != 1 {
							return fmt.Errorf("a rename uploads nothing")
						}
						return nil
					},
				),
			},
			{
				Config: config("debian-13.1.iso", "b"),
				Check: func(*terraform.State) error {
					if f.called("POST /isos?") != 2 {
						return fmt.Errorf("a new source_hash uploads the image again")
					}
					return nil
				},
			},
			{ResourceName: "hyperlite_iso.d", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"source", "source_hash"}},
		},
	})
}

func TestAnISONameTheServerRefusesIsRefusedAtPlan(t *testing.T) {
	_, srv := newFakeHyperlite(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config: providerBlock(srv.URL, "hlt_ok") + `
resource "hyperlite_iso" "d" {
  name   = "debian 13.img"
  source = "/dev/null"
}`,
			PlanOnly:    true,
			ExpectError: regexp.MustCompile("ending with\\s+`\\.iso`"),
		}},
	})
}
