package provider

import (
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
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
