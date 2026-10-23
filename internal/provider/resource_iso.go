package provider

import (
	"context"
	"errors"
	"io"
	"mime/multipart"
	"os"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/float64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// isoNameRe is the server's ISO_NAME_RE (app/routers/isos.py) with the .iso extension it requires: distribution
// images are named with dots and underscores, not with the VM name rule.
var isoNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,200}\.iso$`)

// uploadISO sends a local file to the ISO library of this client's member (POST /isos, multipart), streamed: an image
// of several GB is never held in memory, and the request has no time limit but the context's.
func (c *Client) uploadISO(ctx context.Context, name, source string) error {
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	defer f.Close()
	body, w := io.Pipe()
	form := multipart.NewWriter(w)
	go func() {
		part, err := form.CreateFormFile("file", name)
		if err == nil {
			_, err = io.Copy(part, f)
		}
		if err == nil {
			err = form.Close()
		}
		w.CloseWithError(err)
	}()
	err = c.post(ctx, "/isos", form.FormDataContentType(), body)
	body.CloseWithError(err) // the writer stops when the request ends early
	return err
}

// ---- hyperlite_iso ----

type isoResource struct{ configured }

type isoResourceModel struct {
	ID         types.String  `tfsdk:"id"`
	Node       types.String  `tfsdk:"node"`
	Name       types.String  `tfsdk:"name"`
	Source     types.String  `tfsdk:"source"`
	SourceHash types.String  `tfsdk:"source_hash"`
	SizeMB     types.Float64 `tfsdk:"size_mb"`
	AddedAt    types.String  `tfsdk:"added_at"`
}

func newISOResource() resource.Resource { return &isoResource{} }

func (r *isoResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_iso"
}

// replacedOnceKnown replaces the image when the attribute changes, except after an import: the state then has no
// source, and the configuration giving one changes nothing on the server.
var replacedOnceKnown = stringplanmodifier.RequiresReplaceIf(
	func(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
		resp.RequiresReplace = !req.StateValue.IsNull()
	},
	"Uploads the image again when it changes (not after an import).",
	"Uploads the image again when it changes (not after an import).",
)

func (r *isoResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keepString := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "An ISO image uploaded from this machine to a node's library, to install VMs from it (`hyperlite_vm.iso`). Through the node the provider talks to, which sends it on to the member `node` names (Hyperlite 1.3 or later).",
		Attributes: map[string]schema.Attribute{
			"id":   schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{idFollowsName{}}, Description: "The file's name in the library."},
			"node": nodeAttribute(&r.configured, "image"),
			"name": schema.StringAttribute{
				Required: true,
				Validators: []validator.String{stringvalidator.RegexMatches(isoNameRe,
					"letters, digits, '.', '-', '_' and '+', starting with a letter or a digit, ending with `.iso`")},
				Description: "The file's name in the library (`debian-13.1.0-amd64-netinst.iso`). A change renames it.",
			},
			"source": schema.StringAttribute{
				Required: true, PlanModifiers: []planmodifier.String{replacedOnceKnown},
				Description: "The local file uploaded. Another path uploads it again.",
			},
			"source_hash": schema.StringAttribute{
				Optional: true, PlanModifiers: []planmodifier.String{replacedOnceKnown},
				Description: "Set it to `filesha256(source)` to upload the image again when the file changes at the same path.",
			},
			"size_mb":  schema.Float64Attribute{Computed: true, PlanModifiers: []planmodifier.Float64{float64planmodifier.UseStateForUnknown()}, Description: "Its size, in MB."},
			"added_at": schema.StringAttribute{Computed: true, PlanModifiers: keepString, Description: "When it was added (RFC 3339)."},
		},
	}
}

type isoWire struct {
	Name    string  `json:"name"`
	SizeMB  float64 `json:"size_mb"`
	AddedAt string  `json:"added_at"`
}

func (r *isoResource) iso(ctx context.Context, c *Client, name string) (*isoWire, error) {
	isos, err := list[isoWire](ctx, c, "/isos")
	if err != nil {
		return nil, err
	}
	for i := range isos {
		if isos[i].Name == name {
			return &isos[i], nil
		}
	}
	return nil, errNotFound
}

func (m *isoResourceModel) from(i *isoWire) {
	m.ID, m.Name, m.SizeMB, m.AddedAt = types.StringValue(i.Name), types.StringValue(i.Name), types.Float64Value(i.SizeMB), optional(i.AddedAt)
}

func (r *isoResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m isoResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if m.Node.IsUnknown() {
		m.Node = types.StringValue("local")
	}
	c := r.client.at(memberOf(m.Node))
	// The file is read at the upload only: once the image is in the library, a plan does not need it.
	if err := c.uploadISO(ctx, m.Name.ValueString(), m.Source.ValueString()); err != nil {
		resp.Diagnostics.AddError("Cannot upload the ISO image", err.Error())
		return
	}
	i, err := r.iso(ctx, c, m.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the uploaded ISO image", err.Error())
		return
	}
	m.from(i)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *isoResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m isoResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if m.Node.IsNull() {
		m.Node = types.StringValue("local") // imported
	}
	i, err := r.iso(ctx, r.client.at(memberOf(m.Node)), m.ID.ValueString())
	if errors.Is(err, errNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the ISO library", err.Error())
		return
	}
	m.from(i)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *isoResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state isoResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	c := r.client.at(memberOf(state.Node))
	// In place: a rename, or a source named after an import (nothing to upload).
	if !plan.Name.Equal(state.Name) {
		if err := c.call(ctx, "POST", "/isos/"+esc(state.ID.ValueString())+"/rename", map[string]any{"new_name": plan.Name.ValueString()}, nil); err != nil {
			resp.Diagnostics.AddError("Cannot rename the ISO image", err.Error())
			return
		}
	}
	i, err := r.iso(ctx, c, plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the ISO image", err.Error())
		return
	}
	plan.from(i)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *isoResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m isoResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := notFound(r.client.at(memberOf(m.Node)).call(ctx, "DELETE", "/isos/"+esc(m.ID.ValueString())+"?confirm=true", nil, nil))
	if err != nil && !errors.Is(err, errNotFound) {
		resp.Diagnostics.AddError("Cannot delete the ISO image", err.Error())
	}
}

// ImportState takes `<file>`, or `<node>/<file>`.
func (r *isoResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if id := importOn(ctx, req, resp, "<file>"); id != nil {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id[0])...)
	}
}
