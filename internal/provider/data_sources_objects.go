package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Data sources of the objects the resources manage, each found by the name a person gives it: an object made
// elsewhere (the dashboard, another configuration) is used without being managed.

func noSuch(what, name string) (string, string) {
	return "No such " + what, fmt.Sprintf("Hyperlite has no %s named %q.", what, name)
}

// ---- hyperlite_vm ----

type vmDataSource struct{ base }

type vmDataModel struct {
	Name       types.String `tfsdk:"name"`
	Node       types.String `tfsdk:"node"`
	ID         types.String `tfsdk:"id"`
	State      types.String `tfsdk:"state"`
	VCPU       types.Int64  `tfsdk:"vcpu"`
	MemoryMB   types.Int64  `tfsdk:"memory_mb"`
	IP         types.String `tfsdk:"ip"`
	Firmware   types.String `tfsdk:"firmware"`
	Interfaces types.List   `tfsdk:"network_interface"`
}

var vmInterfaceType = types.ObjectType{AttrTypes: map[string]attr.Type{"mac": types.StringType, "network": types.StringType}}

func newVMDataSource() datasource.DataSource { return &vmDataSource{} }

func (d *vmDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm"
}

func (d *vmDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A VM, to refer to one made elsewhere: reserve its address, back it up, give it a pool.",
		Attributes: map[string]schema.Attribute{
			"name":      schema.StringAttribute{Required: true, Description: "The VM's name."},
			"node":      schema.StringAttribute{Optional: true, Computed: true, Description: "The node it is on (`local` when absent)."},
			"id":        schema.StringAttribute{Computed: true, Description: "Its UUID."},
			"state":     schema.StringAttribute{Computed: true, Description: "`active`, `stopped`, `paused`..."},
			"vcpu":      schema.Int64Attribute{Computed: true, Description: "Virtual CPUs."},
			"memory_mb": schema.Int64Attribute{Computed: true, Description: "Memory, in MB."},
			"ip":        schema.StringAttribute{Computed: true, Description: "Its address, when it runs and reports one."},
			"firmware":  schema.StringAttribute{Computed: true, Description: "`bios` or `uefi`."},
			"network_interface": schema.ListAttribute{
				ElementType: vmInterfaceType, Computed: true,
				Description: "Its interfaces, in order: `mac` and `network`.",
			},
		},
	}
}

func (d *vmDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m vmDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name, node := m.Name.ValueString(), m.Node.ValueString()
	vm, err := d.client.vmByName(ctx, name, node)
	if errors.Is(err, errNotFound) {
		resp.Diagnostics.AddError(noSuch("VM", name))
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the VM", err.Error())
		return
	}
	ifaces, err := d.client.vmInterfaces(ctx, name, node)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the VM's interfaces", err.Error())
		return
	}
	elems := make([]attr.Value, len(ifaces))
	for i, n := range ifaces {
		elems[i] = types.ObjectValueMust(vmInterfaceType.AttrTypes, map[string]attr.Value{
			"mac": types.StringValue(n.MAC), "network": types.StringValue(n.Network),
		})
	}
	m.ID, m.Node, m.State = types.StringValue(vm.UUID), types.StringValue(vm.Node), types.StringValue(vm.State)
	m.VCPU, m.MemoryMB = types.Int64Value(vm.VCPU), types.Int64Value(int64(vm.MemoryMB))
	m.IP, m.Firmware = optional(vm.IP), optional(vm.Firmware)
	m.Interfaces = types.ListValueMust(vmInterfaceType, elems)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---- hyperlite_storage_pool ----

type storagePoolDataSource struct{ base }

type storagePoolDataModel struct {
	Name         types.String  `tfsdk:"name"`
	Node         types.String  `tfsdk:"node"`
	ID           types.String  `tfsdk:"id"`
	UUID         types.String  `tfsdk:"uuid"`
	Type         types.String  `tfsdk:"type"`
	State        types.String  `tfsdk:"state"`
	Path         types.String  `tfsdk:"path"`
	Autostart    types.Bool    `tfsdk:"autostart"`
	CapacityGB   types.Float64 `tfsdk:"capacity_gb"`
	AllocationGB types.Float64 `tfsdk:"allocation_gb"`
	AvailableGB  types.Float64 `tfsdk:"available_gb"`
}

func newStoragePoolDataSource() datasource.DataSource { return &storagePoolDataSource{} }

func (d *storagePoolDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_storage_pool"
}

func (d *storagePoolDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A storage pool, to put disks and volumes in it, or to check its free space.",
		Attributes: map[string]schema.Attribute{
			"name":          schema.StringAttribute{Required: true, Description: "The pool's name."},
			"node":          schema.StringAttribute{Optional: true, Description: "The node it is on (`local` when absent)."},
			"id":            schema.StringAttribute{Computed: true, Description: "The pool's name."},
			"uuid":          schema.StringAttribute{Computed: true, Description: "Its libvirt UUID."},
			"type":          schema.StringAttribute{Computed: true, Description: "`dir`, `netfs`, `iscsi`, `zfs`..."},
			"state":         schema.StringAttribute{Computed: true, Description: "`active`, `inactive`, `building`, `degraded` or `inaccessible`."},
			"path":          schema.StringAttribute{Computed: true, Description: "Where its volumes are."},
			"autostart":     schema.BoolAttribute{Computed: true, Description: "Started with its node."},
			"capacity_gb":   schema.Float64Attribute{Computed: true, Description: "Its size, in GB."},
			"allocation_gb": schema.Float64Attribute{Computed: true, Description: "What its volumes use, in GB."},
			"available_gb":  schema.Float64Attribute{Computed: true, Description: "What is left, in GB."},
		},
	}
}

func (d *storagePoolDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m storagePoolDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var p struct {
		Name         string  `json:"name"`
		UUID         string  `json:"uuid"`
		Type         string  `json:"type"`
		State        string  `json:"state"`
		Path         string  `json:"path"`
		Autostart    bool    `json:"autostart"`
		CapacityGB   float64 `json:"capacity_gb"`
		AllocationGB float64 `json:"allocation_gb"`
		AvailableGB  float64 `json:"available_gb"`
	}
	name := m.Name.ValueString()
	err := notFound(d.client.API.Call(ctx, "GET", withNode("/storage/"+esc(name), m.Node.ValueString()), nil, &p))
	if errors.Is(err, errNotFound) {
		resp.Diagnostics.AddError(noSuch("storage pool", name))
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the storage pool", err.Error())
		return
	}
	m.ID, m.UUID, m.Type, m.State = types.StringValue(p.Name), optional(p.UUID), types.StringValue(p.Type), types.StringValue(p.State)
	m.Path, m.Autostart = optional(p.Path), types.BoolValue(p.Autostart)
	m.CapacityGB, m.AllocationGB, m.AvailableGB = types.Float64Value(p.CapacityGB), types.Float64Value(p.AllocationGB), types.Float64Value(p.AvailableGB)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---- hyperlite_resource_pool ----

type resourcePoolDataSource struct{ base }

type resourcePoolDataModel struct {
	Name        types.String `tfsdk:"name"`
	ID          types.String `tfsdk:"id"`
	Description types.String `tfsdk:"description"`
	VMs         types.Set    `tfsdk:"vms"`
}

func newResourcePoolDataSource() datasource.DataSource { return &resourcePoolDataSource{} }

func (d *resourcePoolDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_resource_pool"
}

func (d *resourcePoolDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A pool of VMs, to give rights on it (`hyperlite_acl`) or back it up (`hyperlite_backup_job`).",
		Attributes: map[string]schema.Attribute{
			"name":        schema.StringAttribute{Required: true, Description: "The pool's name."},
			"id":          schema.StringAttribute{Computed: true, Description: "Its id."},
			"description": schema.StringAttribute{Computed: true, Description: "Its description."},
			"vms":         schema.SetAttribute{ElementType: types.StringType, Computed: true, Description: "Its VMs, by name."},
		},
	}
}

func (d *resourcePoolDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m resourcePoolDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pools, err := list[poolWire](ctx, d.client, "/pools")
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the pools", err.Error())
		return
	}
	for _, p := range pools {
		if p.Name == m.Name.ValueString() {
			m.ID, m.Description = types.StringValue(strconv.FormatInt(p.ID, 10)), types.StringValue(p.Description)
			m.VMs = setOf(append([]string{}, p.VMs...))
			resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
			return
		}
	}
	resp.Diagnostics.AddError(noSuch("pool", m.Name.ValueString()))
}

// ---- hyperlite_user ----

type userDataSource struct{ base }

type userDataModel struct {
	Username types.String `tfsdk:"username"`
	ID       types.String `tfsdk:"id"`
	Role     types.String `tfsdk:"role"`
}

func newUserDataSource() datasource.DataSource { return &userDataSource{} }

func (d *userDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user"
}

func (d *userDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A local account, to give it rights (`hyperlite_acl`) or put it in a group.",
		Attributes: map[string]schema.Attribute{
			"username": schema.StringAttribute{Required: true, Description: "The account's name."},
			"id":       schema.StringAttribute{Computed: true, Description: "The account's name."},
			"role":     schema.StringAttribute{Computed: true, Description: "Its global role: `viewer` or `admin`."},
		},
	}
}

func (d *userDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m userDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var u struct {
		Username string `json:"username"`
		Role     string `json:"role"`
	}
	name := m.Username.ValueString()
	err := notFound(d.client.API.Call(ctx, "GET", "/auth/users/"+esc(name), nil, &u))
	if errors.Is(err, errNotFound) {
		resp.Diagnostics.AddError(noSuch("account", name))
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the account", err.Error())
		return
	}
	m.ID, m.Role = types.StringValue(u.Username), types.StringValue(u.Role)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---- hyperlite_group ----

type groupDataSource struct{ base }

type groupDataModel struct {
	Name    types.String `tfsdk:"name"`
	ID      types.String `tfsdk:"id"`
	Members types.Set    `tfsdk:"members"`
}

func newGroupDataSource() datasource.DataSource { return &groupDataSource{} }

func (d *groupDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group"
}

func (d *groupDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A group of accounts, to give it rights (`hyperlite_acl`).",
		Attributes: map[string]schema.Attribute{
			"name":    schema.StringAttribute{Required: true, Description: "The group's name."},
			"id":      schema.StringAttribute{Computed: true, Description: "Its id."},
			"members": schema.SetAttribute{ElementType: types.StringType, Computed: true, Description: "Its accounts, by name."},
		},
	}
}

func (d *groupDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m groupDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	groups, err := list[groupWire](ctx, d.client, "/groups")
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the groups", err.Error())
		return
	}
	for _, g := range groups {
		if g.Name == m.Name.ValueString() {
			m.ID, m.Members = types.StringValue(strconv.FormatInt(g.ID, 10)), setOf(append([]string{}, g.Members...))
			resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
			return
		}
	}
	resp.Diagnostics.AddError(noSuch("group", m.Name.ValueString()))
}

// ---- hyperlite_role ----

type roleDataSource struct{ base }

type roleDataModel struct {
	Name       types.String `tfsdk:"name"`
	ID         types.String `tfsdk:"id"`
	Key        types.String `tfsdk:"key"`
	Custom     types.Bool   `tfsdk:"custom"`
	Privileges types.Set    `tfsdk:"privileges"`
}

func newRoleDataSource() datasource.DataSource { return &roleDataSource{} }

func (d *roleDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_role"
}

func (d *roleDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A role, predefined (`drive`, `operator`, `manager`...) or custom, to assign it (`hyperlite_acl.role = data.hyperlite_role.x.key`).",
		Attributes: map[string]schema.Attribute{
			"name":       schema.StringAttribute{Required: true, Description: "A predefined role's key, or a custom role's name."},
			"id":         schema.StringAttribute{Computed: true, Description: "Its key."},
			"key":        schema.StringAttribute{Computed: true, Description: "What an assignment names it by."},
			"custom":     schema.BoolAttribute{Computed: true, Description: "A custom role."},
			"privileges": schema.SetAttribute{ElementType: types.StringType, Computed: true, Description: "What it allows."},
		},
	}
}

func (d *roleDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m roleDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := m.Name.ValueString()
	set := func(key string, custom bool, privileges []string) {
		m.ID, m.Key, m.Custom, m.Privileges = types.StringValue(key), types.StringValue(key), types.BoolValue(custom), setOf(append([]string{}, privileges...))
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
	var predefined map[string]struct {
		Privileges []string `json:"privileges"`
	}
	if err := d.client.API.Call(ctx, "GET", "/acl/roles", nil, &predefined); err != nil {
		resp.Diagnostics.AddError("Cannot read the roles", err.Error())
		return
	}
	if r, ok := predefined[name]; ok {
		set(name, false, r.Privileges)
		return
	}
	customs, err := list[struct {
		Key        string   `json:"key"`
		Label      string   `json:"label"`
		Privileges []string `json:"privileges"`
	}](ctx, d.client, "/acl/custom-roles")
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the custom roles", err.Error())
		return
	}
	for _, r := range customs {
		if r.Label == name {
			set(r.Key, true, r.Privileges)
			return
		}
	}
	keys := make([]string, 0, len(predefined))
	for k := range predefined {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	summary, detail := noSuch("role", name)
	resp.Diagnostics.AddError(summary, fmt.Sprintf("%s The predefined roles are %q.", detail, keys))
}

// ---- hyperlite_iso ----

type isoDataSource struct{ base }

type isoDataModel struct {
	Name    types.String  `tfsdk:"name"`
	ID      types.String  `tfsdk:"id"`
	SizeMB  types.Float64 `tfsdk:"size_mb"`
	AddedAt types.String  `tfsdk:"added_at"`
}

func newISODataSource() datasource.DataSource { return &isoDataSource{} }

func (d *isoDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_iso"
}

func (d *isoDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An ISO image of the node's library, to install a VM from it (`hyperlite_vm.iso`).",
		Attributes: map[string]schema.Attribute{
			"name":     schema.StringAttribute{Required: true, Description: "The file's name (`debian-13.iso`)."},
			"id":       schema.StringAttribute{Computed: true, Description: "The file's name."},
			"size_mb":  schema.Float64Attribute{Computed: true, Description: "Its size, in MB."},
			"added_at": schema.StringAttribute{Computed: true, Description: "When it was added (RFC 3339)."},
		},
	}
}

func (d *isoDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m isoDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	isos, err := list[struct {
		Name    string  `json:"name"`
		SizeMB  float64 `json:"size_mb"`
		AddedAt string  `json:"added_at"`
	}](ctx, d.client, "/isos")
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the ISO library", err.Error())
		return
	}
	for _, i := range isos {
		if i.Name == m.Name.ValueString() {
			m.ID, m.SizeMB, m.AddedAt = types.StringValue(i.Name), types.Float64Value(i.SizeMB), optional(i.AddedAt)
			resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
			return
		}
	}
	resp.Diagnostics.AddError(noSuch("ISO image", m.Name.ValueString()))
}
