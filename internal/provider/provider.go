// Package provider is the Hyperlite provider: its configuration, resources and data sources
// (Hyperlite's docs/design/terraform-provider.md).
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Hyperlite-hv/Hyperlite/cli/hyperlite"
)

// New returns the provider of the given version.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return &hyperliteProvider{version: version} }
}

type hyperliteProvider struct {
	version string
}

type providerModel struct {
	Server             types.String `tfsdk:"server"`
	APIToken           types.String `tfsdk:"api_token"`
	Fingerprint        types.String `tfsdk:"fingerprint"`
	AllowUnverifiedSSL types.Bool   `tfsdk:"allow_unverified_ssl"`
}

func (p *hyperliteProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "hyperlite"
	resp.Version = p.version
}

func (p *hyperliteProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Hyperlite server or cluster through its API v1.",
		Attributes: map[string]schema.Attribute{
			"server": schema.StringAttribute{
				Description: "Address of a Hyperlite node, e.g. https://hyperlite.example.internal:8000. " +
					"Defaults to the HYPERLITE_SERVER environment variable.",
				Optional: true,
			},
			"api_token": schema.StringAttribute{
				Description: "A personal API token (hlt_...), created in Account security. " +
					"Defaults to the HYPERLITE_TOKEN environment variable.",
				Optional:  true,
				Sensitive: true,
			},
			"fingerprint": schema.StringAttribute{
				Description: "SHA-256 fingerprint of the server's certificate, when the system does not trust it " +
					"(a self-signed or internal certificate), as `hyperlite login` pins it. " +
					"Defaults to the HYPERLITE_FINGERPRINT environment variable.",
				Optional: true,
			},
			"allow_unverified_ssl": schema.BoolAttribute{
				Description: "Accept any certificate. Only for a lab: pin the certificate with `fingerprint` instead.",
				Optional:    true,
			},
		},
	}
}

// fromEnv is the attribute's value, or the environment variable's when it is not set in the configuration.
func fromEnv(v types.String, name string) string {
	if !v.IsNull() && !v.IsUnknown() {
		return v.ValueString()
	}
	return os.Getenv(name)
}

func (p *hyperliteProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if cfg.Server.IsUnknown() || cfg.APIToken.IsUnknown() {
		// Known only after an apply of something else: the provider is configured again then.
		return
	}
	server := strings.TrimRight(fromEnv(cfg.Server, "HYPERLITE_SERVER"), "/")
	token := fromEnv(cfg.APIToken, "HYPERLITE_TOKEN")
	fingerprint := fromEnv(cfg.Fingerprint, "HYPERLITE_FINGERPRINT")
	insecure := cfg.AllowUnverifiedSSL.ValueBool()
	if server == "" {
		resp.Diagnostics.AddAttributeError(path.Root("server"), "No Hyperlite server",
			"Set `server` in the provider block or the HYPERLITE_SERVER environment variable.")
	}
	if token == "" {
		resp.Diagnostics.AddAttributeError(path.Root("api_token"), "No API token",
			"Set `api_token` in the provider block or the HYPERLITE_TOKEN environment variable. "+
				"Create one in Hyperlite, Account security > API tokens.")
	}
	if insecure && fingerprint != "" {
		resp.Diagnostics.AddAttributeError(path.Root("allow_unverified_ssl"), "Two ways to trust the certificate",
			"`fingerprint` pins the certificate; `allow_unverified_ssl` accepts any. Keep `fingerprint`.")
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if !strings.Contains(server, "://") {
		server = "https://" + server
	}
	if insecure {
		resp.Diagnostics.AddWarning("Certificate not verified",
			"allow_unverified_ssl accepts any certificate: anyone on the path can read the token. Pin it with `fingerprint`.")
	}

	c := &hyperlite.Client{
		URL:         server,
		Token:       token,
		Fingerprint: fingerprint,
		Insecure:    insecure,
		UserAgent:   "terraform-provider-hyperlite/" + p.version,
	}
	client, err := connect(ctx, c)
	if err != nil {
		resp.Diagnostics.AddError("Cannot use the Hyperlite server", err.Error())
		return
	}
	resp.DataSourceData = client
	resp.ResourceData = client
}

// Client is what resources and data sources receive: the API client and the server's version.
type Client struct {
	API     *hyperlite.Client
	Version string
	// node is the member of the cluster every call names (?node=): empty for the node the provider talks to.
	node string
	// self is the name the node the provider talks to has in its cluster (GET /health): lists call its objects
	// "local", and a configuration may name it.
	self string
}

// sameNode tells whether two `node` values name the same member: "", "local" and the provider's own node's name are
// one node.
func (c *Client) sameNode(a, b string) bool { return c.canonical(a) == c.canonical(b) }

func (c *Client) canonical(node string) string {
	if node == "local" || (node != "" && node == c.self) {
		return ""
	}
	return node
}

// at is the client of one member's objects: each call names that member, which does the work itself (Hyperlite sends
// the request on, with the same token's user), and a task is followed there. "" or "local": the node the provider
// talks to.
func (c *Client) at(node string) *Client {
	node = c.canonical(node)
	if node == c.node {
		return c
	}
	cc := *c
	cc.node = node
	return &cc
}

// call is every API call of the provider: the route, on this client's member.
func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	if err := c.reaches(method, path); err != nil {
		return err
	}
	return c.API.Call(ctx, method, withNode(path, c.node), body, out)
}

// post sends a body that is not JSON (a file, streamed: never held in memory) to the route, on this client's member.
// No time limit but the context's.
func (c *Client) post(ctx context.Context, path, contentType string, body io.Reader) error {
	if err := c.reaches("POST", path); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.API.URL+hyperlite.APIPrefix+withNode(path, c.node), body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.API.Token)
	if c.API.UserAgent != "" {
		req.Header.Set("User-Agent", c.API.UserAgent)
	}
	hc := c.API.HTTPClient()
	hc.Timeout = 0
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("cannot send to %s: %w", c.API.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var payload struct {
			Detail any `json:"detail"`
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = json.Unmarshal(data, &payload)
		if d, ok := payload.Detail.(string); ok {
			return &hyperlite.APIError{Status: resp.StatusCode, Detail: d}
		}
		return &hyperlite.APIError{Status: resp.StatusCode}
	}
	return nil
}

// reaches refuses a call naming another member that the server would make on itself.
func (c *Client) reaches(method, path string) error {
	if c.node != "" && c.self == "" && !tookNode(method, path) {
		return fmt.Errorf("this Hyperlite (%s) would do this on its own node, not on %s: `node` needs Hyperlite 1.3 or later "+
			"for this object; leave it out or point the provider at %s", c.Version, c.node, c.node)
	}
	return nil
}

// before13 is what a Hyperlite before 1.3, which does not say its node's name, already did on the node `?node=`
// names (docs/api/openapi-v1.json of 1.2.0). It ignores the parameter everywhere else and would act on itself.
var before13 = regexp.MustCompile(`^(GET /vms(/[^/]+(/(boot|disks|network))?)?|PUT /vms/[^/]+/boot|` +
	`POST /vms/[^/]+/(start|stop|restart|rename|migrate)|DELETE /vms/[^/]+|GET /storage(/[^/]+)?|POST /storage|` +
	`DELETE /storage/[^/]+|DELETE /isos/[^/]+|GET /tasks)$`)

func tookNode(method, path string) bool {
	route, _, _ := strings.Cut(path, "?")
	return before13.MatchString(method + " " + route)
}

// waitTask follows a task of this client's member.
func (c *Client) waitTask(ctx context.Context, id string) error {
	_, err := c.API.WaitTask(ctx, id, hyperlite.WaitOptions{Node: c.node})
	return err
}

// connect checks, before any plan, that the server speaks API v1 and that the token is accepted, so a wrong address
// or token fails at once with its reason.
func connect(ctx context.Context, c *hyperlite.Client) (*Client, error) {
	health, err := c.GetHealth(ctx)
	if err != nil {
		return nil, err
	}
	if err := health.CheckAPI(); err != nil {
		return nil, err
	}
	var me struct {
		Username string `json:"username"`
	}
	if err := c.Call(ctx, "GET", "/auth/me", nil, &me); err != nil {
		return nil, fmt.Errorf("the API token is not accepted: %w", err)
	}
	return &Client{API: c, Version: health.HyperliteVersion, self: health.Node}, nil
}

func (p *hyperliteProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		newVMResource,
		newNetworkResource,
		newReservationResource,
		newPoolResource,
		newUserResource,
		newGroupResource,
		newRoleResource,
		newACLResource,
		newStoragePoolResource,
		newVolumeResource,
		newScheduleResource,
		newBackupJobResource,
		newSnapshotResource,
		newHAResource,
		newTemplateResource,
		newISOResource,
	}
}

func (p *hyperliteProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		newNodeDataSource,
		newNetworkDataSource,
		newTemplateDataSource,
		newVMDataSource,
		newStoragePoolDataSource,
		newResourcePoolDataSource,
		newUserDataSource,
		newGroupDataSource,
		newRoleDataSource,
		newISODataSource,
	}
}

// clientOf is the configured client, or a diagnostic when the provider data is not one (a bug of the provider).
func clientOf(data any) (*Client, error) {
	if data == nil {
		return nil, nil // not configured yet: the framework calls Configure again later
	}
	c, ok := data.(*Client)
	if !ok {
		return nil, fmt.Errorf("unexpected provider data %T: report this bug", data)
	}
	return c, nil
}
