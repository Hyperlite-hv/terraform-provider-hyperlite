package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
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
