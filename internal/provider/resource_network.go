package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// configured is what every resource of this provider embeds: the client, set by Configure.
type configured struct{ client *Client }

func (c *configured) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	cl, err := clientOf(req.ProviderData)
	if err != nil {
		resp.Diagnostics.AddError("Provider not configured", err.Error())
		return
	}
	c.client = cl
}

// ---- hyperlite_network ----

type networkResource struct{ configured }

type networkResourceModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	Mode      types.String `tfsdk:"mode"`
	Bridge    types.String `tfsdk:"bridge"`
	Address   types.String `tfsdk:"address"`
	Netmask   types.String `tfsdk:"netmask"`
	DHCPStart types.String `tfsdk:"dhcp_start"`
	DHCPEnd   types.String `tfsdk:"dhcp_end"`
	Autostart types.Bool   `tfsdk:"autostart"`
	UUID      types.String `tfsdk:"uuid"`
	Active    types.Bool   `tfsdk:"active"`
}

func newNetworkResource() resource.Resource { return &networkResource{} }

func (r *networkResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_network"
}

func (r *networkResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "A virtual network of the node: NAT or isolated with Hyperlite's DHCP, or a bridge onto a host bridge.",
		Attributes: map[string]schema.Attribute{
			"id":   schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "The network's name."},
			"name": schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.RegexMatches(nameRe, nameRule)}, Description: "Its name. A change renames it."},
			"mode": schema.StringAttribute{
				Required: true, Validators: []validator.String{stringvalidator.OneOf("nat", "isolated", "bridge")},
				Description: "`nat` (routed to the host's network), `isolated` (no way out) or `bridge` (onto a host bridge). Between `nat` and `isolated` it changes in place; to or from `bridge`, the network is replaced.",
			},
			"bridge": schema.StringAttribute{
				Optional: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Description: "For `bridge`: the host bridge (`br0`).",
			},
			"address":    schema.StringAttribute{Optional: true, Validators: []validator.String{ipv4Address()}, Description: "For `nat` and `isolated`: the gateway's address (`10.0.20.1`)."},
			"netmask":    schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keep, Validators: []validator.String{ipv4Address()}, Description: "Its netmask (`255.255.255.0` when absent)."},
			"dhcp_start": schema.StringAttribute{Optional: true, Validators: []validator.String{ipv4Address()}, Description: "First address Hyperlite's DHCP hands out (with `dhcp_end`; none: no DHCP)."},
			"dhcp_end":   schema.StringAttribute{Optional: true, Validators: []validator.String{ipv4Address()}, Description: "Last address of the DHCP range."},
			"autostart": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Started with the node.",
			},
			"uuid":   schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "Its libvirt UUID."},
			"active": schema.BoolAttribute{Computed: true, Description: "Started."},
		},
	}
}

func (r *networkResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m networkResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.Mode.IsUnknown() {
		return
	}
	if m.Mode.ValueString() == "bridge" {
		if m.Bridge.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("bridge"), "A host bridge is needed", "A `bridge` network names the host bridge it sits on.")
		}
		if !m.Address.IsNull() || !m.DHCPStart.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("address"), "No subnet on a bridge", "A `bridge` network takes the host network's addresses: leave `address` and the DHCP range out.")
		}
	} else if m.Address.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("address"), "An address is needed", "A `nat` or `isolated` network needs its gateway's `address`.")
	}
	if m.DHCPStart.IsNull() != m.DHCPEnd.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("dhcp_start"), "A DHCP range has two ends", "Give both `dhcp_start` and `dhcp_end`, or neither.")
	}
}

func (r *networkResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var plan, state networkResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if (plan.Mode.ValueString() == "bridge") != (state.Mode.ValueString() == "bridge") {
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root("mode"))
	}
}

func (r *networkResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m networkResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"name": m.Name.ValueString(), "mode": m.Mode.ValueString()}
	if m.Mode.ValueString() == "bridge" {
		body["bridge_name"] = m.Bridge.ValueString()
	} else {
		body["subnet_address"] = m.Address.ValueString()
		if !m.Netmask.IsNull() && !m.Netmask.IsUnknown() {
			body["subnet_netmask"] = m.Netmask.ValueString()
		}
		if !m.DHCPStart.IsNull() {
			body["dhcp_start"], body["dhcp_end"] = m.DHCPStart.ValueString(), m.DHCPEnd.ValueString()
		}
	}
	if err := r.client.API.Call(ctx, "POST", "/networks", body, nil); err != nil {
		resp.Diagnostics.AddError("Cannot create the network", err.Error())
		return
	}
	m.ID = m.Name
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), m.ID)...)
	if !m.Autostart.ValueBool() {
		if err := r.client.API.Call(ctx, "PUT", "/networks/"+esc(m.Name.ValueString())+"/autostart", map[string]any{"autostart": false}, nil); err != nil {
			resp.Diagnostics.AddError("The network was made but its start with the node could not be set", err.Error())
			return
		}
	}
	r.readInto(ctx, &m, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

type networkDetail struct {
	Name      string `json:"name"`
	UUID      string `json:"uuid"`
	Type      string `json:"type"`
	Active    bool   `json:"active"`
	Autostart bool   `json:"autostart"`
	Bridge    string `json:"bridge"`
	IPAM      struct {
		Mode    *string `json:"mode"`
		Address *string `json:"address"`
		Netmask *string `json:"netmask"`
		DHCP    *struct {
			Start string `json:"start"`
			End   string `json:"end"`
		} `json:"dhcp"`
		Reservations []struct {
			MAC  string  `json:"mac"`
			IP   string  `json:"ip"`
			Name *string `json:"name"`
		} `json:"reservations"`
	} `json:"ipam"`
}

func (c *Client) network(ctx context.Context, name string) (*networkDetail, error) {
	var n networkDetail
	if err := c.API.Call(ctx, "GET", "/networks/"+esc(name), nil, &n); err != nil {
		return nil, notFound(err)
	}
	return &n, nil
}

func (r *networkResource) readInto(ctx context.Context, m *networkResourceModel, diags interface{ AddError(string, string) }) bool {
	n, err := r.client.network(ctx, m.ID.ValueString())
	if err != nil {
		if !errors.Is(err, errNotFound) {
			diags.AddError("Cannot read the network", err.Error())
		}
		return false
	}
	m.Name, m.ID, m.UUID = types.StringValue(n.Name), types.StringValue(n.Name), types.StringValue(n.UUID)
	m.Active, m.Autostart = types.BoolValue(n.Active), types.BoolValue(n.Autostart)
	if n.Type == "bridge" || (n.IPAM.Mode != nil && *n.IPAM.Mode == "bridge") {
		m.Mode, m.Bridge = types.StringValue("bridge"), types.StringValue(n.Bridge)
		m.Address, m.Netmask, m.DHCPStart, m.DHCPEnd = types.StringNull(), types.StringNull(), types.StringNull(), types.StringNull()
		return true
	}
	if n.IPAM.Mode != nil {
		m.Mode = types.StringValue(*n.IPAM.Mode)
	}
	m.Address, m.Netmask = optionalString(n.IPAM.Address, types.StringNull()), optionalString(n.IPAM.Netmask, types.StringNull())
	if n.IPAM.DHCP != nil {
		m.DHCPStart, m.DHCPEnd = types.StringValue(n.IPAM.DHCP.Start), types.StringValue(n.IPAM.DHCP.End)
	} else {
		m.DHCPStart, m.DHCPEnd = types.StringNull(), types.StringNull()
	}
	return true
}

func (r *networkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m networkResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.readInto(ctx, &m, &resp.Diagnostics) {
		if !resp.Diagnostics.HasError() {
			resp.State.RemoveResource(ctx)
		}
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *networkResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state networkResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	c := r.client
	name := state.Name.ValueString()
	if plan.Name.ValueString() != name {
		if err := c.API.Call(ctx, "POST", "/networks/"+esc(name)+"/rename", map[string]any{"new_name": plan.Name.ValueString()}, nil); err != nil {
			resp.Diagnostics.AddError("Cannot rename the network", err.Error())
			return
		}
		name = plan.Name.ValueString()
	}
	if plan.Mode.ValueString() != "bridge" {
		body := map[string]any{"mode": plan.Mode.ValueString(), "address": plan.Address.ValueString(), "dhcp": nil}
		if !plan.Netmask.IsNull() && !plan.Netmask.IsUnknown() {
			body["netmask"] = plan.Netmask.ValueString()
		}
		if !plan.DHCPStart.IsNull() {
			body["dhcp"] = map[string]any{"start": plan.DHCPStart.ValueString(), "end": plan.DHCPEnd.ValueString()}
		}
		var out struct {
			NeedsRestart bool `json:"needs_restart"`
		}
		if err := c.API.Call(ctx, "PATCH", "/networks/"+esc(name), body, &out); err != nil {
			resp.Diagnostics.AddError("Cannot change the network", err.Error())
			return
		}
		if out.NeedsRestart {
			resp.Diagnostics.AddWarning("The network applies the change at its next start",
				fmt.Sprintf("The mode, address or mask of %q are written; the running network takes them when it is restarted (it is not restarted here: its VMs would lose their connection).", name))
		}
	}
	if plan.Autostart.ValueBool() != state.Autostart.ValueBool() {
		if err := c.API.Call(ctx, "PUT", "/networks/"+esc(name)+"/autostart", map[string]any{"autostart": plan.Autostart.ValueBool()}, nil); err != nil {
			resp.Diagnostics.AddError("Cannot set the network's start with the node", err.Error())
			return
		}
	}
	plan.ID = types.StringValue(name)
	r.readInto(ctx, &plan, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *networkResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m networkResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.API.Call(ctx, "DELETE", "/networks/"+esc(m.ID.ValueString())+"?confirm=true", nil, nil); err != nil && !errors.Is(notFound(err), errNotFound) {
		resp.Diagnostics.AddError("Cannot delete the network", err.Error())
	}
}

func (r *networkResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// ---- hyperlite_network_reservation ----

type reservationResource struct{ configured }

type reservationModel struct {
	ID      types.String `tfsdk:"id"`
	Network types.String `tfsdk:"network"`
	MAC     types.String `tfsdk:"mac"`
	IP      types.String `tfsdk:"ip"`
	Name    types.String `tfsdk:"name"`
}

func newReservationResource() resource.Resource { return &reservationResource{} }

func (r *reservationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_network_reservation"
}

func (r *reservationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "A DHCP reservation of a NAT or isolated network: the address a MAC address always gets.",
		Attributes: map[string]schema.Attribute{
			"id":      schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, Description: "`<network>/<mac>`."},
			"network": schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "The network."},
			"mac":     schema.StringAttribute{Required: true, PlanModifiers: replace, Validators: []validator.String{stringvalidator.RegexMatches(macRe, "a MAC address: 52:54:00:12:34:56")}, Description: "The MAC address (a `hyperlite_vm`'s `network_interface.N.mac`)."},
			"ip":      schema.StringAttribute{Required: true, PlanModifiers: replace, Validators: []validator.String{ipv4Address()}, Description: "The address it gets."},
			"name":    schema.StringAttribute{Optional: true, PlanModifiers: replace, Validators: []validator.String{stringvalidator.RegexMatches(reservationNameRe, "letters, digits and dashes, 63 characters at most")}, Description: "A host name given with it."},
		},
	}
}

func (r *reservationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m reservationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"mac": strings.ToLower(m.MAC.ValueString()), "ip": m.IP.ValueString()}
	if !m.Name.IsNull() {
		body["name"] = m.Name.ValueString()
	}
	if err := r.client.API.Call(ctx, "POST", "/networks/"+esc(m.Network.ValueString())+"/reservations", body, nil); err != nil {
		resp.Diagnostics.AddError("Cannot reserve the address", err.Error())
		return
	}
	m.ID = types.StringValue(m.Network.ValueString() + "/" + strings.ToLower(m.MAC.ValueString()))
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *reservationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m reservationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	network, mac, _ := strings.Cut(m.ID.ValueString(), "/")
	n, err := r.client.network(ctx, network)
	if errors.Is(err, errNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the network", err.Error())
		return
	}
	for _, res := range n.IPAM.Reservations {
		if strings.EqualFold(res.MAC, mac) {
			m.Network, m.IP = types.StringValue(network), types.StringValue(res.IP)
			if !strings.EqualFold(m.MAC.ValueString(), res.MAC) {
				m.MAC = types.StringValue(res.MAC)
			}
			m.Name = optionalString(res.Name, m.Name)
			resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

func (r *reservationResource) Update(context.Context, resource.UpdateRequest, *resource.UpdateResponse) {
	// Every attribute replaces the reservation.
}

func (r *reservationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m reservationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	network, mac, _ := strings.Cut(m.ID.ValueString(), "/")
	if err := r.client.API.Call(ctx, "DELETE", "/networks/"+esc(network)+"/reservations/"+esc(mac), nil, nil); err != nil && !errors.Is(notFound(err), errNotFound) {
		resp.Diagnostics.AddError("Cannot remove the reservation", err.Error())
	}
}

// ImportState takes `<network>/<mac>`.
func (r *reservationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	network, mac, ok := strings.Cut(req.ID, "/")
	if !ok || network == "" || mac == "" {
		resp.Diagnostics.AddError("Import id", "Give `<network>/<mac>`.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), network+"/"+strings.ToLower(mac))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("mac"), strings.ToLower(mac))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("network"), network)...)
}
