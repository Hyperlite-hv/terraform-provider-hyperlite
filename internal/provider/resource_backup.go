package provider

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// The server's own rule (app/core/backup_groups.py): two-digit hours and minutes.
var (
	timeRe = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
	// app/core/object_meta.py TAG_RE.
	tagRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,31}$`)
)

const tagRule = "1 to 32 lowercase letters, digits, '-', '_' or '.', starting with a letter or a digit"

// keep is the GFS rule `keep_<what>`, with the server's bounds.
func keep(what string, most int64) schema.Int64Attribute {
	return schema.Int64Attribute{
		Optional: true, Validators: []validator.Int64{int64validator.Between(1, most)},
		Description: "GFS retention: the last backup of each of the last N " + what + ".",
	}
}

// The retention and schedule fields a VM's schedule and a grouped job share.
func scheduleAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"frequency": schema.StringAttribute{
			Required: true, Validators: []validator.String{stringvalidator.OneOf("daily", "weekly", "monthly")},
			Description: "`daily`, `weekly` or `monthly`.",
		},
		"time": schema.StringAttribute{
			Required: true, Validators: []validator.String{stringvalidator.RegexMatches(timeRe, "HH:MM, on 24 hours")},
			Description: "The time of the run, `HH:MM`, on the node's clock.",
		},
		"target_dir": schema.StringAttribute{
			Optional: true, Computed: true, PlanModifiers: []planmodifier.String{defaultDirectory{}},
			Description: "Where the backups go (an absolute path); absent: the node's default directory, which the server reports.",
		},
		"retention_count": schema.Int64Attribute{
			Optional: true, Computed: true, Default: int64default.StaticInt64(7),
			Validators: []validator.Int64{int64validator.Between(1, 365)}, Description: "The newest backups kept, besides those a GFS rule keeps.",
		},
		"keep_days":   keep("days", 366),
		"keep_weeks":  keep("weeks", 260),
		"keep_months": keep("months", 120),
	}
}

// targetDirSet is the private state key saying whether the configuration named target_dir at the last apply.
const targetDirSet = "target_dir_set"

// defaultDirectory plans target_dir when the configuration leaves it out. The server always reports the directory it
// writes in, the default one included, so the attribute is computed; but a directory removed from the configuration
// must go back to the default one, which the prior state alone cannot tell from a default already in place.
type defaultDirectory struct{}

func (defaultDirectory) Description(context.Context) string {
	return "Keeps the default directory the server reports; one removed from the configuration goes back to the default."
}

func (d defaultDirectory) MarkdownDescription(ctx context.Context) string { return d.Description(ctx) }

func (defaultDirectory) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if !req.ConfigValue.IsNull() || req.StateValue.IsNull() || req.Plan.Raw.IsNull() {
		return // configured, created (unknown until the server answers) or destroyed
	}
	set, diags := req.Private.GetKey(ctx, targetDirSet)
	resp.Diagnostics.Append(diags...)
	if string(set) == "true" {
		resp.PlanValue = types.StringUnknown()
		return
	}
	resp.PlanValue = req.StateValue
}

// rememberTargetDir records for defaultDirectory whether the configuration names target_dir.
func rememberTargetDir(ctx context.Context, config tfsdk.Config, private interface {
	SetKey(context.Context, string, []byte) diag.Diagnostics
}) diag.Diagnostics {
	var dir types.String
	diags := config.GetAttribute(ctx, path.Root("target_dir"), &dir)
	return append(diags, private.SetKey(ctx, targetDirSet, []byte(strconv.FormatBool(!dir.IsNull())))...)
}

func optInt(v types.Int64) any {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	return v.ValueInt64()
}

func optStr(v types.String) any {
	if v.IsNull() || v.IsUnknown() || v.ValueString() == "" {
		return nil
	}
	return v.ValueString()
}

func intOrNull(v *int64) types.Int64 {
	if v == nil {
		return types.Int64Null()
	}
	return types.Int64Value(*v)
}

// ---- hyperlite_vm_backup_schedule ----

type scheduleResource struct{ configured }

type scheduleModel struct {
	ID             types.String `tfsdk:"id"`
	Node           types.String `tfsdk:"node"`
	VM             types.String `tfsdk:"vm"`
	Frequency      types.String `tfsdk:"frequency"`
	Time           types.String `tfsdk:"time"`
	TargetDir      types.String `tfsdk:"target_dir"`
	RetentionCount types.Int64  `tfsdk:"retention_count"`
	KeepDays       types.Int64  `tfsdk:"keep_days"`
	KeepWeeks      types.Int64  `tfsdk:"keep_weeks"`
	KeepMonths     types.Int64  `tfsdk:"keep_months"`
}

func newScheduleResource() resource.Resource { return &scheduleResource{} }

func (r *scheduleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm_backup_schedule"
}

func (r *scheduleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := scheduleAttributes()
	attrs["id"] = schema.StringAttribute{Computed: true, PlanModifiers: keepID, Description: "The VM's name."}
	attrs["vm"] = schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, Description: "The VM backed up."}
	attrs["node"] = nodeAttribute(&r.configured, "VM")
	resp.Schema = schema.Schema{Description: "The backup schedule of one VM.", Attributes: attrs}
}

type scheduleWire struct {
	Frequency      string  `json:"frequency"`
	Time           string  `json:"time"`
	TargetDir      *string `json:"target_dir"`
	RetentionCount int64   `json:"retention_count"`
	KeepDays       *int64  `json:"keep_days"`
	KeepWeeks      *int64  `json:"keep_weeks"`
	KeepMonths     *int64  `json:"keep_months"`
}

func (r *scheduleResource) put(ctx context.Context, m *scheduleModel) error {
	body := map[string]any{
		"frequency": m.Frequency.ValueString(), "time": m.Time.ValueString(), "target_dir": optStr(m.TargetDir),
		"retention_count": m.RetentionCount.ValueInt64(), "keep_days": optInt(m.KeepDays),
		"keep_weeks": optInt(m.KeepWeeks), "keep_months": optInt(m.KeepMonths),
	}
	if m.Node.IsUnknown() {
		m.Node = types.StringValue("local")
	}
	if err := r.client.at(memberOf(m.Node)).call(ctx, "PUT", "/vms/"+esc(m.VM.ValueString())+"/backup-schedule", body, nil); err != nil {
		return err
	}
	// The PUT answers no schedule: the directory the server chose (the default one, when none is set) is read back.
	var s *scheduleWire
	if err := r.client.at(memberOf(m.Node)).call(ctx, "GET", "/vms/"+esc(m.VM.ValueString())+"/backup-schedule", nil, &s); err != nil {
		return err
	}
	if s == nil {
		return errors.New("the server reports no schedule right after setting it")
	}
	m.TargetDir = optionalString(s.TargetDir, m.TargetDir)
	return nil
}

func (r *scheduleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m scheduleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.put(ctx, &m); err != nil {
		resp.Diagnostics.AddError("Cannot set the backup schedule", err.Error())
		return
	}
	m.ID = m.VM
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	resp.Diagnostics.Append(rememberTargetDir(ctx, req.Config, resp.Private)...)
}

func (r *scheduleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m scheduleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if m.Node.IsNull() {
		m.Node = types.StringValue("local") // imported
	}
	c := r.client.at(memberOf(m.Node))
	var s *scheduleWire
	err := notFound(c.call(ctx, "GET", "/vms/"+esc(m.ID.ValueString())+"/backup-schedule", nil, &s))
	if errors.Is(err, errNotFound) || (err == nil && s == nil) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the backup schedule", err.Error())
		return
	}
	m.VM, m.Frequency, m.Time, m.RetentionCount = m.ID, types.StringValue(s.Frequency), types.StringValue(s.Time), types.Int64Value(s.RetentionCount)
	m.TargetDir = optionalString(s.TargetDir, m.TargetDir)
	m.KeepDays, m.KeepWeeks, m.KeepMonths = intOrNull(s.KeepDays), intOrNull(s.KeepWeeks), intOrNull(s.KeepMonths)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *scheduleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m scheduleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.put(ctx, &m); err != nil {
		resp.Diagnostics.AddError("Cannot change the backup schedule", err.Error())
		return
	}
	m.ID = m.VM
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	resp.Diagnostics.Append(rememberTargetDir(ctx, req.Config, resp.Private)...)
}

func (r *scheduleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m scheduleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	c := r.client.at(memberOf(m.Node))
	if err := c.call(ctx, "DELETE", "/vms/"+esc(m.ID.ValueString())+"/backup-schedule", nil, nil); err != nil && !errors.Is(notFound(err), errNotFound) {
		resp.Diagnostics.AddError("Cannot remove the backup schedule", err.Error())
	}
}

// ImportState takes `<vm>`, or `<node>/<vm>`.
func (r *scheduleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if id := importOn(ctx, req, resp, "<vm>"); id != nil {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id[0])...)
	}
}

// ---- hyperlite_backup_job ----

type backupJobResource struct{ configured }

type backupJobModel struct {
	ID             types.String `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	Selection      types.String `tfsdk:"selection"`
	Value          types.String `tfsdk:"value"`
	Excluded       types.Set    `tfsdk:"excluded"`
	Active         types.Bool   `tfsdk:"active"`
	Frequency      types.String `tfsdk:"frequency"`
	Time           types.String `tfsdk:"time"`
	TargetDir      types.String `tfsdk:"target_dir"`
	RetentionCount types.Int64  `tfsdk:"retention_count"`
	KeepDays       types.Int64  `tfsdk:"keep_days"`
	KeepWeeks      types.Int64  `tfsdk:"keep_weeks"`
	KeepMonths     types.Int64  `tfsdk:"keep_months"`
}

func newBackupJobResource() resource.Resource { return &backupJobResource{} }

func (r *backupJobResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_backup_job"
}

func (r *backupJobResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := scheduleAttributes()
	attrs["id"] = schema.StringAttribute{Computed: true, PlanModifiers: keepID, Description: "The job's id."}
	attrs["name"] = schema.StringAttribute{Required: true, Description: "Its name."}
	attrs["selection"] = schema.StringAttribute{
		Required: true, Validators: []validator.String{stringvalidator.OneOf("all", "tag", "pool")},
		Description: "The VMs it backs up: `all`, those with a `tag`, or those of a `pool`.",
	}
	attrs["value"] = schema.StringAttribute{Optional: true, Description: "The tag, or the pool's id."}
	attrs["excluded"] = schema.SetAttribute{
		ElementType: types.StringType, Optional: true, Computed: true,
		Default: setdefault.StaticValue(types.SetValueMust(types.StringType, nil)), Description: "VMs left out, by name.",
	}
	attrs["active"] = schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), Description: "Runs on its schedule."}
	resp.Schema = schema.Schema{Description: "A grouped backup job: every VM it selects, on one schedule.", Attributes: attrs}
}

type backupJobWire struct {
	ID             int64    `json:"id"`
	Name           string   `json:"name"`
	Selection      string   `json:"selection"`
	Value          *string  `json:"value"`
	Excluded       []string `json:"excluded"`
	Active         bool     `json:"active"`
	Frequency      string   `json:"frequency"`
	Time           string   `json:"time"`
	TargetDir      *string  `json:"target_dir"`
	RetentionCount int64    `json:"retention_count"`
	KeepDays       *int64   `json:"keep_days"`
	KeepWeeks      *int64   `json:"keep_weeks"`
	KeepMonths     *int64   `json:"keep_months"`
}

// The server normalises a tag (lowercased) and drops a value `all` does not use: refused here instead, Terraform
// would otherwise find another value than the one configured.
func (r *backupJobResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m backupJobModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.Selection.IsUnknown() || m.Value.IsUnknown() {
		return
	}
	value := m.Value.ValueString()
	switch m.Selection.ValueString() {
	case "all":
		if value != "" {
			resp.Diagnostics.AddAttributeError(path.Root("value"), "Value not used", "`all` selects every VM: leave `value` out.")
		}
	case "tag":
		if !tagRe.MatchString(value) {
			resp.Diagnostics.AddAttributeError(path.Root("value"), "Invalid tag", tagRule+".")
		}
	case "pool":
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("value"), "Invalid pool", "Give the pool's id (`hyperlite_resource_pool.<name>.id`).")
		}
	}
}

func (m *backupJobModel) body(ctx context.Context) map[string]any {
	return map[string]any{
		"name": m.Name.ValueString(), "selection": m.Selection.ValueString(), "value": optStr(m.Value),
		"excluded": stringsOf(ctx, m.Excluded), "active": m.Active.ValueBool(),
		"frequency": m.Frequency.ValueString(), "time": m.Time.ValueString(), "target_dir": optStr(m.TargetDir),
		"retention_count": m.RetentionCount.ValueInt64(), "keep_days": optInt(m.KeepDays),
		"keep_weeks": optInt(m.KeepWeeks), "keep_months": optInt(m.KeepMonths),
	}
}

func (m *backupJobModel) from(w *backupJobWire) {
	m.ID, m.Name, m.Selection = types.StringValue(strconv.FormatInt(w.ID, 10)), types.StringValue(w.Name), types.StringValue(w.Selection)
	m.Value = optionalString(w.Value, m.Value)
	m.Excluded, m.Active = setOf(append([]string{}, w.Excluded...)), types.BoolValue(w.Active)
	m.Frequency, m.Time, m.RetentionCount = types.StringValue(w.Frequency), types.StringValue(w.Time), types.Int64Value(w.RetentionCount)
	m.TargetDir = optionalString(w.TargetDir, m.TargetDir)
	m.KeepDays, m.KeepWeeks, m.KeepMonths = intOrNull(w.KeepDays), intOrNull(w.KeepWeeks), intOrNull(w.KeepMonths)
}

func (r *backupJobResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m backupJobModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var w backupJobWire
	if err := r.client.call(ctx, "POST", "/backup-groups", m.body(ctx), &w); err != nil {
		resp.Diagnostics.AddError("Cannot create the backup job", err.Error())
		return
	}
	m.from(&w)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	resp.Diagnostics.Append(rememberTargetDir(ctx, req.Config, resp.Private)...)
}

func (r *backupJobResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m backupJobModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var w backupJobWire
	err := notFound(r.client.call(ctx, "GET", "/backup-groups/"+m.ID.ValueString(), nil, &w))
	if errors.Is(err, errNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the backup job", err.Error())
		return
	}
	m.from(&w)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *backupJobResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state backupJobModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var w backupJobWire
	if err := r.client.call(ctx, "PUT", "/backup-groups/"+state.ID.ValueString(), plan.body(ctx), &w); err != nil {
		resp.Diagnostics.AddError("Cannot change the backup job", err.Error())
		return
	}
	plan.from(&w)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(rememberTargetDir(ctx, req.Config, resp.Private)...)
}

func (r *backupJobResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m backupJobModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if err := r.client.call(ctx, "DELETE", "/backup-groups/"+m.ID.ValueString(), nil, nil); err != nil && !errors.Is(notFound(err), errNotFound) {
		resp.Diagnostics.AddError("Cannot delete the backup job", err.Error())
	}
}

func (r *backupJobResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// ---- hyperlite_vm_snapshot ----

type snapshotResource struct{ configured }

type snapshotModel struct {
	ID          types.String `tfsdk:"id"`
	Node        types.String `tfsdk:"node"`
	VM          types.String `tfsdk:"vm"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

func newSnapshotResource() resource.Resource { return &snapshotResource{} }

func (r *snapshotResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm_snapshot"
}

func (r *snapshotResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "A snapshot of a VM (with its memory when it runs, on a qcow2 disk; disk only on ZFS). Destroying the resource deletes the snapshot; it never restores it.",
		Attributes: map[string]schema.Attribute{
			"node": nodeAttribute(&r.configured, "VM"),
			"id":   schema.StringAttribute{Computed: true, PlanModifiers: keepID, Description: "`<vm>/<snapshot>`."},
			"vm":   schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "The VM."},
			"name": schema.StringAttribute{
				Required: true, PlanModifiers: replace, Validators: []validator.String{stringvalidator.RegexMatches(nameRe, nameRule)},
				Description: "The snapshot's name.",
			},
			"description": schema.StringAttribute{Optional: true, PlanModifiers: replace, Description: "Its description."},
			"created_at":  schema.StringAttribute{Computed: true, PlanModifiers: keepID, Description: "When it was taken."},
		},
	}
}

type snapshotWire struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
	CreatedAt   *string `json:"created_at"`
}

func (c *Client) snapshot(ctx context.Context, vm, name string) (*snapshotWire, error) {
	var page struct {
		Items []snapshotWire `json:"items"`
	}
	if err := c.call(ctx, "GET", "/vms/"+esc(vm)+"/snapshots", nil, &page); err != nil {
		return nil, notFound(err)
	}
	for i := range page.Items {
		if page.Items[i].Name == name {
			return &page.Items[i], nil
		}
	}
	return nil, errNotFound
}

func (r *snapshotResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m snapshotModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if m.Node.IsUnknown() {
		m.Node = types.StringValue("local")
	}
	c := r.client.at(memberOf(m.Node))
	body := map[string]any{"name": m.Name.ValueString(), "description": optStr(m.Description)}
	var started startedTask
	if err := c.call(ctx, "POST", "/vms/"+esc(m.VM.ValueString())+"/snapshots", body, &started); err != nil {
		resp.Diagnostics.AddError("Cannot take the snapshot", err.Error())
		return
	}
	if started.TaskID != "" {
		if err := c.waitTask(ctx, started.TaskID); err != nil {
			resp.Diagnostics.AddError("The snapshot failed", err.Error())
			return
		}
	}
	m.ID = types.StringValue(m.VM.ValueString() + "/" + m.Name.ValueString())
	s, err := c.snapshot(ctx, m.VM.ValueString(), m.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("The snapshot was taken but cannot be read", err.Error())
		return
	}
	m.CreatedAt = optionalString(s.CreatedAt, types.StringNull())
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *snapshotResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m snapshotModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if m.Node.IsNull() {
		m.Node = types.StringValue("local") // imported
	}
	c := r.client.at(memberOf(m.Node))
	vm, name, _ := strings.Cut(m.ID.ValueString(), "/")
	s, err := c.snapshot(ctx, vm, name)
	if errors.Is(err, errNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the snapshot", err.Error())
		return
	}
	m.VM, m.Name = types.StringValue(vm), types.StringValue(name)
	m.Description = optionalString(s.Description, m.Description)
	m.CreatedAt = optionalString(s.CreatedAt, types.StringNull())
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *snapshotResource) Update(context.Context, resource.UpdateRequest, *resource.UpdateResponse) {
	// Every attribute replaces the snapshot.
}

func (r *snapshotResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m snapshotModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	c := r.client.at(memberOf(m.Node))
	vm, name, _ := strings.Cut(m.ID.ValueString(), "/")
	var started startedTask
	err := c.call(ctx, "DELETE", "/vms/"+esc(vm)+"/snapshots/"+esc(name), nil, &started)
	if errors.Is(notFound(err), errNotFound) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot delete the snapshot", err.Error())
		return
	}
	if started.TaskID != "" {
		if err := c.waitTask(ctx, started.TaskID); err != nil {
			resp.Diagnostics.AddError("Deleting the snapshot failed", err.Error())
		}
	}
}

// ImportState takes `<vm>/<snapshot>`, or `<node>/<vm>/<snapshot>`.
func (r *snapshotResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if id := importOn(ctx, req, resp, "<vm>/<snapshot>"); id != nil {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), strings.Join(id, "/"))...)
	}
}

// ---- hyperlite_vm_ha ----

type haResource struct{ configured }

type haModel struct {
	ID   types.String `tfsdk:"id"`
	VM   types.String `tfsdk:"vm"`
	Node types.String `tfsdk:"node"`
}

func newHAResource() resource.Resource { return &haResource{} }

func (r *haResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm_ha"
}

func (r *haResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "The HA protection of a VM, as vSphere's `vsphere_ha_vm_override`: Hyperlite keeps its definition to start it on another node when its own fails. Its disks must be on shared storage.",
		Attributes: map[string]schema.Attribute{
			"id":   schema.StringAttribute{Computed: true, PlanModifiers: keepID, Description: "The VM's name."},
			"vm":   schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "The VM protected."},
			"node": schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()}, Description: "The node it runs on (`local` when absent)."},
		},
	}
}

func (r *haResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m haModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		Node string `json:"node"`
	}
	if err := r.client.call(ctx, "POST", "/ha/"+esc(m.VM.ValueString())+"/enable", map[string]any{"node": optStr(m.Node)}, &out); err != nil {
		resp.Diagnostics.AddError("Cannot protect the VM", err.Error())
		return
	}
	m.ID = m.VM
	if out.Node == "" {
		out.Node = "local"
	}
	m.Node = types.StringValue(out.Node)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *haResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m haModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		VMName string `json:"vm_name"`
		Node   string `json:"node"`
	}
	err := notFound(r.client.call(ctx, "GET", "/ha/"+esc(m.ID.ValueString()), nil, &out))
	if errors.Is(err, errNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the HA protection", err.Error())
		return
	}
	if out.Node == "" {
		out.Node = "local"
	}
	m.VM, m.Node = types.StringValue(out.VMName), types.StringValue(out.Node)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *haResource) Update(context.Context, resource.UpdateRequest, *resource.UpdateResponse) {}

func (r *haResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m haModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if err := r.client.call(ctx, "DELETE", "/ha/"+esc(m.ID.ValueString()), nil, nil); err != nil && !errors.Is(notFound(err), errNotFound) {
		resp.Diagnostics.AddError("Cannot remove the HA protection", err.Error())
	}
}

func (r *haResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
