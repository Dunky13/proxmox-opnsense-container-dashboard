package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func pveGet(ctx context.Context, h Host, path string, out any) error {
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if h.TokenID == "" || h.TokenSecret == "" {
		return unavailable("Proxmox API token is not configured")
	}
	e := providerJSON(ctx, h.BaseURL, h.CAPEM, "GET", "/api2/json"+path, nil, func(r *http.Request) { r.Header.Set("Authorization", "PVEAPIToken="+h.TokenID+"="+h.TokenSecret) }, &env)
	if e != nil {
		return e
	}
	if len(env.Data) == 0 || string(env.Data) == "null" || json.Unmarshal(env.Data, out) != nil {
		return unavailable("Proxmox response is incomplete")
	}
	return nil
}
func (p *RealProviders) Inventory(ctx context.Context, cfg Config) Inventory {
	ctx, closeHTTP := withProviderHTTP(ctx)
	defer closeHTTP()
	inv := Inventory{ObservedAt: time.Now().UTC(), Complete: true, Containers: []Container{}, Hosts: []HostInventory{}}
	for _, h := range cfg.Hosts {
		cs, guests, e := p.hostInventory(ctx, h)
		hi := HostInventory{HostID: h.ID, Complete: e == nil, Guests: guests}
		if e != nil {
			inv.Complete = false
			hi.Error = e.Error()
		} else {
			hi.NextVMID, _ = NextVMID(h, guests, true)
			inv.Containers = append(inv.Containers, cs...)
		}
		inv.Hosts = append(inv.Hosts, hi)
	}
	sortInventory(&inv)
	return inv
}
func (p *RealProviders) hostInventory(ctx context.Context, h Host) ([]Container, []Guest, error) {
	// The allocation claim requires cluster-wide propagated audit permission.
	var perms map[string]map[string]json.RawMessage
	if e := pveGet(ctx, h, "/access/permissions?path=/", &perms); e != nil {
		return nil, nil, e
	}
	var audit int
	_ = json.Unmarshal(perms["/"]["VM.Audit"], &audit)
	if audit != 1 {
		return nil, nil, unavailable("cluster-wide VM.Audit at / is required for complete occupancy")
	}
	var nodes []struct {
		Node   string `json:"node"`
		Status string `json:"status"`
	}
	if e := pveGet(ctx, h, "/nodes", &nodes); e != nil {
		return nil, nil, e
	}
	nodeOnline := false
	for _, n := range nodes {
		if n.Node == h.Node && n.Status == "online" {
			nodeOnline = true
		}
	}
	if !nodeOnline {
		return nil, nil, unavailable("configured Proxmox node is missing or offline")
	}
	var cluster []struct {
		Type    string `json:"type"`
		Quorate int    `json:"quorate"`
		Online  int    `json:"online"`
	}
	if e := pveGet(ctx, h, "/cluster/status", &cluster); e != nil {
		return nil, nil, e
	}
	if cluster == nil {
		return nil, nil, unavailable("cluster status is incomplete")
	}
	for _, entry := range cluster {
		if entry.Type == "cluster" && entry.Quorate != 1 {
			return nil, nil, unavailable("cluster is not quorate; occupancy cannot be trusted")
		}
	}

	var rows []struct {
		VMID     int    `json:"vmid"`
		Type     string `json:"type"`
		Node     string `json:"node"`
		Name     string `json:"name"`
		Status   string `json:"status"`
		Template int    `json:"template"`
	}
	if e := pveGet(ctx, h, "/cluster/resources?type=vm", &rows); e != nil {
		return nil, nil, e
	}
	if rows == nil {
		return nil, nil, unavailable("missing cluster inventory")
	}
	seen := map[int]bool{}
	generationSeen := map[string]bool{}
	guests := []Guest{}
	containers := []Container{}
	local := []int{}
	for index, r := range rows {
		if r.VMID < 100 || !slug.MatchString(r.Node) || (r.Type != "lxc" && r.Type != "qemu") || seen[r.VMID] {
			return nil, nil, unavailable("invalid or ambiguous cluster inventory")
		}
		seen[r.VMID] = true
		guests = append(guests, Guest{r.VMID, r.Type, r.Node})
		if r.Type != "lxc" || r.Node != h.Node {
			continue
		}
		if r.Status != "running" && r.Status != "stopped" {
			return nil, nil, unavailable("container status is unknown")
		}
		local = append(local, index)
	}
	type networkInterface struct {
		Name   string `json:"name"`
		HWAddr string `json:"hwaddr"`
		Inet   string `json:"inet"`
	}
	type observation struct {
		config       map[string]json.RawMessage
		interfaces   []networkInterface
		networkError bool
	}
	observations, err := readProviderRows(ctx, len(local), func(ctx context.Context, index int) (observation, error) {
		r := rows[local[index]]
		var observed observation
		path := "/nodes/" + url.PathEscape(r.Node) + "/lxc/" + strconv.Itoa(r.VMID)
		if e := pveGet(ctx, h, path+"/config", &observed.config); e != nil {
			return observed, e
		}
		if len(observed.config) == 0 {
			return observed, unavailable("missing container configuration")
		}
		if r.Status == "running" {
			observed.networkError = pveGet(ctx, h, path+"/interfaces", &observed.interfaces) != nil
		}
		return observed, nil
	})
	if err != nil {
		return nil, nil, err
	}
	for index, observed := range observations {
		r := rows[local[index]]
		config := observed.config
		text := func(k string) string { var v string; _ = json.Unmarshal(config[k], &v); return v }
		name := text("hostname")
		if name == "" {
			name = r.Name
		}
		net0 := properties(text("net0"))
		mac := strings.ToLower(net0["hwaddr"])
		ip := ""
		if a, e := netip.ParsePrefix(net0["ip"]); e == nil {
			ip = a.Addr().String()
		} else if a, e := netip.ParseAddr(net0["ip"]); e == nil {
			ip = a.String()
		}
		warnings := []string{}
		if r.Status == "running" {
			if observed.networkError {
				warnings = append(warnings, "runtime network unavailable")
			} else {
				for _, in := range observed.interfaces {
					if strings.EqualFold(in.HWAddr, mac) && mac != "" {
						if a, e := netip.ParsePrefix(in.Inet); e == nil {
							ip = a.Addr().String()
						}
					}
				}
			}
		}
		app, icon := inferApp(name)
		ct := Container{Key: key(h.ID, r.VMID), HostID: h.ID, Node: r.Node, VMID: r.VMID, Name: name, Status: r.Status, Template: r.Template == 1, IP: ip, MAC: mac, Distribution: text("ostype"), DistroSource: "proxmox", App: app, Icon: icon, Warnings: warnings}
		meta := properties(text("meta"))
		volume := rootfsVolume(text("rootfs"))
		generation := ""
		if match := generationMarker.FindStringSubmatch(text("description")); len(match) == 2 {
			generation = strings.ToLower(match[1])
			if generationSeen[generation] {
				return nil, nil, unavailable("duplicate container generation marker; identity is ambiguous")
			}
			generationSeen[generation] = true
		}
		if generation != "" && volume != "" && mac != "" {
			ct.Identity = fingerprint([]string{h.ID, r.Node, strconv.Itoa(r.VMID), generation, volume, mac})
		}
		if t, e := strconv.ParseInt(meta["ctime"], 10, 64); ct.Identity == "" && e == nil && t > 0 && volume != "" && mac != "" {
			ct.Identity = fingerprint([]string{h.ID, r.Node, strconv.Itoa(r.VMID), meta["ctime"], volume, mac})
		} else if ct.Identity == "" {
			ct.Warnings = append(ct.Warnings, "creation identity unavailable; add a unique container-dashboard-generation UUID marker before mutations")
		}
		ct.Fingerprint = fingerprint(struct {
			Identity, Config, Name, Status, IP string
			Template                           bool
		}{ct.Identity, text("digest"), name, r.Status, ip, ct.Template})
		if text("digest") == "" {
			ct.Identity = ""
			ct.Fingerprint = ""
			ct.Warnings = append(ct.Warnings, "configuration digest unavailable")
		}

		containers = append(containers, ct)
	}
	return containers, guests, nil
}
func properties(s string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(part, "=")
		if ok {
			out[k] = v
		}
	}
	return out
}

func rootfsVolume(rootfs string) string {
	if volume := properties(rootfs)["volume"]; volume != "" {
		return volume
	}
	volume, _, _ := strings.Cut(rootfs, ",")
	if strings.Contains(volume, "=") {
		return ""
	}
	return strings.TrimSpace(volume)
}
func (p *RealProviders) Run(ctx context.Context, h Host, id int, kind string) (RunResult, error) {
	if p.SSH == nil {
		return RunResult{}, unavailable("SSH runner unavailable")
	}
	return p.SSH.Run(ctx, h, id, kind)
}
func inventoryError(i Inventory) error {
	if !i.Complete {
		return unavailable("fresh complete inventory is required; inspect host errors")
	}
	return nil
}
func ensureContainer(i Inventory, h Host, id int) (Container, error) {
	if e := inventoryError(i); e != nil {
		return Container{}, e
	}
	c, ok := i.Find(key(h.ID, id))
	if !ok {
		return c, conflict("container is missing or has moved")
	}
	if c.Identity == "" || c.Fingerprint == "" {
		return c, conflict("container creation identity is unavailable")
	}
	if c.Template {
		return c, conflict("template actions are not supported")
	}
	return c, nil
}
func profileCheck(c Container, h Host) (string, string, error) {
	mac, ip, e := DeriveAddress(h, c.VMID)
	if e != nil {
		return "", "", e
	}
	if !strings.EqualFold(mac, c.MAC) || (c.IP != "" && c.IP != ip) {
		return "", "", conflict(fmt.Sprintf("container %d MAC/IP does not match host profile", c.VMID))
	}
	if !hostname.MatchString(c.Name) {
		return "", "", conflict("container hostname is not a DNS label")
	}
	return mac, ip, nil
}

func targetConflicts(inv Inventory, c Container, desired map[string]string) error {
	for _, other := range inv.Containers {
		if other.Key == c.Key {
			continue
		}
		if (other.MAC != "" && strings.EqualFold(other.MAC, desired["mac"])) || (other.IP != "" && other.IP == desired["ip"]) {
			return conflict("another observed container uses the target IP or MAC")
		}
	}
	return nil
}
