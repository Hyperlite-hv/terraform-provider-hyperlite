package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Hyperlite-hv/Hyperlite/cli/hyperlite"
)

// The calls of API v1 the resources make, typed. Each one names the route it uses; the shapes are those of Hyperlite's
// docs/api.md and docs/api/openapi-v1.json.

// errNotFound is what a read returns when the object is gone: the resource then leaves the state.
var errNotFound = errors.New("not found")

func notFound(err error) error {
	if hyperlite.IsNotFound(err) {
		return errNotFound
	}
	return err
}

func esc(s string) string { return url.PathEscape(s) }

// withNode adds ?node= when the object is on another member of the cluster.
func withNode(path, node string) string {
	if node == "" || node == "local" {
		return path
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + "node=" + url.QueryEscape(node)
}

// ---- VMs ----

type vmSummary struct {
	Name     string  `json:"name"`
	UUID     string  `json:"uuid"`
	State    string  `json:"state"`
	VCPU     int64   `json:"vcpu"`
	MemoryMB float64 `json:"memory_mb"`
	IP       string  `json:"ip"`
	Firmware string  `json:"firmware"`
	Node     string  `json:"node"`
}

// findVM finds a VM by its UUID in the whole cluster (GET /vms?cluster=true, one call from the cluster's status).
func (c *Client) findVM(ctx context.Context, uuid string) (*vmSummary, error) {
	var page struct {
		Items []vmSummary `json:"items"`
	}
	if err := c.API.Call(ctx, "GET", "/vms?cluster=true", nil, &page); err != nil {
		return nil, err
	}
	for i := range page.Items {
		if strings.EqualFold(page.Items[i].UUID, uuid) {
			vm := page.Items[i]
			if vm.Node == "" {
				vm.Node = "local"
			}
			return &vm, nil
		}
	}
	return nil, errNotFound
}

// vmByName reads one VM (GET /vms/{name}).
func (c *Client) vmByName(ctx context.Context, name, node string) (*vmSummary, error) {
	var vm vmSummary
	if err := c.API.Call(ctx, "GET", withNode("/vms/"+esc(name), node), nil, &vm); err != nil {
		return nil, notFound(err)
	}
	if vm.Node == "" {
		vm.Node = node
	}
	if vm.Node == "" {
		vm.Node = "local"
	}
	return &vm, nil
}

type vmDisk struct {
	Target string   `json:"target"`
	Bus    string   `json:"bus"`
	Type   string   `json:"type"`
	Source string   `json:"source"`
	SizeGB *float64 `json:"size_gb"`
	Pool   *string  `json:"pool"`
}

// vmDisks is the VM's hard disks (not its CD drives), in the order of its definition (GET /vms/{name}/disks).
func (c *Client) vmDisks(ctx context.Context, name, node string) ([]vmDisk, error) {
	var page struct {
		Items []vmDisk `json:"items"`
	}
	if err := c.API.Call(ctx, "GET", withNode("/vms/"+esc(name)+"/disks", node), nil, &page); err != nil {
		return nil, notFound(err)
	}
	out := make([]vmDisk, 0, len(page.Items))
	for _, d := range page.Items {
		if d.Type == "disk" {
			out = append(out, d)
		}
	}
	return out, nil
}

type vmInterface struct {
	MAC     string `json:"mac"`
	Network string `json:"network"`
	VLAN    *int64 `json:"vlan"`
}

func (c *Client) vmInterfaces(ctx context.Context, name, node string) ([]vmInterface, error) {
	var net struct {
		Interfaces []vmInterface `json:"interfaces"`
	}
	if err := c.API.Call(ctx, "GET", withNode("/vms/"+esc(name)+"/network", node), nil, &net); err != nil {
		return nil, notFound(err)
	}
	return net.Interfaces, nil
}

type staticInterface struct {
	MAC       string   `json:"mac,omitempty"`
	Addresses []string `json:"addresses"`
	Gateway   *string  `json:"gateway"`
}

type cloudInitState struct {
	Available bool              `json:"available"`
	User      string            `json:"user"`
	SSHKeys   []string          `json:"ssh_keys"`
	Hostname  *string           `json:"hostname"`
	Domain    *string           `json:"domain"`
	Network   []staticInterface `json:"network"`
	DNS       []string          `json:"dns"`
	DNSSearch []string          `json:"dns_search"`
	UserData  *string           `json:"user_data"`
}

func (c *Client) vmCloudInit(ctx context.Context, name string) (*cloudInitState, error) {
	var ci cloudInitState
	if err := c.API.Call(ctx, "GET", "/vms/"+esc(name)+"/cloud-init", nil, &ci); err != nil {
		return nil, notFound(err)
	}
	return &ci, nil
}

// cloudInitUpdate is PUT /vms/{name}/cloud-init: every field is sent, so the drive holds exactly the configuration.
type cloudInitUpdate struct {
	User      string            `json:"user"`
	Password  *string           `json:"password,omitempty"`
	SSHKeys   []string          `json:"ssh_keys"`
	Hostname  string            `json:"hostname"`
	Domain    string            `json:"domain"`
	Network   []staticInterface `json:"network"`
	DNS       []string          `json:"dns"`
	DNSSearch []string          `json:"dns_search"`
	UserData  string            `json:"user_data"`
}

func (c *Client) putCloudInit(ctx context.Context, name string, body cloudInitUpdate) error {
	return c.API.Call(ctx, "PUT", "/vms/"+esc(name)+"/cloud-init", body, nil)
}

type bootSetting struct {
	Autostart bool `json:"autostart"`
}

func (c *Client) vmBoot(ctx context.Context, name, node string) (*bootSetting, error) {
	var b bootSetting
	if err := c.API.Call(ctx, "GET", withNode("/vms/"+esc(name)+"/boot", node), nil, &b); err != nil {
		return nil, notFound(err)
	}
	return &b, nil
}

func (c *Client) putBoot(ctx context.Context, name string, autostart bool) error {
	return c.API.Call(ctx, "PUT", "/vms/"+esc(name)+"/boot", map[string]any{"autostart": autostart}, nil)
}

type objectMeta struct {
	Notes string   `json:"notes"`
	Tags  []string `json:"tags"`
}

func (c *Client) vmMeta(ctx context.Context, name, node string) (*objectMeta, error) {
	var m objectMeta
	if err := c.API.Call(ctx, "GET", withNode("/meta/vm/"+esc(name), node), nil, &m); err != nil {
		return nil, notFound(err)
	}
	if m.Tags == nil {
		m.Tags = []string{}
	}
	sort.Strings(m.Tags)
	return &m, nil
}

func (c *Client) putMeta(ctx context.Context, name string, meta objectMeta) error {
	return c.API.Call(ctx, "PUT", "/meta/vm/"+esc(name), meta, nil)
}

// startedTask is the answer of a route called with ?wait=false (202).
type startedTask struct {
	TaskID string `json:"task_id"`
	Name   string `json:"name"`
}

// runTask calls a long route with ?wait=false and waits for its task: what every creation and copy goes through, so
// a large disk copy outlives no proxy timeout and a failure comes back with the task's own error.
func (c *Client) runTask(ctx context.Context, method, path string, body any) error {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	var started startedTask
	if err := c.API.Call(ctx, method, path+sep+"wait=false", body, &started); err != nil {
		return err
	}
	if started.TaskID == "" {
		return fmt.Errorf("%s %s answered without a task: this Hyperlite does not know ?wait=false (1.2 or later is needed)", method, path)
	}
	_, err := c.API.WaitTask(ctx, started.TaskID, hyperlite.WaitOptions{})
	return err
}

func (c *Client) startVM(ctx context.Context, name string) error {
	return c.API.Call(ctx, "POST", "/vms/"+esc(name)+"/start", nil, nil)
}

// stopVM shuts the guest down cleanly and waits until it is stopped, at most `grace`; then, when `force`, stops it
// hard (the rule of vSphere's shutdown_wait_timeout and force_power_off).
func (c *Client) stopVM(ctx context.Context, name string, grace time.Duration, force bool) error {
	vm, err := c.vmByName(ctx, name, "")
	if err != nil {
		return err
	}
	if vm.State == "stopped" {
		return nil
	}
	if err := c.API.Call(ctx, "POST", "/vms/"+esc(name)+"/stop", nil, nil); err != nil && !isConflict(err) {
		return err
	}
	if c.waitState(ctx, name, "stopped", grace) == nil {
		return nil
	}
	if !force {
		return fmt.Errorf("the VM %q did not shut down within %s (force_stop is false)", name, grace)
	}
	if err := c.API.Call(ctx, "POST", "/vms/"+esc(name)+"/stop?force=true", nil, nil); err != nil && !isConflict(err) {
		return err
	}
	return c.waitState(ctx, name, "stopped", time.Minute)
}

func (c *Client) waitState(ctx context.Context, name, state string, within time.Duration) error {
	deadline := time.Now().Add(within)
	for {
		vm, err := c.vmByName(ctx, name, "")
		if err != nil {
			return err
		}
		if vm.State == state {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the VM %q is %s, not %s, after %s", name, vm.State, state, within)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// waitIP waits for the VM's address (its DHCP lease or its guest agent), at most `within`.
func (c *Client) waitIP(ctx context.Context, name string, within time.Duration) (string, error) {
	deadline := time.Now().Add(within)
	for {
		vm, err := c.vmByName(ctx, name, "")
		if err != nil {
			return "", err
		}
		if vm.IP != "" {
			return vm.IP, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("the VM %q has no IP address after %s: it gets one from a DHCP lease of a Hyperlite network or from its guest agent (set wait_for_ip = \"0s\" not to wait)", name, within)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

func isConflict(err error) bool {
	var e *hyperlite.APIError
	return errors.As(err, &e) && e.Status == http.StatusConflict
}

// ---- Pools ----

func (c *Client) poolsOf(ctx context.Context, vm string) ([]int64, error) {
	var page struct {
		Items []struct {
			ID  int64    `json:"id"`
			VMs []string `json:"vms"`
		} `json:"items"`
	}
	if err := c.API.Call(ctx, "GET", "/pools", nil, &page); err != nil {
		return nil, err
	}
	var out []int64
	for _, p := range page.Items {
		for _, v := range p.VMs {
			if v == vm {
				out = append(out, p.ID)
			}
		}
	}
	return out, nil
}
