package provider

import (
	"context"
	"errors"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// ---- hyperlite_template ----

type templateResource struct{ configured }

type templateResourceModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	VM        types.String `tfsdk:"vm"`
	KeepVM    types.Bool   `tfsdk:"keep_vm"`
	Shutdown  types.String `tfsdk:"shutdown_timeout"`
	ForceStop types.Bool   `tfsdk:"force_stop"`
	VCPU      types.Int64  `tfsdk:"vcpu"`
	MemoryMB  types.Int64  `tfsdk:"memory_mb"`
	SSHUser   types.String `tfsdk:"ssh_user"`
	OS        types.String `tfsdk:"os"`
	CreatedAt types.String `tfsdk:"created_at"`
}

func newTemplateResource() resource.Resource { return &templateResource{} }

func (r *templateResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_template"
}

func (r *templateResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keepString := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	keepInt := []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "A template made from a stopped VM, to deploy VMs from it (`hyperlite_vm.clone.template`).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{idFollowsName{}}, Description: "The template's name."},
			"name": schema.StringAttribute{
				Required: true, Validators: []validator.String{stringvalidator.RegexMatches(nameRe, nameRule)},
				Description: "Its name. A change renames it.",
			},
			"vm": schema.StringAttribute{
				Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Description: "The VM it is made from. A running VM is shut down for the copy and started again when it stays. Read when the template is made only: a later change of the VM does not reach it.",
			},
			"keep_vm": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
				Description: "`true`: the template gets a copy of the VM's disk and the VM stays (vSphere's *Clone to Template*). " +
					"`false`: the VM becomes the template and is gone (Proxmox VE's *Convert to template*): do not manage that VM with `hyperlite_vm` too.",
			},
			"shutdown_timeout": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("3m"),
				Validators:  []validator.String{durationValidator{}},
				Description: "How long the VM's clean shutdown may take: a running VM is stopped for the copy, then started again (`keep_vm`).",
			},
			"force_stop": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Stop the VM hard when its clean shutdown did not end within `shutdown_timeout`.",
			},
			"vcpu":       schema.Int64Attribute{Computed: true, PlanModifiers: keepInt, Description: "Virtual CPUs of the VM it was made from."},
			"memory_mb":  schema.Int64Attribute{Computed: true, PlanModifiers: keepInt, Description: "Memory of the VM it was made from, in MB."},
			"ssh_user":   schema.StringAttribute{Computed: true, PlanModifiers: keepString, Description: "The account its system has, if known."},
			"os":         schema.StringAttribute{Computed: true, PlanModifiers: keepString, Description: "Its system, if known."},
			"created_at": schema.StringAttribute{Computed: true, PlanModifiers: keepString, Description: "When it was made."},
		},
	}
}

type templateWire struct {
	Name      string `json:"name"`
	VCPU      int64  `json:"vcpu"`
	MemoryMB  int64  `json:"memory_mb"`
	VMSource  string `json:"vm_source"`
	SSHUser   string `json:"ssh_user"`
	OSLabel   string `json:"os_label"`
	CreatedAt string `json:"created_at"`
}

// template reads one template by its name (GET /templates lists them all).
func (r *templateResource) template(ctx context.Context, name string) (*templateWire, error) {
	templates, err := list[templateWire](ctx, r.client, "/templates")
	if err != nil {
		return nil, err
	}
	for i := range templates {
		if templates[i].Name == name {
			return &templates[i], nil
		}
	}
	return nil, errNotFound
}

func (m *templateResourceModel) from(t *templateWire) {
	m.ID, m.Name = types.StringValue(t.Name), types.StringValue(t.Name)
	m.VCPU, m.MemoryMB = types.Int64Value(t.VCPU), types.Int64Value(t.MemoryMB)
	m.SSHUser, m.OS, m.CreatedAt = optional(t.SSHUser), optional(t.OSLabel), optional(t.CreatedAt)
	if t.VMSource != "" {
		m.VM = types.StringValue(t.VMSource)
	}
}

func (r *templateResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m templateResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	vm := m.VM.ValueString()
	body := map[string]any{"template_name": m.Name.ValueString(), "keep_vm": m.KeepVM.ValueBool()}
	err := r.client.whileStopped(ctx, vm, duration(m.Shutdown, 3*time.Minute), m.ForceStop.ValueBool(), func() (string, error) {
		after := "" // converted: the VM is gone, nothing to start again
		if m.KeepVM.ValueBool() {
			after = vm
		}
		return after, r.client.runTask(ctx, "POST", "/templates/from-vm/"+esc(vm), body)
	})
	if err != nil {
		resp.Diagnostics.AddError("Cannot make the template", err.Error())
		return
	}
	t, err := r.template(ctx, m.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the template made", err.Error())
		return
	}
	m.from(t)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *templateResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m templateResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	t, err := r.template(ctx, m.ID.ValueString())
	if errors.Is(err, errNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the template", err.Error())
		return
	}
	m.from(t)
	// Imported: how it was made is not recorded, the defaults are assumed.
	if m.KeepVM.IsNull() {
		m.KeepVM = types.BoolValue(true)
	}
	if m.Shutdown.IsNull() {
		m.Shutdown, m.ForceStop = types.StringValue("3m"), types.BoolValue(true)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *templateResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state templateResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// The name is the only property of the template changed in place; shutdown_timeout and force_stop are the
	// provider's own.
	if !plan.Name.Equal(state.Name) {
		if err := r.client.API.Call(ctx, "POST", "/templates/"+esc(state.ID.ValueString())+"/rename", map[string]any{"new_name": plan.Name.ValueString()}, nil); err != nil {
			resp.Diagnostics.AddError("Cannot rename the template", err.Error())
			return
		}
	}
	t, err := r.template(ctx, plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the renamed template", err.Error())
		return
	}
	plan.from(t)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *templateResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m templateResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	err := notFound(r.client.API.Call(ctx, "DELETE", "/templates/"+esc(m.ID.ValueString())+"?confirm=true", nil, nil))
	if err != nil && !errors.Is(err, errNotFound) {
		resp.Diagnostics.AddError("Cannot delete the template", err.Error())
	}
}

func (r *templateResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
