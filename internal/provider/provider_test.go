package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

var factories = map[string]func() (tfprotov6.ProviderServer, error){
	"hyperlite": providerserver.NewProtocol6WithError(New("test")()),
}

// fakeHyperlite answers what a Hyperlite 1.1 answers to the provider, for the token hlt_ok only.
func fakeHyperlite(t *testing.T, api int) *httptest.Server {
	t.Helper()
	answer := func(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			answer(w, map[string]any{"status": "ok", "api": api, "hyperlite_version": "1.1.1"})
			return
		}
		if r.Header.Get("Authorization") != "Bearer hlt_ok" {
			w.WriteHeader(http.StatusUnauthorized)
			answer(w, map[string]any{"detail": "Invalid or expired token", "code": "unauthorized"})
			return
		}
		switch r.URL.Path {
		case "/api/v1/auth/me":
			answer(w, map[string]any{"username": "terraform"})
		case "/api/v1/nodes":
			answer(w, map[string]any{"items": []any{map[string]any{"name": "node2", "hostname": "node2.example.internal", "status": "online", "member": true}}, "total": 1})
		case "/api/v1/networks/lan":
			answer(w, map[string]any{"name": "lan", "uuid": "u-1", "type": "nat", "active": true, "autostart": true, "bridge": "virbr1",
				"dhcp": true, "vlan": false, "network": map[string]any{"address": "10.0.20.1", "netmask": "255.255.255.0"}})
		case "/api/v1/templates":
			answer(w, map[string]any{"items": []any{map[string]any{"name": "debian-12-base", "vcpu": 2, "memory_mb": 2048, "vm_source": "base", "ssh_user": "debian", "os_label": "Debian 12"}}, "total": 1})
		case "/api/v1/vms/web":
			answer(w, map[string]any{"name": "web", "uuid": "11111111-2222-4333-8444-555555555555", "state": "active", "vcpu": 2, "memory_mb": 2048,
				"ip": "10.0.20.11", "firmware": "uefi"})
		case "/api/v1/vms/web/network":
			answer(w, map[string]any{"interfaces": []any{map[string]any{"mac": "52:54:00:aa:bb:01", "network": "lan", "vlan": nil}}})
		case "/api/v1/storage/fast":
			answer(w, map[string]any{"name": "fast", "uuid": "u-2", "type": "dir", "state": "active", "path": "/srv/fast", "autostart": true,
				"capacity_gb": 100.0, "allocation_gb": 40.5, "available_gb": 59.5})
		case "/api/v1/pools":
			answer(w, map[string]any{"items": []any{map[string]any{"id": 4, "name": "web", "description": "Front ends", "vms": []string{"web", "api"}}}, "total": 1})
		case "/api/v1/auth/users/ci":
			answer(w, map[string]any{"username": "ci", "role": "viewer"})
		case "/api/v1/groups":
			answer(w, map[string]any{"items": []any{map[string]any{"id": 3, "name": "ops", "members": []string{"ci"}}}, "total": 1})
		case "/api/v1/acl/roles":
			answer(w, map[string]any{"operator": map[string]any{"label": "Operator", "privileges": []string{"vm.view", "vm.power"}}})
		case "/api/v1/acl/custom-roles":
			answer(w, map[string]any{"items": []any{map[string]any{"key": "custom:9", "id": 9, "label": "deployer", "privileges": []string{"vm.allocate"}}}, "total": 1})
		case "/api/v1/isos":
			answer(w, map[string]any{"items": []any{map[string]any{"name": "debian-13.iso", "size_mb": 754.0, "added_at": "2026-10-01T08:00:00+00:00", "node": "local"}}, "total": 1})
		default:
			w.WriteHeader(http.StatusNotFound)
			answer(w, map[string]any{"detail": "Not found", "code": "not_found"})
		}
	}))
}

func providerBlock(url, token string) string {
	return `provider "hyperlite" {
  server    = "` + url + `"
  api_token = "` + token + `"
}
`
}

func TestDataSourcesReadWhatTheServerHolds(t *testing.T) {
	srv := fakeHyperlite(t, 1)
	defer srv.Close()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config: providerBlock(srv.URL, "hlt_ok") + `
data "hyperlite_node" "n" { name = "node2" }
data "hyperlite_network" "lan" { name = "lan" }
data "hyperlite_template" "debian" { name = "debian-12-base" }
data "hyperlite_vm" "web" { name = "web" }
data "hyperlite_storage_pool" "fast" { name = "fast" }
data "hyperlite_resource_pool" "web" { name = "web" }
data "hyperlite_user" "ci" { username = "ci" }
data "hyperlite_group" "ops" { name = "ops" }
data "hyperlite_role" "operator" { name = "operator" }
data "hyperlite_role" "deployer" { name = "deployer" }
data "hyperlite_iso" "debian" { name = "debian-13.iso" }
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.hyperlite_node.n", "status", "online"),
				resource.TestCheckResourceAttr("data.hyperlite_node.n", "member", "true"),
				resource.TestCheckResourceAttr("data.hyperlite_network.lan", "address", "10.0.20.1"),
				resource.TestCheckResourceAttr("data.hyperlite_network.lan", "type", "nat"),
				resource.TestCheckResourceAttr("data.hyperlite_template.debian", "memory_mb", "2048"),
				resource.TestCheckResourceAttr("data.hyperlite_template.debian", "ssh_user", "debian"),
				resource.TestCheckResourceAttr("data.hyperlite_vm.web", "id", "11111111-2222-4333-8444-555555555555"),
				resource.TestCheckResourceAttr("data.hyperlite_vm.web", "node", "local"),
				resource.TestCheckResourceAttr("data.hyperlite_vm.web", "network_interface.0.mac", "52:54:00:aa:bb:01"),
				resource.TestCheckResourceAttr("data.hyperlite_storage_pool.fast", "available_gb", "59.5"),
				resource.TestCheckResourceAttr("data.hyperlite_resource_pool.web", "id", "4"),
				resource.TestCheckTypeSetElemAttr("data.hyperlite_resource_pool.web", "vms.*", "api"),
				resource.TestCheckResourceAttr("data.hyperlite_user.ci", "role", "viewer"),
				resource.TestCheckResourceAttr("data.hyperlite_group.ops", "id", "3"),
				resource.TestCheckResourceAttr("data.hyperlite_role.operator", "key", "operator"),
				resource.TestCheckResourceAttr("data.hyperlite_role.operator", "custom", "false"),
				resource.TestCheckResourceAttr("data.hyperlite_role.deployer", "key", "custom:9"),
				resource.TestCheckTypeSetElemAttr("data.hyperlite_role.deployer", "privileges.*", "vm.allocate"),
				resource.TestCheckResourceAttr("data.hyperlite_iso.debian", "size_mb", "754"),
			),
		}},
	})
}

func TestAWrongTokenFailsAtConfigure(t *testing.T) {
	srv := fakeHyperlite(t, 1)
	defer srv.Close()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config:      providerBlock(srv.URL, "hlt_wrong") + `data "hyperlite_node" "n" { name = "local" }`,
			ExpectError: regexp.MustCompile(`API token is not accepted`),
		}},
	})
}

func TestAServerWithAnotherAPIIsRefused(t *testing.T) {
	srv := fakeHyperlite(t, 2)
	defer srv.Close()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config:      providerBlock(srv.URL, "hlt_ok") + `data "hyperlite_node" "n" { name = "local" }`,
			ExpectError: regexp.MustCompile(`speaks API v2`),
		}},
	})
}

func TestAMissingObjectIsNamed(t *testing.T) {
	srv := fakeHyperlite(t, 1)
	defer srv.Close()
	cases := map[string]string{
		`data "hyperlite_template" "x" { name = "nope" }`:      `no template named "nope"`,
		`data "hyperlite_vm" "x" { name = "nope" }`:            `no VM named "nope"`,
		`data "hyperlite_storage_pool" "x" { name = "nope" }`:  `no storage pool named "nope"`,
		`data "hyperlite_resource_pool" "x" { name = "nope" }`: `no pool named "nope"`,
		`data "hyperlite_user" "x" { username = "nope" }`:      `no account named "nope"`,
		`data "hyperlite_group" "x" { name = "nope" }`:         `no group named "nope"`,
		`data "hyperlite_role" "x" { name = "nope" }`:          `predefined roles are \["operator"\]`,
		`data "hyperlite_iso" "x" { name = "nope" }`:           `no ISO image named "nope"`,
	}
	for config, want := range cases {
		t.Run(want, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: factories,
				Steps: []resource.TestStep{{
					Config:      providerBlock(srv.URL, "hlt_ok") + config,
					ExpectError: regexp.MustCompile(want),
				}},
			})
		})
	}
}
