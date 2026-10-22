package provider

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/netip"
	stdpath "path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Hyperlite-hv/Hyperlite/cli/hyperlite"
)

var (
	_ resource.Resource                   = (*vmResource)(nil)
	_ resource.ResourceWithImportState    = (*vmResource)(nil)
	_ resource.ResourceWithValidateConfig = (*vmResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*vmResource)(nil)
)

func newVMResource() resource.Resource { return &vmResource{} }

type vmResource struct{ client *Client }

type vmModel struct {
	ID              types.String    `tfsdk:"id"`
	Name            types.String    `tfsdk:"name"`
	Node            types.String    `tfsdk:"node"`
	VCPU            types.Int64     `tfsdk:"vcpu"`
	MemoryMB        types.Int64     `tfsdk:"memory_mb"`
	Firmware        types.String    `tfsdk:"firmware"`
	GuestOS         types.String    `tfsdk:"guest_os"`
	StoragePool     types.String    `tfsdk:"storage_pool"`
	ResourcePool    types.Int64     `tfsdk:"resource_pool"`
	ISO             types.String    `tfsdk:"iso"`
	Autostart       types.Bool      `tfsdk:"autostart"`
	Tags            types.Set       `tfsdk:"tags"`
	Notes           types.String    `tfsdk:"notes"`
	ShutdownTimeout types.String    `tfsdk:"shutdown_timeout"`
	ForceStop       types.Bool      `tfsdk:"force_stop"`
	WaitForIP       types.String    `tfsdk:"wait_for_ip"`
	IPAddress       types.String    `tfsdk:"ip_address"`
	State           types.String    `tfsdk:"state"`
	Clone           *cloneModel     `tfsdk:"clone"`
	CloudInit       *cloudInitModel `tfsdk:"cloud_init"`
	Customize       *customizeModel `tfsdk:"customize"`
	Disks           []diskModel     `tfsdk:"disk"`
	Interfaces      []ifaceModel    `tfsdk:"network_interface"`
	Timeouts        timeouts.Value  `tfsdk:"timeouts"`
}

type cloneModel struct {
	Template types.String `tfsdk:"template"`
	VM       types.String `tfsdk:"vm"`
}

type cloudInitModel struct {
	User     types.String `tfsdk:"user"`
	Password types.String `tfsdk:"password"`
	SSHKeys  types.List   `tfsdk:"ssh_keys"`
}

type customizeModel struct {
	Hostname  types.String         `tfsdk:"hostname"`
	Domain    types.String         `tfsdk:"domain"`
	DNS       types.List           `tfsdk:"dns"`
	DNSSearch types.List           `tfsdk:"dns_search"`
	UserData  types.String         `tfsdk:"user_data"`
	Network   []customNetworkModel `tfsdk:"network"`
}

type customNetworkModel struct {
	Interface types.Int64  `tfsdk:"interface"`
	Addresses types.List   `tfsdk:"addresses"`
	Gateway   types.String `tfsdk:"gateway"`
}

type diskModel struct {
	SizeGB      types.Int64  `tfsdk:"size_gb"`
	StoragePool types.String `tfsdk:"storage_pool"`
	Target      types.String `tfsdk:"target"`
}

type ifaceModel struct {
	Network types.String `tfsdk:"network"`
	VLANTag types.Int64  `tfsdk:"vlan_tag"`
	MAC     types.String `tfsdk:"mac"`
}

// The server's rule for every resource name (app/core/vm_builder.py NAME_RE).
var nameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]{1,62}$`)

// A volume's name may end with the extension the server adds.
var volumeNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]{1,62}(\.qcow2)?$`)

const nameRule = "2 to 63 letters, digits or dashes, starting with a letter or a digit"

func (r *vmResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm"
}

func (r *vmResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	c, err := clientOf(req.ProviderData)
	if err != nil {
		resp.Diagnostics.AddError("Provider not configured", err.Error())
		return
	}
	r.client = c
}

func (r *vmResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "A virtual machine: made from Hyperlite's cloud image, an installation ISO, a template or another VM; " +
			"customised before its first boot; started once made.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, PlanModifiers: keep,
				Description: "The VM's UUID: it stays the same when the VM is renamed.",
			},
			"name": schema.StringAttribute{
				Required:    true,
				Validators:  []validator.String{stringvalidator.RegexMatches(nameRe, nameRule)},
				Description: "The VM's name. A change renames it (the VM is stopped for it, then started again).",
			},
			"node": schema.StringAttribute{
				Computed: true, PlanModifiers: keep,
				Description: "The node the VM runs on (`local` for the node the provider talks to).",
			},
			"vcpu": schema.Int64Attribute{
				Required: true, Validators: []validator.Int64{int64validator.AtLeast(1)},
				Description: "Virtual CPUs. A change stops the VM, applies, and starts it again.",
			},
			"memory_mb": schema.Int64Attribute{
				Required: true, Validators: []validator.Int64{int64validator.AtLeast(64)},
				Description: "Memory in MB. A change stops the VM, applies, and starts it again.",
			},
			"firmware": schema.StringAttribute{
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()},
				Validators:    []validator.String{stringvalidator.OneOf("bios", "uefi", "uefi_secure")},
				Description:   "`bios`, `uefi` or `uefi_secure` (Secure Boot and TPM 2.0, what Windows 11 asks). A VM made from Hyperlite's cloud image or an ISO only; a clone keeps its source's.",
			},
			"guest_os": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("auto"), PlanModifiers: replace,
				Validators:  []validator.String{stringvalidator.OneOf("auto", "linux", "windows", "other")},
				Description: "The system an ISO installs, for the devices it gets (`auto` reads it from the ISO's name).",
			},
			"storage_pool": schema.StringAttribute{
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()},
				Description:   "The storage pool of the disks made with the VM (`default` when absent). A disk added later goes on its own `storage_pool`, or this one.",
			},
			"resource_pool": schema.Int64Attribute{
				Optional:    true,
				Description: "The pool (its id) the VM is a member of: its assignments give rights on the VM. Required from an account that is not an administrator (vm.allocate on the pool).",
			},
			"iso": schema.StringAttribute{
				Optional: true, PlanModifiers: replace,
				Description: "An installation ISO of the node's library: the VM gets a blank system disk and is installed from its console. Without `iso` nor `clone`, the VM is made from Hyperlite's cloud image (Debian 12) and needs `cloud_init`.",
			},
			"autostart": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Started when its node starts.",
			},
			"tags": schema.SetAttribute{
				ElementType: types.StringType, Optional: true, Computed: true,
				// The server lowercases a tag: one it would change is refused here, not reported as a drift.
				Validators:  []validator.Set{setvalidator.ValueStringsAre(stringvalidator.RegexMatches(tagRe, tagRule)), setvalidator.SizeAtMost(16)},
				Description: "Tags, as the dashboard shows them (and backup jobs select VMs by).",
			},
			"notes": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString(""),
				Description: "Notes, as the dashboard shows them.",
			},
			"shutdown_timeout": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("3m"),
				Validators:  []validator.String{durationValidator{}},
				Description: "How long a clean shutdown may take before a change that needs the VM stopped, or its deletion.",
			},
			"force_stop": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Stop the VM hard when its clean shutdown did not end within `shutdown_timeout`.",
			},
			"wait_for_ip": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("5m"),
				Validators:  []validator.String{durationValidator{}},
				Description: "How long to wait, once the VM is started, for its address (`0s`: do not wait). Not waited for when `customize` gives the first interface a static address.",
			},
			"ip_address": schema.StringAttribute{
				Computed: true, PlanModifiers: keep,
				Description: "The VM's address: the static one of `customize`, else its DHCP lease or its guest agent's.",
			},
			"state": schema.StringAttribute{
				Computed:    true,
				Description: "`active`, `stopped`, `paused`, `crashed`...",
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
		Blocks: map[string]schema.Block{
			"clone": schema.SingleNestedBlock{
				Description: "Make the VM from a template or from another (stopped) VM, with a copy of its disks. Its system, account and firmware come from the source.",
				Attributes: map[string]schema.Attribute{
					"template": schema.StringAttribute{Optional: true, PlanModifiers: replace, Description: "The template's name (data source `hyperlite_template`)."},
					"vm":       schema.StringAttribute{Optional: true, PlanModifiers: replace, Description: "The name of a stopped VM to clone."},
				},
			},
			"cloud_init": schema.SingleNestedBlock{
				Description: "The account of a VM made from a cloud image (Hyperlite's or a template's): applied before its first boot, then at its next boot after a change.",
				Attributes: map[string]schema.Attribute{
					"user":     schema.StringAttribute{Optional: true, Description: "The account's name. Required in the block."},
					"password": schema.StringAttribute{Optional: true, Sensitive: true, Description: "Its password (never read back: a change made elsewhere is not seen)."},
					"ssh_keys": schema.ListAttribute{ElementType: types.StringType, Optional: true, Description: "Its SSH public keys."},
				},
			},
			"customize": schema.SingleNestedBlock{
				Description: "The guest's customisation, written on its cloud-init drive as vSphere's `customize` and Proxmox VE's cloud-init options are: before its first boot, then at its next boot after a change.",
				Attributes: map[string]schema.Attribute{
					"hostname":   schema.StringAttribute{Optional: true, Validators: []validator.String{hostLabel()}, Description: "Host name (the VM's name when absent)."},
					"domain":     schema.StringAttribute{Optional: true, Validators: []validator.String{domainName()}, Description: "Domain, for the FQDN."},
					"dns":        schema.ListAttribute{ElementType: types.StringType, Optional: true, Validators: []validator.List{listvalidator.ValueStringsAre(ipAddress())}, Description: "Resolvers."},
					"dns_search": schema.ListAttribute{ElementType: types.StringType, Optional: true, Validators: []validator.List{listvalidator.ValueStringsAre(domainName())}, Description: "Search domains."},
					"user_data":  schema.StringAttribute{Optional: true, Description: "A cloud-config added to Hyperlite's own (packages, runcmd, write_files...); its account and host-name keys are refused."},
				},
				Blocks: map[string]schema.Block{
					"network": schema.ListNestedBlock{
						Description: "A static configuration of one interface; the others use DHCP.",
						NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
							"interface": schema.Int64Attribute{Optional: true, Description: "The index of the `network_interface` (0, the first, when absent)."},
							"addresses": schema.ListAttribute{ElementType: types.StringType, Required: true, Validators: []validator.List{listvalidator.SizeAtLeast(1), listvalidator.ValueStringsAre(ipWithPrefix())}, Description: "Addresses with their prefix: `10.0.20.11/24`."},
							"gateway":   schema.StringAttribute{Optional: true, Validators: []validator.String{ipAddress()}, Description: "Default gateway."},
						}},
					},
				},
			},
			"disk": schema.ListNestedBlock{
				Description: "The VM's disks, in order: the first is its system disk. A disk grows in place (never shrinks); one added is created and attached; one removed is detached and deleted.",
				Validators:  []validator.List{listvalidator.SizeAtLeast(1)},
				NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
					"size_gb": schema.Int64Attribute{Required: true, Validators: []validator.Int64{int64validator.AtLeast(1)}, Description: "Size in GB."},
					"storage_pool": schema.StringAttribute{
						Optional: true, Computed: true, PlanModifiers: keep,
						Description: "Its storage pool; a change moves the disk (administrators).",
					},
					"target": schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "Its device name in the VM (`sda`, `vdb`...)."},
				}},
			},
			"network_interface": schema.ListNestedBlock{
				Description: "The VM's interfaces, in order. A change of network keeps the interface's MAC address.",
				Validators:  []validator.List{listvalidator.SizeAtLeast(1)},
				NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
					"network":  schema.StringAttribute{Required: true, Description: "The network's name (data source `hyperlite_network`)."},
					"vlan_tag": schema.Int64Attribute{Optional: true, Validators: []validator.Int64{int64validator.Between(1, 4094)}, Description: "802.1Q tag, on a network that carries tags."},
					"mac":      schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "Its MAC address."},
				}},
			},
		},
	}
}

// ---- Validation ----

func (r *vmResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m vmModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	cloned := m.Clone != nil
	if cloned {
		t, v := !m.Clone.Template.IsNull(), !m.Clone.VM.IsNull()
		if t == v && !m.Clone.Template.IsUnknown() && !m.Clone.VM.IsUnknown() {
			resp.Diagnostics.AddAttributeError(path.Root("clone"), "One source", "Give `template` or `vm` in `clone`, not both.")
		}
		if !m.ISO.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("iso"), "Two sources", "A VM is made from `clone` or from `iso`, not both.")
		}
		if !m.Firmware.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("firmware"), "Firmware of a clone", "A clone keeps its source's firmware: leave `firmware` out.")
		}
	}
	install := !m.ISO.IsNull()
	if install && (m.CloudInit != nil || m.Customize != nil) {
		resp.Diagnostics.AddAttributeError(path.Root("iso"), "No cloud-init on an ISO installation",
			"A VM installed from an ISO has no cloud-init drive: its account and network are set in its installer. Remove `cloud_init` and `customize`.")
	}
	if !cloned && !install {
		if m.CloudInit == nil || m.CloudInit.User.IsNull() || m.CloudInit.Password.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("cloud_init"), "An account is needed",
				"A VM made from Hyperlite's cloud image needs `cloud_init` with `user` and `password`.")
		}
	}
	if m.CloudInit != nil && m.CloudInit.User.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("cloud_init").AtName("user"), "No user", "`cloud_init` needs `user`.")
	}
	if m.Customize != nil {
		seen := map[int64]bool{}
		for i, n := range m.Customize.Network {
			idx := n.Interface.ValueInt64()
			if !n.Interface.IsUnknown() && (idx < 0 || int(idx) >= len(m.Interfaces)) {
				resp.Diagnostics.AddAttributeError(path.Root("customize").AtName("network").AtListIndex(i).AtName("interface"),
					"No such interface", fmt.Sprintf("The VM has %d network_interface; interface %d does not exist.", len(m.Interfaces), idx))
			}
			if seen[idx] {
				resp.Diagnostics.AddAttributeError(path.Root("customize").AtName("network").AtListIndex(i).AtName("interface"),
					"Interface configured twice", fmt.Sprintf("Interface %d has two `network` blocks.", idx))
			}
			seen[idx] = true
			if gw, err := netip.ParseAddr(n.Gateway.ValueString()); err == nil && !n.Addresses.IsUnknown() {
				var addrs []types.String
				n.Addresses.ElementsAs(ctx, &addrs, false)
				inside := false
				for _, a := range addrs {
					if a.IsUnknown() {
						inside = true // known at apply only: the server checks it
					}
					if p, err := netip.ParsePrefix(a.ValueString()); err == nil && p.Masked().Contains(gw) {
						inside = true
					}
				}
				if !inside {
					resp.Diagnostics.AddAttributeError(path.Root("customize").AtName("network").AtListIndex(i).AtName("gateway"),
						"Gateway out of reach", "The gateway must be inside the network of one of the interface's addresses.")
				}
			}
		}
	}
}

// ModifyPlan refuses a disk that would shrink (Hyperlite grows disks only, as vSphere does), and marks what Hyperlite
// gives a disk or an interface added (its device name, its pool, its MAC address) as known after the apply: the
// framework leaves the computed attributes of a new element of a block null.
func (r *vmResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var state, plan vmModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if !req.State.Raw.IsNull() {
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	changed := false
	for i := range plan.Disks {
		if i < len(state.Disks) {
			if !plan.Disks[i].SizeGB.IsUnknown() && plan.Disks[i].SizeGB.ValueInt64() < state.Disks[i].SizeGB.ValueInt64() {
				resp.Diagnostics.AddAttributeError(path.Root("disk").AtListIndex(i).AtName("size_gb"), "A disk does not shrink",
					fmt.Sprintf("Disk %d is %d GB: it can grow, never shrink (its file system would be cut). Remove the disk to delete it.", i, state.Disks[i].SizeGB.ValueInt64()))
			}
			continue
		}
		if plan.Disks[i].Target.IsNull() {
			plan.Disks[i].Target, changed = types.StringUnknown(), true
		}
		if plan.Disks[i].StoragePool.IsNull() {
			plan.Disks[i].StoragePool, changed = types.StringUnknown(), true
		}
	}
	for i := len(state.Interfaces); i < len(plan.Interfaces); i++ {
		if plan.Interfaces[i].MAC.IsNull() {
			plan.Interfaces[i].MAC, changed = types.StringUnknown(), true
		}
	}
	if changed && !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
	}
}

// ---- CRUD ----

func (r *vmResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan vmModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Create(ctx, 30*time.Minute)
	resp.Diagnostics.Append(d...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	name := plan.Name.ValueString()
	if err := r.make(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Cannot create the VM", err.Error())
		return
	}
	vm, err := r.client.vmByName(ctx, name, "")
	if err != nil {
		resp.Diagnostics.AddError("The VM was made but cannot be read", err.Error())
		return
	}
	plan.ID = types.StringValue(vm.UUID)
	// From here the VM exists: a failure leaves it in the state, tainted, and the next apply replaces it.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), plan.ID)...)
	if err := r.configure(ctx, &plan, nil, true); err != nil {
		resp.Diagnostics.AddError("The VM was made but its configuration failed", err.Error())
		return
	}
	if err := r.client.startVM(ctx, name); err != nil && !isConflict(err) {
		resp.Diagnostics.AddError("The VM was made but did not start", err.Error())
		return
	}
	if err := r.awaitAddress(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("The VM started but has no address", err.Error())
		return
	}
	resp.Diagnostics.Append(r.read(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// make creates the VM, stopped: from a template, another VM, an ISO or Hyperlite's cloud image.
func (r *vmResource) make(ctx context.Context, m *vmModel) error {
	name := m.Name.ValueString()
	switch {
	case m.Clone != nil && !m.Clone.Template.IsNull():
		body := map[string]any{"new_name": name, "network": m.Interfaces[0].Network.ValueString()}
		return r.client.runTask(ctx, "POST", "/templates/"+esc(m.Clone.Template.ValueString())+"/deploy", body)
	case m.Clone != nil:
		return r.client.runTask(ctx, "POST", "/vms/"+esc(m.Clone.VM.ValueString())+"/clone", map[string]any{"new_name": name})
	}
	disks := make([]map[string]any, len(m.Disks))
	for i, d := range m.Disks {
		disks[i] = map[string]any{"size_gb": d.SizeGB.ValueInt64()}
	}
	body := map[string]any{
		"name":      name,
		"vcpu":      m.VCPU.ValueInt64(),
		"memory_mb": m.MemoryMB.ValueInt64(),
		"disks":     disks,
		"network":   m.Interfaces[0].Network.ValueString(),
		"guest_os":  m.GuestOS.ValueString(),
	}
	if !m.Firmware.IsNull() && !m.Firmware.IsUnknown() {
		body["firmware"] = m.Firmware.ValueString()
	}
	if !m.StoragePool.IsNull() && !m.StoragePool.IsUnknown() {
		body["storage_pool"] = m.StoragePool.ValueString()
	}
	if !m.ResourcePool.IsNull() {
		body["resource_pool"] = m.ResourcePool.ValueInt64()
	}
	if !m.ISO.IsNull() {
		body["iso"] = m.ISO.ValueString()
	} else if m.CloudInit != nil {
		body["username"] = m.CloudInit.User.ValueString()
		body["password"] = m.CloudInit.Password.ValueString()
	}
	return r.client.runTask(ctx, "POST", "/vms", body)
}

// configure brings the VM to the plan: resources, disks, interfaces, cloud-init, pool, start at boot, notes and tags.
// `prior` is the state before (nil at creation). The VM is stopped for what needs it; `fresh`: it is new and stopped.
func (r *vmResource) configure(ctx context.Context, plan, prior *vmModel, fresh bool) error {
	c := r.client
	name := plan.Name.ValueString()
	vm, err := c.vmByName(ctx, name, "")
	if err != nil {
		return err
	}
	if vm.VCPU != plan.VCPU.ValueInt64() || int64(math.Round(vm.MemoryMB)) != plan.MemoryMB.ValueInt64() {
		if err := r.stopped(ctx, plan, func() error {
			return c.API.Call(ctx, "PATCH", "/vms/"+esc(name), map[string]any{"vcpu": plan.VCPU.ValueInt64(), "memory_mb": plan.MemoryMB.ValueInt64()}, nil)
		}); err != nil {
			return fmt.Errorf("CPU and memory: %w", err)
		}
	}
	if err := r.reconcileDisks(ctx, plan); err != nil {
		return fmt.Errorf("disks: %w", err)
	}
	if err := r.reconcileInterfaces(ctx, plan); err != nil {
		return fmt.Errorf("network interfaces: %w", err)
	}
	if plan.CloudInit != nil || plan.Customize != nil || (prior != nil && (prior.CloudInit != nil || prior.Customize != nil)) {
		if err := r.writeCloudInit(ctx, plan, prior); err != nil {
			return fmt.Errorf("cloud-init: %w", err)
		}
	}
	if err := r.reconcilePool(ctx, plan, prior, fresh); err != nil {
		return fmt.Errorf("resource pool: %w", err)
	}
	if fresh && !plan.Autostart.ValueBool() {
		// Off by default for a new VM: nothing to write.
	} else if err := c.putBoot(ctx, name, plan.Autostart.ValueBool()); err != nil {
		return fmt.Errorf("start at boot: %w", err)
	}
	tags := []string{}
	if !plan.Tags.IsNull() && !plan.Tags.IsUnknown() {
		plan.Tags.ElementsAs(ctx, &tags, false)
	}
	if !(fresh && len(tags) == 0 && plan.Notes.ValueString() == "") {
		if err := c.putMeta(ctx, name, objectMeta{Notes: plan.Notes.ValueString(), Tags: tags}); err != nil {
			return fmt.Errorf("notes and tags: %w", err)
		}
	}
	return nil
}

// stopped runs fn with the VM stopped, then starts it again if it was running: the rule of vSphere's
// shutdown_wait_timeout and force_power_off.
func (r *vmResource) stopped(ctx context.Context, m *vmModel, fn func() error) error {
	c := r.client
	name := m.Name.ValueString()
	vm, err := c.vmByName(ctx, name, "")
	if err != nil {
		return err
	}
	running := vm.State != "stopped"
	if running {
		if err := c.stopVM(ctx, name, duration(m.ShutdownTimeout, 3*time.Minute), m.ForceStop.ValueBool()); err != nil {
			return err
		}
	}
	if err := fn(); err != nil {
		if running {
			_ = c.startVM(ctx, name) // put it back as it was; the error that matters is fn's
		}
		return err
	}
	if running {
		return c.startVM(ctx, name)
	}
	return nil
}

// rename stops the VM (Hyperlite renames a stopped VM), renames it, and starts it again under its new name if it ran.
func (r *vmResource) rename(ctx context.Context, state *vmModel, to string) error {
	c := r.client
	from := state.Name.ValueString()
	vm, err := c.vmByName(ctx, from, "")
	if err != nil {
		return err
	}
	running := vm.State != "stopped"
	if running {
		if err := c.stopVM(ctx, from, duration(state.ShutdownTimeout, 3*time.Minute), state.ForceStop.ValueBool()); err != nil {
			return err
		}
	}
	if err := c.API.Call(ctx, "POST", "/vms/"+esc(from)+"/rename", map[string]any{"new_name": to}, nil); err != nil {
		if running {
			_ = c.startVM(ctx, from) // put it back as it was; the error that matters is the rename's
		}
		return err
	}
	if running {
		return c.startVM(ctx, to)
	}
	return nil
}

// tryLive runs fn, and again with the VM stopped when Hyperlite answers that it must be (409).
func (r *vmResource) tryLive(ctx context.Context, m *vmModel, fn func() error) error {
	err := fn()
	if err == nil || !isConflict(err) {
		return err
	}
	return r.stopped(ctx, m, fn)
}

func (r *vmResource) reconcileDisks(ctx context.Context, plan *vmModel) error {
	c := r.client
	name := plan.Name.ValueString()
	actual, err := c.vmDisks(ctx, name, "")
	if err != nil {
		return err
	}
	for i := range plan.Disks {
		want := plan.Disks[i]
		if i < len(actual) {
			have := actual[i]
			if have.SizeGB != nil && float64(want.SizeGB.ValueInt64()) > *have.SizeGB+0.01 {
				body := map[string]any{"size_gb": want.SizeGB.ValueInt64()}
				if err := r.tryLive(ctx, plan, func() error {
					return c.API.Call(ctx, "POST", "/vms/"+esc(name)+"/disks/"+esc(have.Target)+"/resize", body, nil)
				}); err != nil {
					return fmt.Errorf("growing %s: %w", have.Target, err)
				}
			}
			if pool := want.StoragePool; !pool.IsNull() && !pool.IsUnknown() && have.Pool != nil && pool.ValueString() != *have.Pool {
				if err := r.stopped(ctx, plan, func() error {
					return c.runMove(ctx, name, have.Target, pool.ValueString())
				}); err != nil {
					return fmt.Errorf("moving %s to %s: %w", have.Target, pool.ValueString(), err)
				}
			}
			continue
		}
		pool := "default"
		switch {
		case !want.StoragePool.IsNull() && !want.StoragePool.IsUnknown():
			pool = want.StoragePool.ValueString()
		case !plan.StoragePool.IsNull() && !plan.StoragePool.IsUnknown():
			pool = plan.StoragePool.ValueString()
		}
		volume := fmt.Sprintf("%s-data%d", name, i)
		var made struct {
			Name string `json:"name"`
		}
		if err := c.API.Call(ctx, "POST", "/storage/"+esc(pool)+"/volumes", map[string]any{"name": volume, "size_gb": want.SizeGB.ValueInt64()}, &made); err != nil {
			return fmt.Errorf("creating disk %d on %s: %w", i, pool, err)
		}
		if made.Name == "" {
			made.Name = volume
		}
		target := nextTarget(actual)
		attach := map[string]any{"volume_name": made.Name, "pool": pool, "target_dev": target}
		if err := r.tryLive(ctx, plan, func() error {
			return c.API.Call(ctx, "POST", "/vms/"+esc(name)+"/disks", attach, nil)
		}); err != nil {
			return fmt.Errorf("attaching disk %d: %w", i, err)
		}
		actual = append(actual, vmDisk{Target: target, Type: "disk"})
	}
	for i := len(actual) - 1; i >= len(plan.Disks); i-- {
		have := actual[i]
		if err := r.tryLive(ctx, plan, func() error {
			return c.API.Call(ctx, "DELETE", "/vms/"+esc(name)+"/disks/"+esc(have.Target), nil, nil)
		}); err != nil {
			return fmt.Errorf("detaching %s: %w", have.Target, err)
		}
		if have.Pool != nil && have.Source != "" {
			// The disk removed from the configuration is deleted, as Terraform deletes what it no longer manages.
			if err := c.API.Call(ctx, "DELETE", "/storage/"+esc(*have.Pool)+"/volumes/"+esc(stdpath.Base(have.Source))+"?confirm=true", nil, nil); err != nil && !errors.Is(notFound(err), errNotFound) {
				return fmt.Errorf("deleting the volume of %s: %w", have.Target, err)
			}
		}
	}
	return nil
}

// runMove moves a disk to another storage pool (POST /vms/{name}/disks/{dev}/move, a task).
func (c *Client) runMove(ctx context.Context, name, target, pool string) error {
	var started startedTask
	if err := c.API.Call(ctx, "POST", "/vms/"+esc(name)+"/disks/"+esc(target)+"/move", map[string]any{"pool": pool}, &started); err != nil {
		return err
	}
	if started.TaskID == "" {
		return nil
	}
	_, err := c.API.WaitTask(ctx, started.TaskID, hyperlite.WaitOptions{})
	return err
}

// nextTarget is the first free device name on the bus of the VM's disks (sdb after sda, vdc after vdb).
func nextTarget(disks []vmDisk) string {
	prefix := "sd"
	used := map[string]bool{}
	for i, d := range disks {
		if i == 0 && len(d.Target) > 2 {
			prefix = d.Target[:2]
		}
		used[d.Target] = true
	}
	for ch := 'a'; ch <= 'z'; ch++ {
		if t := prefix + string(ch); !used[t] {
			return t
		}
	}
	return prefix + "aa"
}

func (r *vmResource) reconcileInterfaces(ctx context.Context, plan *vmModel) error {
	c := r.client
	name := plan.Name.ValueString()
	actual, err := c.vmInterfaces(ctx, name, "")
	if err != nil {
		return err
	}
	for i, want := range plan.Interfaces {
		var tag any
		if !want.VLANTag.IsNull() {
			tag = want.VLANTag.ValueInt64()
		}
		body := map[string]any{"network": want.Network.ValueString(), "vlan_tag": tag}
		if i < len(actual) {
			have := actual[i]
			sameTag := (have.VLAN == nil && want.VLANTag.IsNull()) || (have.VLAN != nil && !want.VLANTag.IsNull() && *have.VLAN == want.VLANTag.ValueInt64())
			if have.Network != want.Network.ValueString() || !sameTag {
				if err := r.tryLive(ctx, plan, func() error {
					return c.API.Call(ctx, "PUT", "/vms/"+esc(name)+"/network?mac="+have.MAC, body, nil)
				}); err != nil {
					return fmt.Errorf("moving interface %d (%s) to %s: %w", i, have.MAC, want.Network.ValueString(), err)
				}
			}
			continue
		}
		if err := r.tryLive(ctx, plan, func() error {
			return c.API.Call(ctx, "POST", "/vms/"+esc(name)+"/interfaces", body, nil)
		}); err != nil {
			return fmt.Errorf("adding interface %d on %s: %w", i, want.Network.ValueString(), err)
		}
	}
	for i := len(actual) - 1; i >= len(plan.Interfaces); i-- {
		mac := actual[i].MAC
		if err := r.tryLive(ctx, plan, func() error {
			return c.API.Call(ctx, "DELETE", "/vms/"+esc(name)+"/interfaces/"+esc(mac), nil, nil)
		}); err != nil {
			return fmt.Errorf("removing interface %d (%s): %w", i, mac, err)
		}
	}
	return nil
}

// writeCloudInit writes the account and the customisation on the VM's drive, every field, so it holds exactly the
// configuration (a block removed clears what it set).
func (r *vmResource) writeCloudInit(ctx context.Context, plan, prior *vmModel) error {
	c := r.client
	name := plan.Name.ValueString()
	current, err := c.vmCloudInit(ctx, name)
	if err != nil {
		return err
	}
	if !current.Available {
		return errors.New("this VM has no cloud-init drive (installed from an ISO, or cloned from such a VM): remove `cloud_init` and `customize`")
	}
	body := cloudInitUpdate{User: current.User, SSHKeys: []string{}, Network: []staticInterface{}, DNS: []string{}, DNSSearch: []string{}}
	if plan.CloudInit != nil {
		body.User = plan.CloudInit.User.ValueString()
		if !plan.CloudInit.Password.IsNull() {
			changed := prior == nil || prior.CloudInit == nil || prior.CloudInit.Password.ValueString() != plan.CloudInit.Password.ValueString()
			if changed {
				pw := plan.CloudInit.Password.ValueString()
				body.Password = &pw
			}
		}
		if !plan.CloudInit.SSHKeys.IsNull() {
			plan.CloudInit.SSHKeys.ElementsAs(ctx, &body.SSHKeys, false)
		}
	} else {
		body.SSHKeys = current.SSHKeys
	}
	if body.User == "" {
		return errors.New("the VM's cloud-init account is not known: give `cloud_init { user = ... }`")
	}
	if cz := plan.Customize; cz != nil {
		body.Hostname, body.Domain, body.UserData = cz.Hostname.ValueString(), cz.Domain.ValueString(), cz.UserData.ValueString()
		if !cz.DNS.IsNull() {
			cz.DNS.ElementsAs(ctx, &body.DNS, false)
		}
		if !cz.DNSSearch.IsNull() {
			cz.DNSSearch.ElementsAs(ctx, &body.DNSSearch, false)
		}
		ifaces, err := c.vmInterfaces(ctx, name, "")
		if err != nil {
			return err
		}
		for _, n := range cz.Network {
			idx := int(n.Interface.ValueInt64())
			if idx >= len(ifaces) {
				return fmt.Errorf("customize.network: the VM has no interface %d", idx)
			}
			entry := staticInterface{MAC: ifaces[idx].MAC, Addresses: []string{}}
			n.Addresses.ElementsAs(ctx, &entry.Addresses, false)
			if !n.Gateway.IsNull() {
				gw := n.Gateway.ValueString()
				entry.Gateway = &gw
			}
			body.Network = append(body.Network, entry)
		}
	}
	return c.putCloudInit(ctx, name, body)
}

func (r *vmResource) reconcilePool(ctx context.Context, plan, prior *vmModel, fresh bool) error {
	c := r.client
	name := plan.Name.ValueString()
	want := plan.ResourcePool
	var had types.Int64
	if prior != nil {
		had = prior.ResourcePool
	}
	if fresh && plan.Clone == nil {
		return nil // POST /vms put it in its pool
	}
	if want.Equal(had) {
		return nil
	}
	if !had.IsNull() {
		if err := c.API.Call(ctx, "DELETE", fmt.Sprintf("/pools/%d/members/%s", had.ValueInt64(), esc(name)), nil, nil); err != nil && !errors.Is(notFound(err), errNotFound) {
			return err
		}
	}
	if !want.IsNull() {
		return c.API.Call(ctx, "POST", fmt.Sprintf("/pools/%d/members", want.ValueInt64()), map[string]any{"vm_name": name}, nil)
	}
	return nil
}

// awaitAddress fills ip_address: the static address of the first interface, else the one the VM gets.
func (r *vmResource) awaitAddress(ctx context.Context, m *vmModel) error {
	if ip := staticAddress(ctx, m); ip != "" {
		m.IPAddress = types.StringValue(ip)
		return nil
	}
	within := duration(m.WaitForIP, 5*time.Minute)
	if within == 0 {
		m.IPAddress = types.StringNull()
		return nil
	}
	ip, err := r.client.waitIP(ctx, m.Name.ValueString(), within)
	if err != nil {
		return err
	}
	m.IPAddress = types.StringValue(ip)
	return nil
}

func staticAddress(ctx context.Context, m *vmModel) string {
	if m.Customize == nil {
		return ""
	}
	for _, n := range m.Customize.Network {
		if n.Interface.ValueInt64() != 0 {
			continue
		}
		var addrs []string
		n.Addresses.ElementsAs(ctx, &addrs, false)
		for _, a := range addrs {
			if ip, _, ok := strings.Cut(a, "/"); ok && !strings.Contains(ip, ":") {
				return ip
			}
		}
	}
	return ""
}

func (r *vmResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state vmModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	vm, err := r.client.findVM(ctx, state.ID.ValueString())
	if errors.Is(err, errNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the VM", err.Error())
		return
	}
	state.Name = types.StringValue(vm.Name)
	resp.Diagnostics.Append(r.read(ctx, &state)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// read fills the model from Hyperlite: what the VM is, by its name.
func (r *vmResource) read(ctx context.Context, m *vmModel) diag.Diagnostics {
	var diags diag.Diagnostics
	c := r.client
	name := m.Name.ValueString()
	node := m.Node.ValueString()
	if m.Node.IsUnknown() || m.Node.IsNull() {
		node = ""
	}
	vm, err := c.vmByName(ctx, name, node)
	if err != nil {
		diags.AddError("Cannot read the VM", err.Error())
		return diags
	}
	m.ID, m.Node, m.State = types.StringValue(vm.UUID), types.StringValue(vm.Node), types.StringValue(vm.State)
	m.VCPU, m.MemoryMB = types.Int64Value(vm.VCPU), types.Int64Value(int64(math.Round(vm.MemoryMB)))
	m.Firmware = types.StringValue(vm.Firmware)
	if m.GuestOS.IsNull() || m.GuestOS.IsUnknown() {
		m.GuestOS = types.StringValue("auto")
	}

	disks, err := c.vmDisks(ctx, name, node)
	if err != nil {
		diags.AddError("Cannot read the VM's disks", err.Error())
		return diags
	}
	m.Disks = make([]diskModel, len(disks))
	for i, d := range disks {
		size := int64(0)
		if d.SizeGB != nil {
			size = int64(math.Round(*d.SizeGB))
		}
		pool := types.StringNull()
		if d.Pool != nil {
			pool = types.StringValue(*d.Pool)
		}
		m.Disks[i] = diskModel{SizeGB: types.Int64Value(size), StoragePool: pool, Target: types.StringValue(d.Target)}
	}
	if (m.StoragePool.IsNull() || m.StoragePool.IsUnknown()) && len(disks) > 0 {
		m.StoragePool = m.Disks[0].StoragePool
	}
	if m.StoragePool.IsUnknown() {
		m.StoragePool = types.StringNull()
	}

	ifaces, err := c.vmInterfaces(ctx, name, node)
	if err != nil {
		diags.AddError("Cannot read the VM's interfaces", err.Error())
		return diags
	}
	m.Interfaces = make([]ifaceModel, len(ifaces))
	macIndex := map[string]int64{}
	for i, f := range ifaces {
		tag := types.Int64Null()
		if f.VLAN != nil {
			tag = types.Int64Value(*f.VLAN)
		}
		m.Interfaces[i] = ifaceModel{Network: types.StringValue(f.Network), VLANTag: tag, MAC: types.StringValue(f.MAC)}
		macIndex[strings.ToLower(f.MAC)] = int64(i)
	}

	if boot, err := c.vmBoot(ctx, name, node); err == nil {
		m.Autostart = types.BoolValue(boot.Autostart)
	} else {
		diags.AddError("Cannot read the VM's start at boot", err.Error())
	}
	if meta, err := c.vmMeta(ctx, name, node); err == nil {
		elems := make([]attr.Value, len(meta.Tags))
		for i, t := range meta.Tags {
			elems[i] = types.StringValue(t)
		}
		m.Tags = types.SetValueMust(types.StringType, elems)
		m.Notes = types.StringValue(meta.Notes)
	} else {
		diags.AddError("Cannot read the VM's notes and tags", err.Error())
	}

	if m.CloudInit != nil || m.Customize != nil {
		ci, err := c.vmCloudInit(ctx, name)
		if err != nil {
			diags.AddError("Cannot read the VM's cloud-init", err.Error())
			return diags
		}
		if m.CloudInit != nil && ci.User != "" {
			m.CloudInit.User = types.StringValue(ci.User)
			if !(m.CloudInit.SSHKeys.IsNull() && len(ci.SSHKeys) == 0) {
				m.CloudInit.SSHKeys = stringList(ci.SSHKeys)
			}
		}
		if m.Customize != nil {
			m.Customize.Hostname = optionalString(ci.Hostname, m.Customize.Hostname)
			m.Customize.Domain = optionalString(ci.Domain, m.Customize.Domain)
			m.Customize.UserData = optionalString(ci.UserData, m.Customize.UserData)
			if !(m.Customize.DNS.IsNull() && len(ci.DNS) == 0) {
				m.Customize.DNS = stringList(ci.DNS)
			}
			if !(m.Customize.DNSSearch.IsNull() && len(ci.DNSSearch) == 0) {
				m.Customize.DNSSearch = stringList(ci.DNSSearch)
			}
			var nets []customNetworkModel
			for _, n := range ci.Network {
				idx, ok := macIndex[strings.ToLower(n.MAC)]
				if !ok {
					continue
				}
				gw := types.StringNull()
				if n.Gateway != nil {
					gw = types.StringValue(*n.Gateway)
				}
				iface := types.Int64Value(idx)
				nets = append(nets, customNetworkModel{Interface: iface, Addresses: stringList(n.Addresses), Gateway: gw})
			}
			sort.Slice(nets, func(a, b int) bool { return nets[a].Interface.ValueInt64() < nets[b].Interface.ValueInt64() })
			// An entry written without `interface` (the first) is read back as interface 0: kept as written.
			for i := range nets {
				if i < len(m.Customize.Network) && m.Customize.Network[i].Interface.IsNull() && nets[i].Interface.ValueInt64() == 0 {
					nets[i].Interface = types.Int64Null()
				}
			}
			m.Customize.Network = nets
		}
	}

	if !m.ResourcePool.IsNull() {
		// Read back for an administrator; another account may not list the pools, and keeps what it set.
		if pools, err := c.poolsOf(ctx, name); err == nil {
			found := false
			for _, p := range pools {
				found = found || p == m.ResourcePool.ValueInt64()
			}
			if !found {
				m.ResourcePool = types.Int64Null()
			}
		}
	}
	if m.IPAddress.IsUnknown() {
		m.IPAddress = types.StringNull()
	}
	if vm.IP != "" && staticAddress(ctx, m) == "" {
		m.IPAddress = types.StringValue(vm.IP)
	}
	return diags
}

func (r *vmResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state vmModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Update(ctx, 30*time.Minute)
	resp.Diagnostics.Append(d...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if plan.Name.ValueString() != state.Name.ValueString() {
		if err := r.rename(ctx, &state, plan.Name.ValueString()); err != nil {
			resp.Diagnostics.AddError("Cannot rename the VM", err.Error())
			return
		}
	}
	if err := r.configure(ctx, &plan, &state, false); err != nil {
		resp.Diagnostics.AddError("Cannot update the VM", err.Error())
		return
	}
	plan.ID = state.ID
	plan.Node = state.Node
	resp.Diagnostics.Append(r.read(ctx, &plan)...)
	if ip := staticAddress(ctx, &plan); ip != "" {
		plan.IPAddress = types.StringValue(ip)
	} else if plan.IPAddress.IsNull() {
		plan.IPAddress = state.IPAddress
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *vmResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state vmModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := state.Timeouts.Delete(ctx, 15*time.Minute)
	resp.Diagnostics.Append(d...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c := r.client
	vm, err := c.findVM(ctx, state.ID.ValueString())
	if errors.Is(err, errNotFound) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the VM", err.Error())
		return
	}
	if err := c.stopVM(ctx, vm.Name, duration(state.ShutdownTimeout, 3*time.Minute), state.ForceStop.ValueBool()); err != nil {
		resp.Diagnostics.AddError("Cannot stop the VM before deleting it", err.Error())
		return
	}
	if err := c.API.Call(ctx, "DELETE", "/vms/"+esc(vm.Name)+"?confirm=true", nil, nil); err != nil && !errors.Is(notFound(err), errNotFound) {
		resp.Diagnostics.AddError("Cannot delete the VM", err.Error())
	}
}

// ImportState takes the VM's name, `<node>/<name>` or its UUID.
func (r *vmResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := req.ID
	if _, name, ok := strings.Cut(id, "/"); ok {
		id = name
	}
	uuid := id
	if !uuidRe.MatchString(id) {
		vm, err := r.client.vmByName(ctx, id, "")
		if err != nil {
			resp.Diagnostics.AddError("No such VM", fmt.Sprintf("%q: %v", req.ID, err))
			return
		}
		uuid = vm.UUID
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), uuid)...)
	for attrName, v := range map[string]attr.Value{
		"shutdown_timeout": types.StringValue("3m"), "force_stop": types.BoolValue(true), "wait_for_ip": types.StringValue("5m"),
		"guest_os": types.StringValue("auto"),
	} {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(attrName), v)...)
	}
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ---- small helpers ----

func stringList(values []string) types.List {
	elems := make([]attr.Value, len(values))
	for i, v := range values {
		elems[i] = types.StringValue(v)
	}
	return types.ListValueMust(types.StringType, elems)
}

// optionalString is the server's value; an empty one stays as the configuration wrote it (null or "").
func optionalString(server *string, configured types.String) types.String {
	if server == nil || *server == "" {
		if configured.ValueString() == "" {
			return configured
		}
		return types.StringNull()
	}
	return types.StringValue(*server)
}

func duration(v types.String, fallback time.Duration) time.Duration {
	if v.IsNull() || v.IsUnknown() {
		return fallback
	}
	d, err := time.ParseDuration(v.ValueString())
	if err != nil {
		return fallback
	}
	return d
}

type durationValidator struct{}

func (durationValidator) Description(context.Context) string {
	return "a duration such as 30s, 5m or 1h"
}
func (v durationValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (durationValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if d, err := time.ParseDuration(req.ConfigValue.ValueString()); err != nil || d < 0 {
		resp.Diagnostics.AddAttributeError(req.Path, "Not a duration", fmt.Sprintf("%q: give a duration such as 30s, 5m or 1h.", req.ConfigValue.ValueString()))
	}
}
