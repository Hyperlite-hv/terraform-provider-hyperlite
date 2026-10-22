package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// idFollowsName plans the id of an object known by its name and renamed in place (a network, a storage pool, a
// template): the prior id while the name stays, unknown when it changes. UseStateForUnknown alone would plan the old
// name, and Terraform would report the new one as an inconsistent result after the rename.
type idFollowsName struct{}

func (idFollowsName) Description(context.Context) string {
	return "The prior id while the name stays; known after the apply when the name changes."
}

func (m idFollowsName) MarkdownDescription(ctx context.Context) string { return m.Description(ctx) }

func (idFollowsName) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.StateValue.IsNull() || req.Plan.Raw.IsNull() {
		return // created (unknown until made) or destroyed
	}
	var planned, prior types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("name"), &planned)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("name"), &prior)...)
	if planned.Equal(prior) {
		resp.PlanValue = req.StateValue
	}
}

// nodeAttribute is the `node` of what lives on one member of the cluster (a network, a pool, a template...): made
// there, read and changed there (Hyperlite sends each request on), and made again on another member when it changes.
// Absent: the node the provider talks to.
func nodeAttribute(r *configured, what string) schema.StringAttribute {
	return schema.StringAttribute{
		Optional: true, Computed: true,
		PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), otherNodeReplaces{r}},
		Description: fmt.Sprintf("The member of the cluster the %s is on (absent: the node the provider talks to). "+
			"Another member makes it again there.", what),
	}
}

// otherNodeReplaces replaces the object when `node` names another member: "local", nothing and the provider's own
// node's name are one node, and changing between them changes nothing.
type otherNodeReplaces struct{ r *configured }

func (otherNodeReplaces) Description(context.Context) string {
	return "Makes the object again when it moves to another member of the cluster."
}

func (m otherNodeReplaces) MarkdownDescription(ctx context.Context) string { return m.Description(ctx) }

func (m otherNodeReplaces) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.StateValue.IsNull() || req.PlanValue.IsUnknown() || m.r.client == nil {
		return
	}
	if !m.r.client.sameNode(req.PlanValue.ValueString(), req.StateValue.ValueString()) {
		resp.RequiresReplace = true
	}
}
