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
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.hyperlite_node.n", "status", "online"),
				resource.TestCheckResourceAttr("data.hyperlite_node.n", "member", "true"),
				resource.TestCheckResourceAttr("data.hyperlite_network.lan", "address", "10.0.20.1"),
				resource.TestCheckResourceAttr("data.hyperlite_network.lan", "type", "nat"),
				resource.TestCheckResourceAttr("data.hyperlite_template.debian", "memory_mb", "2048"),
				resource.TestCheckResourceAttr("data.hyperlite_template.debian", "ssh_user", "debian"),
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
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config:      providerBlock(srv.URL, "hlt_ok") + `data "hyperlite_template" "x" { name = "nope" }`,
			ExpectError: regexp.MustCompile(`no template named "nope"`),
		}},
	})
}
