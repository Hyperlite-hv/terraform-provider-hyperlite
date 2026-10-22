package provider

import (
	"context"
	"errors"
	"math"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// ---- hyperlite_storage_pool ----

type storagePoolResource struct{ configured }

type storagePoolModel struct {
	ID            types.String  `tfsdk:"id"`
	Name          types.String  `tfsdk:"name"`
	Type          types.String  `tfsdk:"type"`
	Path          types.String  `tfsdk:"path"`
	NFSHost       types.String  `tfsdk:"nfs_host"`
	NFSExportPath types.String  `tfsdk:"nfs_export_path"`
	NFSVersion    types.String  `tfsdk:"nfs_version"`
	SizeGB        types.Int64   `tfsdk:"size_gb"`
	ISCSIHost     types.String  `tfsdk:"iscsi_host"`
	ISCSITarget   types.String  `tfsdk:"iscsi_target"`
	CHAPUser      types.String  `tfsdk:"chap_user"`
	CHAPPassword  types.String  `tfsdk:"chap_password"`
	State         types.String  `tfsdk:"state"`
	CapacityGB    types.Float64 `tfsdk:"capacity_gb"`
}

func newStoragePoolResource() resource.Resource { return &storagePoolResource{} }

func (r *storagePoolResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_storage_pool"
}

func (r *storagePoolResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	opt := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Optional: true, PlanModifiers: replace, Description: desc}
	}
	resp.Schema = schema.Schema{
		Description: "A storage pool of the node the provider talks to: a directory, an NFS export, a ZFS pool or an iSCSI target. Its name changes in place; anything else replaces it.",
		Attributes: map[string]schema.Attribute{
			"id":   schema.StringAttribute{Computed: true, PlanModifiers: keepID, Description: "The pool's name."},
			"name": schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.RegexMatches(nameRe, nameRule)}, Description: "Its name. A change renames it."},
			"type": schema.StringAttribute{
				Required: true, PlanModifiers: replace, Validators: []validator.String{stringvalidator.OneOf("dir", "netfs", "zfs", "iscsi")},
				Description: "`dir` (a directory of the node), `netfs` (an NFS export, shared storage), `zfs` or `iscsi`.",
			},
			"path":            opt("For `dir`: the directory."),
			"nfs_host":        opt("For `netfs`: the NFS server."),
			"nfs_export_path": opt("For `netfs`: the export."),
			"nfs_version":     schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()}, Description: "For `netfs`: the NFS version (`4.2` when absent)."},
			"size_gb":         schema.Int64Attribute{Optional: true, PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}, Description: "For `zfs`: the size of the pool's backing file."},
			"iscsi_host":      opt("For `iscsi`: the target's address."),
			"iscsi_target":    opt("For `iscsi`: the target's IQN."),
			"chap_user":       opt("For `iscsi`: the CHAP user."),
			"chap_password":   schema.StringAttribute{Optional: true, Sensitive: true, PlanModifiers: replace, Description: "For `iscsi`: the CHAP password (never read back)."},
			"state":           schema.StringAttribute{Computed: true, Description: "`active`, `inactive`, `building`, `degraded` or `inaccessible`."},
			"capacity_gb":     schema.Float64Attribute{Computed: true, Description: "Its capacity, in GB."},
		},
	}
}

func (r *storagePoolResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m storagePoolModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.Type.IsUnknown() {
		return
	}
	need := map[string][]struct {
		name  string
		value bool
	}{
		"dir":   {{"path", !m.Path.IsNull()}},
		"netfs": {{"nfs_host", !m.NFSHost.IsNull()}, {"nfs_export_path", !m.NFSExportPath.IsNull()}},
		"zfs":   {{"size_gb", !m.SizeGB.IsNull()}},
		"iscsi": {{"iscsi_host", !m.ISCSIHost.IsNull()}, {"iscsi_target", !m.ISCSITarget.IsNull()}},
	}
	for _, f := range need[m.Type.ValueString()] {
		if !f.value {
			resp.Diagnostics.AddAttributeError(path.Root(f.name), "Missing", "A `"+m.Type.ValueString()+"` pool needs `"+f.name+"`.")
		}
	}
}

type storagePoolWire struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	State      string   `json:"state"`
	Path       *string  `json:"path"`
	NFSVersion *string  `json:"nfs_version"`
	CapacityGB *float64 `json:"capacity_gb"`
}

func (r *storagePoolResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m storagePoolModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"name": m.Name.ValueString(), "type": m.Type.ValueString()}
	for k, v := range map[string]types.String{
		"path": m.Path, "nfs_host": m.NFSHost, "nfs_export_path": m.NFSExportPath, "nfs_version": m.NFSVersion,
		"iscsi_host": m.ISCSIHost, "iscsi_target": m.ISCSITarget, "chap_user": m.CHAPUser, "chap_password": m.CHAPPassword,
	} {
		if !v.IsNull() && !v.IsUnknown() {
			body[k] = v.ValueString()
		}
	}
	if !m.SizeGB.IsNull() {
		body["size_gb"] = m.SizeGB.ValueInt64()
	}
	if err := r.client.API.Call(ctx, "POST", "/storage", body, nil); err != nil {
		resp.Diagnostics.AddError("Cannot create the storage pool", err.Error())
		return
	}
	m.ID = m.Name
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), m.ID)...)
	if !r.readInto(ctx, &m, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *storagePoolResource) readInto(ctx context.Context, m *storagePoolModel, diags interface{ AddError(string, string) }) bool {
	var p storagePoolWire
	err := notFound(r.client.API.Call(ctx, "GET", "/storage/"+esc(m.ID.ValueString()), nil, &p))
	if err != nil {
		if !errors.Is(err, errNotFound) {
			diags.AddError("Cannot read the storage pool", err.Error())
		}
		return false
	}
	m.Name, m.ID, m.Type, m.State = types.StringValue(p.Name), types.StringValue(p.Name), types.StringValue(p.Type), types.StringValue(p.State)
	if p.Type == "dir" && p.Path != nil {
		m.Path = types.StringValue(*p.Path)
	}
	if p.Type == "netfs" && p.NFSVersion != nil {
		m.NFSVersion = types.StringValue(*p.NFSVersion)
	}
	if m.NFSVersion.IsUnknown() {
		m.NFSVersion = types.StringNull()
	}
	m.CapacityGB = types.Float64Null()
	if p.CapacityGB != nil {
		m.CapacityGB = types.Float64Value(math.Round(*p.CapacityGB*100) / 100)
	}
	return true
}

func (r *storagePoolResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m storagePoolModel
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

func (r *storagePoolResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state storagePoolModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.Name.ValueString() != state.Name.ValueString() {
		if err := r.client.API.Call(ctx, "POST", "/storage/"+esc(state.Name.ValueString())+"/rename", map[string]any{"new_name": plan.Name.ValueString()}, nil); err != nil {
			resp.Diagnostics.AddError("Cannot rename the storage pool", err.Error())
			return
		}
	}
	plan.ID = plan.Name
	r.readInto(ctx, &plan, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *storagePoolResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m storagePoolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if err := r.client.API.Call(ctx, "DELETE", "/storage/"+esc(m.ID.ValueString())+"?confirm=true", nil, nil); err != nil && !errors.Is(notFound(err), errNotFound) {
		resp.Diagnostics.AddError("Cannot delete the storage pool", err.Error())
	}
}

func (r *storagePoolResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// ---- hyperlite_storage_volume ----

type volumeResource struct{ configured }

type volumeModel struct {
	ID          types.String `tfsdk:"id"`
	StoragePool types.String `tfsdk:"storage_pool"`
	Name        types.String `tfsdk:"name"`
	SizeGB      types.Int64  `tfsdk:"size_gb"`
	Volume      types.String `tfsdk:"volume"`
	Path        types.String `tfsdk:"path"`
}

func newVolumeResource() resource.Resource { return &volumeResource{} }

func (r *volumeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_storage_volume"
}

func (r *volumeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "A blank volume of a storage pool (a qcow2 file, or a zvol on ZFS), to keep data apart from a VM. A disk of `hyperlite_vm` makes its own; use this for a volume that outlives its VMs.",
		Attributes: map[string]schema.Attribute{
			"id":           schema.StringAttribute{Computed: true, PlanModifiers: keepID, Description: "`<storage_pool>/<volume>`."},
			"storage_pool": schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Its storage pool."},
			"name":         schema.StringAttribute{Required: true, PlanModifiers: replace, Validators: []validator.String{stringvalidator.RegexMatches(volumeNameRe, nameRule+", `.qcow2` optional")}, Description: "Its name (`.qcow2` is added on a file pool)."},
			"size_gb":      schema.Int64Attribute{Required: true, PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}, Description: "Its size in GB (a change replaces it: grow a VM's disk through `hyperlite_vm`)."},
			"volume":       schema.StringAttribute{Computed: true, PlanModifiers: keepID, Description: "Its name in the pool, as `hyperlite_vm`'s attach takes it."},
			"path":         schema.StringAttribute{Computed: true, PlanModifiers: keepID, Description: "Its path on the node."},
		},
	}
}

type volumeWire struct {
	Name       string   `json:"name"`
	Path       string   `json:"path"`
	CapacityGB *float64 `json:"capacity_gb"`
}

func (r *volumeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m volumeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var made volumeWire
	body := map[string]any{"name": m.Name.ValueString(), "size_gb": m.SizeGB.ValueInt64()}
	if err := r.client.API.Call(ctx, "POST", "/storage/"+esc(m.StoragePool.ValueString())+"/volumes", body, &made); err != nil {
		resp.Diagnostics.AddError("Cannot create the volume", err.Error())
		return
	}
	m.Volume, m.Path = types.StringValue(made.Name), types.StringValue(made.Path)
	m.ID = types.StringValue(m.StoragePool.ValueString() + "/" + made.Name)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *volumeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m volumeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pool, vol, _ := strings.Cut(m.ID.ValueString(), "/")
	var v volumeWire
	err := notFound(r.client.API.Call(ctx, "GET", "/storage/"+esc(pool)+"/volumes/"+esc(vol), nil, &v))
	if errors.Is(err, errNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the volume", err.Error())
		return
	}
	m.StoragePool, m.Volume, m.Path = types.StringValue(pool), types.StringValue(v.Name), types.StringValue(v.Path)
	if m.Name.IsNull() {
		m.Name = types.StringValue(strings.TrimSuffix(v.Name, ".qcow2"))
	}
	if v.CapacityGB != nil {
		m.SizeGB = types.Int64Value(int64(math.Round(*v.CapacityGB)))
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *volumeResource) Update(context.Context, resource.UpdateRequest, *resource.UpdateResponse) {
	// Every attribute replaces the volume.
}

func (r *volumeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m volumeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	pool, vol, _ := strings.Cut(m.ID.ValueString(), "/")
	if err := r.client.API.Call(ctx, "DELETE", "/storage/"+esc(pool)+"/volumes/"+esc(vol)+"?confirm=true", nil, nil); err != nil && !errors.Is(notFound(err), errNotFound) {
		resp.Diagnostics.AddError("Cannot delete the volume", err.Error())
	}
}

// ImportState takes `<storage_pool>/<volume>`.
func (r *volumeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	pool, vol, ok := strings.Cut(req.ID, "/")
	if !ok || pool == "" || vol == "" {
		resp.Diagnostics.AddError("Import id", "Give `<storage_pool>/<volume>`.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), strings.TrimSuffix(vol, ".qcow2"))...)
}
