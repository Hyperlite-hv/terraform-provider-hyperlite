package provider

import (
	"context"
	"fmt"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// base is what every data source shares: the client the provider configured.
type base struct {
	client *Client
}

func (b *base) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	c, err := clientOf(req.ProviderData)
	if err != nil {
		resp.Diagnostics.AddError("Provider not configured", err.Error())
		return
	}
	b.client = c
}

// list reads a list of API v1 ({"items": [...]}) into items.
func list[T any](ctx context.Context, c *Client, path string) ([]T, error) {
	var page struct {
		Items []T `json:"items"`
	}
	if err := c.API.Call(ctx, "GET", path, nil, &page); err != nil {
		return nil, err
	}
	return page.Items, nil
}

// ---- hyperlite_node ----

type nodeDataSource struct{ base }

type nodeModel struct {
	Name     types.String `tfsdk:"name"`
	ID       types.String `tfsdk:"id"`
	Hostname types.String `tfsdk:"hostname"`
	Status   types.String `tfsdk:"status"`
	Member   types.Bool   `tfsdk:"member"`
}

func newNodeDataSource() datasource.DataSource { return &nodeDataSource{} }

func (d *nodeDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_node"
}

func (d *nodeDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A node of the Hyperlite cluster, to place resources on it. `local` is the node the provider talks to.",
		Attributes: map[string]schema.Attribute{
			"name":     schema.StringAttribute{Required: true, Description: "The node's name, or `local`."},
			"id":       schema.StringAttribute{Computed: true, Description: "The node's name."},
			"hostname": schema.StringAttribute{Computed: true, Description: "The address Hyperlite reaches it at."},
			"status":   schema.StringAttribute{Computed: true, Description: "`online`, `offline` or `unknown`."},
			"member": schema.BoolAttribute{
				Computed:    true,
				Description: "A member of the cluster (it runs Hyperlite), not a node driven over SSH only.",
			},
		},
	}
}

func (d *nodeDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m nodeModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := m.Name.ValueString()
	m.ID = types.StringValue(name)
	if name == "local" {
		m.Hostname, m.Status, m.Member = types.StringNull(), types.StringValue("online"), types.BoolValue(true)
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
		return
	}
	nodes, err := list[struct {
		Name     string `json:"name"`
		Hostname string `json:"hostname"`
		Status   string `json:"status"`
		Member   bool   `json:"member"`
	}](ctx, d.client, "/nodes")
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the nodes", err.Error())
		return
	}
	for _, n := range nodes {
		if n.Name == name {
			m.Hostname, m.Status, m.Member = types.StringValue(n.Hostname), types.StringValue(n.Status), types.BoolValue(n.Member)
			resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
			return
		}
	}
	resp.Diagnostics.AddError("No such node", fmt.Sprintf("Hyperlite has no node named %q.", name))
}

// ---- hyperlite_network ----

type networkDataSource struct{ base }

type networkModel struct {
	Name      types.String `tfsdk:"name"`
	ID        types.String `tfsdk:"id"`
	UUID      types.String `tfsdk:"uuid"`
	Type      types.String `tfsdk:"type"`
	Active    types.Bool   `tfsdk:"active"`
	Autostart types.Bool   `tfsdk:"autostart"`
	Bridge    types.String `tfsdk:"bridge"`
	Address   types.String `tfsdk:"address"`
	Netmask   types.String `tfsdk:"netmask"`
	DHCP      types.Bool   `tfsdk:"dhcp"`
	VLAN      types.Bool   `tfsdk:"vlan"`
}

func newNetworkDataSource() datasource.DataSource { return &networkDataSource{} }

func (d *networkDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_network"
}

func (d *networkDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	computed := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, Description: desc}
	}
	resp.Schema = schema.Schema{
		Description: "A virtual network, to connect VM interfaces to it.",
		Attributes: map[string]schema.Attribute{
			"name":      schema.StringAttribute{Required: true, Description: "The network's name."},
			"id":        computed("The network's name."),
			"uuid":      computed("The network's libvirt UUID."),
			"type":      computed("`nat`, `isolated`, `bridge`, `macvtap`..."),
			"active":    schema.BoolAttribute{Computed: true, Description: "Started."},
			"autostart": schema.BoolAttribute{Computed: true, Description: "Started with its node."},
			"bridge":    computed("The host bridge or interface it sits on."),
			"address":   computed("The gateway address of a NAT or isolated network."),
			"netmask":   computed("Its netmask."),
			"dhcp":      schema.BoolAttribute{Computed: true, Description: "Hyperlite hands out addresses on it."},
			"vlan":      schema.BoolAttribute{Computed: true, Description: "It carries VLAN tags (an interface may set `vlan_tag`)."},
		},
	}
}

func (d *networkDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m networkModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var n struct {
		Name      string `json:"name"`
		UUID      string `json:"uuid"`
		Type      string `json:"type"`
		Active    bool   `json:"active"`
		Autostart bool   `json:"autostart"`
		Bridge    string `json:"bridge"`
		DHCP      bool   `json:"dhcp"`
		VLAN      bool   `json:"vlan"`
		Network   *struct {
			Address string `json:"address"`
			Netmask string `json:"netmask"`
		} `json:"network"`
	}
	if err := d.client.API.Call(ctx, "GET", "/networks/"+url.PathEscape(m.Name.ValueString()), nil, &n); err != nil {
		resp.Diagnostics.AddError("Cannot read the network", err.Error())
		return
	}
	m.ID, m.UUID, m.Type = types.StringValue(n.Name), types.StringValue(n.UUID), types.StringValue(n.Type)
	m.Active, m.Autostart, m.DHCP, m.VLAN = types.BoolValue(n.Active), types.BoolValue(n.Autostart), types.BoolValue(n.DHCP), types.BoolValue(n.VLAN)
	m.Bridge = optional(n.Bridge)
	m.Address, m.Netmask = types.StringNull(), types.StringNull()
	if n.Network != nil {
		m.Address, m.Netmask = optional(n.Network.Address), optional(n.Network.Netmask)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// optional is null for an empty value: what the API leaves out is not an empty string in the state.
func optional(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

// ---- hyperlite_template ----

type templateDataSource struct{ base }

type templateModel struct {
	Name      types.String `tfsdk:"name"`
	ID        types.String `tfsdk:"id"`
	VCPU      types.Int64  `tfsdk:"vcpu"`
	MemoryMB  types.Int64  `tfsdk:"memory_mb"`
	VMSource  types.String `tfsdk:"vm_source"`
	SSHUser   types.String `tfsdk:"ssh_user"`
	OS        types.String `tfsdk:"os"`
	CreatedAt types.String `tfsdk:"created_at"`
}

func newTemplateDataSource() datasource.DataSource { return &templateDataSource{} }

func (d *templateDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_template"
}

func (d *templateDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A VM template, to size and make VMs from it.",
		Attributes: map[string]schema.Attribute{
			"name":       schema.StringAttribute{Required: true, Description: "The template's name."},
			"id":         schema.StringAttribute{Computed: true, Description: "The template's name."},
			"vcpu":       schema.Int64Attribute{Computed: true, Description: "Virtual CPUs of the VM it was made from."},
			"memory_mb":  schema.Int64Attribute{Computed: true, Description: "Memory of the VM it was made from, in MB."},
			"vm_source":  schema.StringAttribute{Computed: true, Description: "The VM it was made from."},
			"ssh_user":   schema.StringAttribute{Computed: true, Description: "The account its system has, if known."},
			"os":         schema.StringAttribute{Computed: true, Description: "Its system, if known."},
			"created_at": schema.StringAttribute{Computed: true, Description: "When it was made."},
		},
	}
}

func (d *templateDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m templateModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	templates, err := list[struct {
		Name      string `json:"name"`
		VCPU      int64  `json:"vcpu"`
		MemoryMB  int64  `json:"memory_mb"`
		VMSource  string `json:"vm_source"`
		SSHUser   string `json:"ssh_user"`
		OSLabel   string `json:"os_label"`
		CreatedAt string `json:"created_at"`
	}](ctx, d.client, "/templates")
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the templates", err.Error())
		return
	}
	for _, t := range templates {
		if t.Name != m.Name.ValueString() {
			continue
		}
		m.ID, m.VCPU, m.MemoryMB = types.StringValue(t.Name), types.Int64Value(t.VCPU), types.Int64Value(t.MemoryMB)
		m.VMSource, m.SSHUser, m.OS, m.CreatedAt = optional(t.VMSource), optional(t.SSHUser), optional(t.OSLabel), optional(t.CreatedAt)
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
		return
	}
	resp.Diagnostics.AddError("No such template", fmt.Sprintf("Hyperlite has no template named %q.", m.Name.ValueString()))
}
