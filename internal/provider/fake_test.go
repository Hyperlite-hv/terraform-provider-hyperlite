package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeVM is a VM as the fake Hyperlite holds it.
type fakeVM struct {
	Name, UUID, State, Firmware string
	VCPU                        int64
	MemoryMB                    int64
	Disks                       []fakeDisk
	Ifaces                      []vmInterface
	CloudInit                   map[string]any // nil: no cloud-init drive
	Autostart                   bool
	Notes                       string
	Tags                        []string
	Schedule                    map[string]any // nil: no backup schedule
}

type fakeDisk struct {
	Target, Pool, Volume string
	SizeGB               float64
}

// fakeHyperliteAPI keeps VMs, volumes and pools, and answers the routes the VM resource calls with the rules of the
// real API that matter to it: a running VM refuses resources, a rename and a deletion (409); long operations answer
// a task; interfaces keep their MAC address.
type fakeHyperliteAPI struct {
	t       *testing.T
	mu      sync.Mutex
	vms     map[string]*fakeVM
	pools   map[int64][]string
	volumes map[string]float64 // pool/volume -> size
	calls   []string
	seq     int
	tasks   map[string]bool
	jobs    map[int64]map[string]any  // grouped backup jobs
	tpls    map[string]map[string]any // templates, by name
	isos    map[string][]byte         // the ISO library: name -> content
}

// fakeBackupDir is the node's default backup directory, which the real server reports when none is set.
const fakeBackupDir = "/var/lib/hyperlite/backups"

// backupDir stores a directory as the server does: the default one is kept empty and read back in full.
func backupDir(body map[string]any) map[string]any {
	out := merge(body, nil)
	if dir, _ := body["target_dir"].(string); dir == "" {
		out["target_dir"] = fakeBackupDir
	}
	return out
}

func newFakeHyperlite(t *testing.T) (*fakeHyperliteAPI, *httptest.Server) {
	f := &fakeHyperliteAPI{t: t, vms: map[string]*fakeVM{}, pools: map[int64][]string{7: {}}, volumes: map[string]float64{}, tasks: map[string]bool{}, jobs: map[int64]map[string]any{}, tpls: map[string]map[string]any{}, isos: map[string][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeHyperliteAPI) mac() string {
	f.seq++
	return fmt.Sprintf("52:54:00:00:00:%02x", f.seq)
}

func (f *fakeHyperliteAPI) task() map[string]any {
	f.seq++
	id := fmt.Sprintf("t%d", f.seq)
	f.tasks[id] = true
	return map[string]any{"task_id": id, "status": "running"}
}

func (f *fakeHyperliteAPI) newVM(name string, vcpu, mem int64, disks []float64, network, pool string, cloud bool) *fakeVM {
	f.seq++
	vm := &fakeVM{Name: name, UUID: fmt.Sprintf("00000000-0000-4000-8000-%012d", f.seq), State: "stopped", Firmware: "bios", VCPU: vcpu, MemoryMB: mem, Tags: []string{}}
	for i, s := range disks {
		vm.Disks = append(vm.Disks, fakeDisk{Target: "sd" + string(rune('a'+i)), Pool: pool, Volume: fmt.Sprintf("%s-%d.qcow2", name, i), SizeGB: s})
	}
	vm.Ifaces = []vmInterface{{MAC: f.mac(), Network: network}}
	if cloud {
		vm.CloudInit = map[string]any{"user": "", "ssh_keys": []string{}, "hostname": nil, "domain": nil, "network": []any{}, "dns": []string{}, "dns_search": []string{}, "user_data": nil}
	}
	f.vms[name] = vm
	return vm
}

func (f *fakeHyperliteAPI) called(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeHyperliteAPI) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/api/v1")
	f.calls = append(f.calls, r.Method+" "+p+"?"+r.URL.RawQuery)
	var body map[string]any
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	seg := strings.Split(strings.Trim(p, "/"), "/")
	fail := func(status int, detail string) {
		reply(w, status, map[string]any{"detail": detail, "code": "conflict"})
	}
	get := func(name string) *fakeVM {
		vm := f.vms[name]
		if vm == nil {
			reply(w, 404, map[string]any{"detail": "VM not found", "code": "not_found"})
		}
		return vm
	}
	switch {
	case p == "/health":
		reply(w, 200, map[string]any{"status": "ok", "api": 1, "hyperlite_version": "1.2.0"})
	case p == "/auth/me":
		reply(w, 200, map[string]any{"username": "terraform"})
	case len(seg) == 2 && seg[0] == "tasks":
		reply(w, 200, map[string]any{"id": seg[1], "status": "succeeded"})
	case p == "/vms" && r.Method == "GET":
		items := []any{}
		for _, vm := range f.vms {
			items = append(items, f.summary(vm))
		}
		reply(w, 200, map[string]any{"items": items, "total": len(items)})
	case p == "/vms" && r.Method == "POST":
		if r.URL.Query().Get("wait") != "false" {
			fail(400, "the provider must not hold a creation")
			return
		}
		var sizes []float64
		for _, d := range body["disks"].([]any) {
			sizes = append(sizes, d.(map[string]any)["size_gb"].(float64))
		}
		pool, _ := body["storage_pool"].(string)
		if pool == "" {
			pool = "default"
		}
		vm := f.newVM(body["name"].(string), int64(body["vcpu"].(float64)), int64(body["memory_mb"].(float64)), sizes, body["network"].(string), pool, body["iso"] == nil)
		if vm.CloudInit != nil {
			vm.CloudInit["user"] = body["username"]
		}
		if fw, ok := body["firmware"].(string); ok {
			vm.Firmware = fw
		}
		if rp, ok := body["resource_pool"].(float64); ok {
			f.pools[int64(rp)] = append(f.pools[int64(rp)], vm.Name)
		}
		reply(w, 202, merge(f.task(), map[string]any{"name": vm.Name}))
	case p == "/isos" && r.Method == "GET":
		items := []any{}
		for name, data := range f.isos {
			items = append(items, map[string]any{"name": name, "size_mb": float64(len(data)) / (1024 * 1024), "added_at": "2026-10-22T08:00:00+00:00", "node": "local"})
		}
		reply(w, 200, map[string]any{"items": items, "total": len(items)})
	case p == "/isos" && r.Method == "POST":
		file, header, err := r.FormFile("file")
		if err != nil {
			fail(422, "fake: no file part: "+err.Error())
			return
		}
		data, _ := io.ReadAll(file)
		if f.isos[header.Filename] != nil {
			fail(409, "An ISO named '"+header.Filename+"' already exists: rename or delete it first")
			return
		}
		f.isos[header.Filename] = data
		reply(w, 201, map[string]any{"name": header.Filename})
	case len(seg) == 3 && seg[0] == "isos" && seg[2] == "rename":
		f.isos[body["new_name"].(string)] = f.isos[seg[1]]
		delete(f.isos, seg[1])
		reply(w, 200, map[string]any{"name": body["new_name"], "old": seg[1]})
	case len(seg) == 2 && seg[0] == "isos" && r.Method == "DELETE":
		delete(f.isos, seg[1])
		reply(w, 200, map[string]any{"name": seg[1], "deleted": true})
	case p == "/templates" && r.Method == "GET":
		items := []any{}
		for _, t := range f.tpls {
			items = append(items, t)
		}
		reply(w, 200, map[string]any{"items": items, "total": len(items)})
	case len(seg) == 3 && seg[0] == "templates" && seg[1] == "from-vm":
		vm := get(seg[2])
		if vm == nil {
			return
		}
		if vm.State != "stopped" {
			fail(409, "Stop the VM before making a template of it")
			return
		}
		name := body["template_name"].(string)
		f.tpls[name] = map[string]any{"name": name, "vcpu": vm.VCPU, "memory_mb": vm.MemoryMB, "vm_source": vm.Name,
			"ssh_user": "debian", "os_label": "Debian 12", "created_at": "2026-10-22T08:00:00"}
		if keep, _ := body["keep_vm"].(bool); !keep {
			delete(f.vms, vm.Name)
		}
		reply(w, 202, merge(f.task(), map[string]any{"name": name}))
	case len(seg) == 3 && seg[0] == "templates" && seg[2] == "rename":
		t := f.tpls[seg[1]]
		if t == nil {
			reply(w, 404, map[string]any{"detail": "Template not found", "code": "not_found"})
			return
		}
		delete(f.tpls, seg[1])
		t["name"] = body["new_name"]
		f.tpls[body["new_name"].(string)] = t
		reply(w, 200, t)
	case len(seg) == 2 && seg[0] == "templates" && r.Method == "DELETE":
		if r.URL.Query().Get("confirm") != "true" {
			fail(400, "Confirmation required (?confirm=true)")
			return
		}
		delete(f.tpls, seg[1])
		reply(w, 200, map[string]any{"template": seg[1], "deleted": true})
	case len(seg) == 3 && seg[0] == "templates" && seg[2] == "deploy":
		vm := f.newVM(body["new_name"].(string), 2, 2048, []float64{10}, body["network"].(string), "default", true)
		vm.CloudInit["user"] = "debian"
		reply(w, 202, merge(f.task(), map[string]any{"name": vm.Name}))
	case len(seg) == 2 && seg[0] == "vms" && r.Method == "GET":
		if vm := get(seg[1]); vm != nil {
			reply(w, 200, f.summary(vm))
		}
	case len(seg) == 2 && seg[0] == "vms" && r.Method == "PATCH":
		if vm := get(seg[1]); vm != nil {
			if vm.State != "stopped" {
				fail(409, "Stop the VM before changing its resources")
				return
			}
			vm.VCPU, vm.MemoryMB = int64(body["vcpu"].(float64)), int64(body["memory_mb"].(float64))
			reply(w, 200, f.summary(vm))
		}
	case len(seg) == 2 && seg[0] == "vms" && r.Method == "DELETE":
		if vm := get(seg[1]); vm != nil {
			if vm.State != "stopped" {
				fail(409, "VM running")
				return
			}
			delete(f.vms, vm.Name)
			reply(w, 200, map[string]any{})
		}
	case len(seg) == 3 && seg[0] == "vms":
		vm := get(seg[1])
		if vm == nil {
			return
		}
		switch seg[2] + " " + r.Method {
		case "start POST":
			if vm.State != "stopped" {
				fail(409, "already running")
				return
			}
			vm.State = "active"
			reply(w, 200, f.summary(vm))
		case "stop POST":
			if vm.State == "stopped" {
				fail(409, "already stopped")
				return
			}
			vm.State = "stopped"
			reply(w, 200, f.summary(vm))
		case "rename POST":
			if vm.State != "stopped" {
				fail(409, "Stop the VM before renaming it")
				return
			}
			delete(f.vms, vm.Name)
			vm.Name = body["new_name"].(string)
			f.vms[vm.Name] = vm
			reply(w, 200, map[string]any{})
		case "disks GET":
			items := []any{}
			for _, d := range vm.Disks {
				pool, size := d.Pool, d.SizeGB
				items = append(items, map[string]any{"target": d.Target, "type": "disk", "source": "/images/" + d.Volume, "size_gb": size, "pool": pool})
			}
			items = append(items, map[string]any{"target": "sdz", "type": "cdrom", "source": nil, "size_gb": nil, "pool": nil})
			reply(w, 200, map[string]any{"items": items, "total": len(items)})
		case "disks POST":
			key := body["pool"].(string) + "/" + body["volume_name"].(string)
			size, ok := f.volumes[key]
			if !ok {
				fail(404, "no such volume")
				return
			}
			vm.Disks = append(vm.Disks, fakeDisk{Target: body["target_dev"].(string), Pool: body["pool"].(string), Volume: body["volume_name"].(string), SizeGB: size})
			delete(f.volumes, key)
			reply(w, 201, map[string]any{})
		case "network GET":
			reply(w, 200, map[string]any{"interfaces": vm.Ifaces, "ip": nil})
		case "network PUT":
			mac := r.URL.Query().Get("mac")
			for i := range vm.Ifaces {
				if mac == "" && i == 0 || strings.EqualFold(vm.Ifaces[i].MAC, mac) {
					vm.Ifaces[i].Network = body["network"].(string)
					vm.Ifaces[i].VLAN = nil
					if t, ok := body["vlan_tag"].(float64); ok {
						tag := int64(t)
						vm.Ifaces[i].VLAN = &tag
					}
					reply(w, 200, map[string]any{})
					return
				}
			}
			reply(w, 404, map[string]any{"detail": "no interface", "code": "not_found"})
		case "interfaces POST":
			iface := vmInterface{MAC: f.mac(), Network: body["network"].(string)}
			if t, ok := body["vlan_tag"].(float64); ok {
				tag := int64(t)
				iface.VLAN = &tag
			}
			vm.Ifaces = append(vm.Ifaces, iface)
			reply(w, 201, map[string]any{})
		case "cloud-init GET":
			if vm.CloudInit == nil {
				reply(w, 200, map[string]any{"available": false})
				return
			}
			reply(w, 200, merge(map[string]any{"available": true, "running": vm.State != "stopped"}, vm.CloudInit))
		case "cloud-init PUT":
			if vm.CloudInit == nil {
				fail(409, "no cloud-init drive")
				return
			}
			for _, k := range []string{"user", "ssh_keys", "hostname", "domain", "network", "dns", "dns_search", "user_data"} {
				v := body[k]
				if s, ok := v.(string); ok && s == "" && k != "user" {
					v = nil
				}
				vm.CloudInit[k] = v
			}
			reply(w, 200, map[string]any{})
		case "boot GET":
			reply(w, 200, map[string]any{"autostart": vm.Autostart})
		case "boot PUT":
			vm.Autostart = body["autostart"].(bool)
			reply(w, 200, map[string]any{})
		case "backup-schedule GET":
			reply(w, 200, vm.Schedule)
		case "backup-schedule PUT":
			vm.Schedule = backupDir(body)
			reply(w, 200, map[string]any{"message": "saved"})
		case "backup-schedule DELETE":
			vm.Schedule = nil
			reply(w, 200, map[string]any{})
		default:
			fail(404, "fake: no route "+r.Method+" "+p)
		}
	case len(seg) == 5 && seg[0] == "vms" && seg[2] == "disks" && seg[4] == "resize":
		vm := get(seg[1])
		for i := range vm.Disks {
			if vm.Disks[i].Target == seg[3] {
				vm.Disks[i].SizeGB = body["size_gb"].(float64)
			}
		}
		reply(w, 200, map[string]any{})
	case len(seg) == 4 && seg[0] == "vms" && seg[2] == "disks" && r.Method == "DELETE":
		vm := get(seg[1])
		var kept []fakeDisk
		for _, d := range vm.Disks {
			if d.Target != seg[3] {
				kept = append(kept, d)
			} else {
				f.volumes[d.Pool+"/"+d.Volume] = d.SizeGB
			}
		}
		vm.Disks = kept
		reply(w, 200, map[string]any{})
	case len(seg) == 4 && seg[0] == "vms" && seg[2] == "interfaces" && r.Method == "DELETE":
		vm := get(seg[1])
		var kept []vmInterface
		for _, i := range vm.Ifaces {
			if !strings.EqualFold(i.MAC, seg[3]) {
				kept = append(kept, i)
			}
		}
		vm.Ifaces = kept
		reply(w, 200, map[string]any{})
	case len(seg) == 3 && seg[0] == "storage" && seg[2] == "volumes" && r.Method == "POST":
		name := body["name"].(string) + ".qcow2"
		f.volumes[seg[1]+"/"+name] = body["size_gb"].(float64)
		reply(w, 201, map[string]any{"name": name})
	case len(seg) == 4 && seg[0] == "storage" && seg[2] == "volumes" && r.Method == "DELETE":
		delete(f.volumes, seg[1]+"/"+seg[3])
		reply(w, 200, map[string]any{})
	case len(seg) == 3 && seg[0] == "meta" && seg[1] == "vm":
		vm := get(seg[2])
		if vm == nil {
			return
		}
		if r.Method == "PUT" {
			vm.Notes = body["notes"].(string)
			vm.Tags = []string{}
			for _, t := range body["tags"].([]any) {
				vm.Tags = append(vm.Tags, t.(string))
			}
		}
		reply(w, 200, map[string]any{"notes": vm.Notes, "tags": vm.Tags})
	case p == "/pools":
		items := []any{}
		ids := []int64{}
		for id := range f.pools {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
		for _, id := range ids {
			items = append(items, map[string]any{"id": id, "name": "p" + strconv.FormatInt(id, 10), "vms": f.pools[id]})
		}
		reply(w, 200, map[string]any{"items": items, "total": len(items)})
	case p == "/backup-groups" && r.Method == "POST":
		f.seq++
		job := merge(backupDir(body), map[string]any{"id": int64(f.seq)})
		f.jobs[int64(f.seq)] = job
		reply(w, 201, job)
	case len(seg) == 2 && seg[0] == "backup-groups":
		id, _ := strconv.ParseInt(seg[1], 10, 64)
		if f.jobs[id] == nil {
			reply(w, 404, map[string]any{"detail": "Backup job not found", "code": "not_found"})
			return
		}
		switch r.Method {
		case "GET":
			reply(w, 200, f.jobs[id])
		case "PUT":
			f.jobs[id] = merge(backupDir(body), map[string]any{"id": id})
			reply(w, 200, f.jobs[id])
		case "DELETE":
			delete(f.jobs, id)
			reply(w, 200, map[string]any{})
		}
	case len(seg) == 3 && seg[0] == "pools" && seg[2] == "members":
		id, _ := strconv.ParseInt(seg[1], 10, 64)
		f.pools[id] = append(f.pools[id], body["vm_name"].(string))
		reply(w, 201, map[string]any{})
	default:
		fail(404, "fake: no route "+r.Method+" "+p)
	}
}

func (f *fakeHyperliteAPI) summary(vm *fakeVM) map[string]any {
	ip := any(nil)
	if vm.State == "active" {
		ip = "192.168.199.10"
	}
	return map[string]any{"name": vm.Name, "uuid": vm.UUID, "state": vm.State, "vcpu": vm.VCPU, "memory_mb": vm.MemoryMB, "ip": ip, "firmware": vm.Firmware, "node": "local"}
}

func merge(a, b map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
