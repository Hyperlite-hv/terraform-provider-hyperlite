package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Objects known by a numeric id (pools, groups, roles, assignments): the Terraform id is that number, as text.

func setOf(values []string) types.Set {
	sort.Strings(values)
	elems := make([]attr.Value, len(values))
	for i, v := range values {
		elems[i] = types.StringValue(v)
	}
	return types.SetValueMust(types.StringType, elems)
}

func stringsOf(ctx context.Context, s types.Set) []string {
	out := []string{}
	if !s.IsNull() && !s.IsUnknown() {
		s.ElementsAs(ctx, &out, false)
	}
	sort.Strings(out)
	return out
}

var keepID = []planmodifier.String{stringplanmodifier.UseStateForUnknown()}

// ---- hyperlite_resource_pool ----

type poolResource struct{ configured }

type poolModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
}

func newPoolResource() resource.Resource { return &poolResource{} }

func (r *poolResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_resource_pool"
}

func (r *poolResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A pool of VMs: an assignment on it gives rights on every VM it holds (a VM joins it with `hyperlite_vm.resource_pool`).",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, PlanModifiers: keepID, Description: "The pool's id."},
			"name":        schema.StringAttribute{Required: true, Description: "Its name."},
			"description": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), Description: "Its description."},
		},
	}
}

type poolWire struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	VMs         []string `json:"vms"`
}

func (r *poolResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m poolModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var made poolWire
	if err := r.client.API.Call(ctx, "POST", "/pools", map[string]any{"name": m.Name.ValueString(), "description": m.Description.ValueString()}, &made); err != nil {
		resp.Diagnostics.AddError("Cannot create the pool", err.Error())
		return
	}
	m.ID = types.StringValue(strconv.FormatInt(made.ID, 10))
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *poolResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m poolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var p poolWire
	err := notFound(r.client.API.Call(ctx, "GET", "/pools/"+m.ID.ValueString(), nil, &p))
	if errors.Is(err, errNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the pool", err.Error())
		return
	}
	m.Name, m.Description = types.StringValue(p.Name), types.StringValue(p.Description)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *poolResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state poolModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"description": plan.Description.ValueString()}
	if plan.Name.ValueString() != state.Name.ValueString() {
		body["name"] = plan.Name.ValueString()
	}
	if err := r.client.API.Call(ctx, "PATCH", "/pools/"+state.ID.ValueString(), body, nil); err != nil {
		resp.Diagnostics.AddError("Cannot change the pool", err.Error())
		return
	}
	plan.ID = state.ID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *poolResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m poolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if err := r.client.API.Call(ctx, "DELETE", "/pools/"+m.ID.ValueString(), nil, nil); err != nil && !errors.Is(notFound(err), errNotFound) {
		resp.Diagnostics.AddError("Cannot delete the pool", err.Error())
	}
}

func (r *poolResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// ---- hyperlite_user ----

type userResource struct{ configured }

type userModel struct {
	ID       types.String `tfsdk:"id"`
	Username types.String `tfsdk:"username"`
	Password types.String `tfsdk:"password"`
	Role     types.String `tfsdk:"role"`
}

func newUserResource() resource.Resource { return &userResource{} }

func (r *userResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user"
}

func (r *userResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A local account of Hyperlite. Its rights beyond its global role come from assignments (`hyperlite_acl`).",
		Attributes: map[string]schema.Attribute{
			"id":       schema.StringAttribute{Computed: true, PlanModifiers: keepID, Description: "The account's name."},
			"username": schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, Description: "Its name (a change replaces the account)."},
			"password": schema.StringAttribute{Required: true, Sensitive: true, Description: "Its password, checked by Hyperlite's password policy. Never read back: a change made elsewhere is not seen."},
			"role": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("viewer"),
				Validators:  []validator.String{stringvalidator.OneOf("admin", "viewer")},
				Description: "Its global role: `viewer` (reads everything) or `admin`.",
			},
		},
	}
}

// The main rules of the server's password policy (app/core/password_policy.py), refused at plan as the dashboard
// does; the server stays the authority for the others (sequences, well-known passwords).
func (r *userResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m userModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.Password.IsNull() || m.Password.IsUnknown() {
		return
	}
	if problem := passwordProblem(m.Password.ValueString(), m.Username.ValueString()); problem != "" {
		resp.Diagnostics.AddAttributeError(path.Root("password"), "Password refused by Hyperlite's policy", problem+".")
	}
}

func passwordProblem(password, username string) string {
	low := strings.ToLower(password)
	distinct := map[rune]bool{}
	for _, c := range low {
		distinct[c] = true
	}
	switch {
	case utf8.RuneCountInString(password) < 12:
		return "The password must contain at least 12 characters"
	case len(password) > 72:
		return "The password must not exceed 72 bytes (bcrypt ignores the rest)"
	case utf8.RuneCountInString(username) >= 3 && strings.Contains(low, strings.ToLower(username)):
		return "The password must not contain the account name"
	case len(distinct) < 5:
		return "The password is too repetitive: use more different characters"
	}
	return ""
}

func (r *userResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m userModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"username": m.Username.ValueString(), "password": m.Password.ValueString(), "role": m.Role.ValueString()}
	if err := r.client.API.Call(ctx, "POST", "/auth/users", body, nil); err != nil {
		resp.Diagnostics.AddError("Cannot create the account", err.Error())
		return
	}
	m.ID = m.Username
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *userResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m userModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var u struct {
		Username string `json:"username"`
		Role     string `json:"role"`
	}
	err := notFound(r.client.API.Call(ctx, "GET", "/auth/users/"+esc(m.ID.ValueString()), nil, &u))
	if errors.Is(err, errNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the account", err.Error())
		return
	}
	m.Username, m.Role = types.StringValue(u.Username), types.StringValue(u.Role)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *userResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state userModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{}
	if plan.Role.ValueString() != state.Role.ValueString() {
		body["role"] = plan.Role.ValueString()
	}
	if plan.Password.ValueString() != state.Password.ValueString() {
		body["password"] = plan.Password.ValueString()
	}
	if len(body) > 0 {
		if err := r.client.API.Call(ctx, "PATCH", "/auth/users/"+esc(state.ID.ValueString()), body, nil); err != nil {
			resp.Diagnostics.AddError("Cannot change the account", err.Error())
			return
		}
	}
	plan.ID = state.ID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *userResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m userModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if err := r.client.API.Call(ctx, "DELETE", "/auth/users/"+esc(m.ID.ValueString()), nil, nil); err != nil && !errors.Is(notFound(err), errNotFound) {
		resp.Diagnostics.AddError("Cannot delete the account", err.Error())
	}
}

func (r *userResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// ---- hyperlite_group ----

type groupResource struct{ configured }

type groupModel struct {
	ID      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	Members types.Set    `tfsdk:"members"`
}

func newGroupResource() resource.Resource { return &groupResource{} }

func (r *groupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group"
}

func (r *groupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A group of accounts: an assignment to it gives its rights to every member.",
		Attributes: map[string]schema.Attribute{
			"id":   schema.StringAttribute{Computed: true, PlanModifiers: keepID, Description: "The group's id."},
			"name": schema.StringAttribute{Required: true, Description: "Its name."},
			"members": schema.SetAttribute{
				ElementType: types.StringType, Optional: true, Computed: true,
				PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown()},
				Description:   "Its members, by account name; when absent, the members are not managed here.",
			},
		},
	}
}

type groupWire struct {
	ID      int64    `json:"id"`
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

func (r *groupResource) syncMembers(ctx context.Context, id string, have, want []string) error {
	h := map[string]bool{}
	for _, u := range have {
		h[u] = true
	}
	w := map[string]bool{}
	for _, u := range want {
		w[u] = true
		if !h[u] {
			if err := r.client.API.Call(ctx, "POST", "/groups/"+id+"/members", map[string]any{"username": u}, nil); err != nil {
				return fmt.Errorf("adding %s: %w", u, err)
			}
		}
	}
	for _, u := range have {
		if !w[u] {
			if err := r.client.API.Call(ctx, "DELETE", "/groups/"+id+"/members/"+esc(u), nil, nil); err != nil {
				return fmt.Errorf("removing %s: %w", u, err)
			}
		}
	}
	return nil
}

func (r *groupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m groupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var made groupWire
	if err := r.client.API.Call(ctx, "POST", "/groups", map[string]any{"name": m.Name.ValueString()}, &made); err != nil {
		resp.Diagnostics.AddError("Cannot create the group", err.Error())
		return
	}
	m.ID = types.StringValue(strconv.FormatInt(made.ID, 10))
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), m.ID)...)
	if !m.Members.IsUnknown() {
		if err := r.syncMembers(ctx, m.ID.ValueString(), nil, stringsOf(ctx, m.Members)); err != nil {
			resp.Diagnostics.AddError("The group was made but its members could not be set", err.Error())
			return
		}
	}
	m.Members = setOf(stringsOf(ctx, m.Members))
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *groupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m groupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var g groupWire
	err := notFound(r.client.API.Call(ctx, "GET", "/groups/"+m.ID.ValueString(), nil, &g))
	if errors.Is(err, errNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the group", err.Error())
		return
	}
	m.Name, m.Members = types.StringValue(g.Name), setOf(append([]string{}, g.Members...))
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *groupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state groupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if plan.Name.ValueString() != state.Name.ValueString() {
		if err := r.client.API.Call(ctx, "PATCH", "/groups/"+id, map[string]any{"name": plan.Name.ValueString()}, nil); err != nil {
			resp.Diagnostics.AddError("Cannot rename the group", err.Error())
			return
		}
	}
	if !plan.Members.IsUnknown() {
		if err := r.syncMembers(ctx, id, stringsOf(ctx, state.Members), stringsOf(ctx, plan.Members)); err != nil {
			resp.Diagnostics.AddError("Cannot set the group's members", err.Error())
			return
		}
	} else {
		plan.Members = state.Members
	}
	plan.ID = state.ID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *groupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m groupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if err := r.client.API.Call(ctx, "DELETE", "/groups/"+m.ID.ValueString(), nil, nil); err != nil && !errors.Is(notFound(err), errNotFound) {
		resp.Diagnostics.AddError("Cannot delete the group", err.Error())
	}
}

func (r *groupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// ---- hyperlite_role ----

type roleResource struct{ configured }

type roleModel struct {
	ID         types.String `tfsdk:"id"`
	Key        types.String `tfsdk:"key"`
	Name       types.String `tfsdk:"name"`
	Privileges types.Set    `tfsdk:"privileges"`
}

func newRoleResource() resource.Resource { return &roleResource{} }

func (r *roleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_role"
}

func (r *roleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A custom role: a set of privileges (`vm.power`, `vm.allocate`, `storage.allocate`...), given by `hyperlite_acl`. A change of privileges keeps its assignments.",
		Attributes: map[string]schema.Attribute{
			"id":         schema.StringAttribute{Computed: true, PlanModifiers: keepID, Description: "The role's id."},
			"key":        schema.StringAttribute{Computed: true, PlanModifiers: keepID, Description: "What `hyperlite_acl.role` takes: `custom:<id>`."},
			"name":       schema.StringAttribute{Required: true, Description: "Its name."},
			"privileges": schema.SetAttribute{ElementType: types.StringType, Required: true, Description: "Its privileges (Hyperlite's GET /api/v1/acl/privileges lists them)."},
		},
	}
}

func (r *roleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m roleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var made struct {
		ID  int64  `json:"id"`
		Key string `json:"key"`
	}
	body := map[string]any{"name": m.Name.ValueString(), "privileges": stringsOf(ctx, m.Privileges)}
	if err := r.client.API.Call(ctx, "POST", "/acl/custom-roles", body, &made); err != nil {
		resp.Diagnostics.AddError("Cannot create the role", err.Error())
		return
	}
	m.ID, m.Key = types.StringValue(strconv.FormatInt(made.ID, 10)), types.StringValue(made.Key)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *roleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m roleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var role struct {
		Key        string   `json:"key"`
		Label      string   `json:"label"`
		Privileges []string `json:"privileges"`
	}
	err := notFound(r.client.API.Call(ctx, "GET", "/acl/custom-roles/"+m.ID.ValueString(), nil, &role))
	if errors.Is(err, errNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the role", err.Error())
		return
	}
	m.Key, m.Name, m.Privileges = types.StringValue(role.Key), types.StringValue(role.Label), setOf(role.Privileges)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *roleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state roleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"privileges": stringsOf(ctx, plan.Privileges)}
	if plan.Name.ValueString() != state.Name.ValueString() {
		body["name"] = plan.Name.ValueString()
	}
	if err := r.client.API.Call(ctx, "PATCH", "/acl/custom-roles/"+state.ID.ValueString(), body, nil); err != nil {
		resp.Diagnostics.AddError("Cannot change the role", err.Error())
		return
	}
	plan.ID, plan.Key = state.ID, state.Key
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *roleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m roleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if err := r.client.API.Call(ctx, "DELETE", "/acl/custom-roles/"+m.ID.ValueString(), nil, nil); err != nil && !errors.Is(notFound(err), errNotFound) {
		resp.Diagnostics.AddError("Cannot delete the role", err.Error())
	}
}

func (r *roleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// ---- hyperlite_acl ----

type aclResource struct{ configured }

type aclModel struct {
	ID           types.String `tfsdk:"id"`
	SubjectType  types.String `tfsdk:"subject_type"`
	Subject      types.String `tfsdk:"subject"`
	Role         types.String `tfsdk:"role"`
	ResourceType types.String `tfsdk:"resource_type"`
	Resource     types.String `tfsdk:"resource"`
}

func newACLResource() resource.Resource { return &aclResource{} }

func (r *aclResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_acl"
}

func (r *aclResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "One assignment: a role given to an account or a group on one object. One resource per assignment, so several configurations can give rights on the same object without overwriting each other.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, PlanModifiers: keepID, Description: "The assignment's id."},
			"subject_type": schema.StringAttribute{
				Required: true, PlanModifiers: replace, Validators: []validator.String{stringvalidator.OneOf("user", "group")},
				Description: "`user` or `group`.",
			},
			"subject": schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "The account's name, or the group's id (`hyperlite_group.x.id`)."},
			"role":    schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "A predefined role (`drive`, `operator`, `manager`) or a custom one (`hyperlite_role.x.key`)."},
			"resource_type": schema.StringAttribute{
				Required: true, PlanModifiers: replace, Validators: []validator.String{stringvalidator.OneOf("vm", "pool", "container", "storage", "network")},
				Description: "What it is given on: `vm`, `pool` (of VMs), `container`, `storage` (a storage pool) or `network`.",
			},
			"resource": schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "The object: a VM's, container's, storage pool's or network's name, or a pool's id (`hyperlite_resource_pool.x.id`)."},
		},
	}
}

func (r *aclResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m aclModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{
		"subject_type": m.SubjectType.ValueString(), "subject_id": m.Subject.ValueString(), "role": m.Role.ValueString(),
		"resource_type": m.ResourceType.ValueString(), "resource_id": m.Resource.ValueString(),
	}
	var made struct {
		ID int64 `json:"id"`
	}
	if err := r.client.API.Call(ctx, "POST", "/acl", body, &made); err != nil {
		resp.Diagnostics.AddError("Cannot give the role", err.Error())
		return
	}
	m.ID = types.StringValue(strconv.FormatInt(made.ID, 10))
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *aclResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m aclModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var a struct {
		SubjectType  string `json:"subject_type"`
		SubjectID    string `json:"subject_id"`
		Role         string `json:"role"`
		ResourceType string `json:"resource_type"`
		ResourceID   string `json:"resource_id"`
	}
	err := notFound(r.client.API.Call(ctx, "GET", "/acl/"+m.ID.ValueString(), nil, &a))
	if errors.Is(err, errNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the assignment", err.Error())
		return
	}
	m.SubjectType, m.Subject, m.Role = types.StringValue(a.SubjectType), types.StringValue(a.SubjectID), types.StringValue(a.Role)
	m.ResourceType, m.Resource = types.StringValue(a.ResourceType), types.StringValue(a.ResourceID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *aclResource) Update(context.Context, resource.UpdateRequest, *resource.UpdateResponse) {
	// Every attribute replaces the assignment.
}

func (r *aclResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m aclModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if err := r.client.API.Call(ctx, "DELETE", "/acl/"+m.ID.ValueString(), nil, nil); err != nil && !errors.Is(notFound(err), errNotFound) {
		resp.Diagnostics.AddError("Cannot remove the assignment", err.Error())
	}
}

func (r *aclResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := strconv.ParseInt(strings.TrimSpace(req.ID), 10, 64); err != nil {
		resp.Diagnostics.AddError("Import id", "Give the assignment's id, as the Permissions page shows it.")
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
