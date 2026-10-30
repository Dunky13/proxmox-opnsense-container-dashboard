package dashboard

import (
	"context"
	"fmt"
	"time"
)

type DemoProviders struct{ Store *Store }

func NewDemo(store *Store) (*DemoProviders, error) {
	if !store.Read().Demo {
		return nil, bad("demo provider requires isolated demo state")
	}
	p := &DemoProviders{store}
	st := store.Read()
	if len(st.Config.Hosts) == 0 && len(st.Config.OPNsenses) == 0 {
		hosts := DefaultProfiles()
		for n := range hosts {
			hosts[n].BaseURL = "https://demo-" + hosts[n].ID + ".invalid"
			hosts[n].Node = hosts[n].ID
			hosts[n].OPNsenseID = "demo-edge"
			// DemoProviders never invokes SSH. These fixture paths describe the
			// simulated execution capability without loading any real key.
			hosts[n].SSH = SSHConfig{Enabled: true, Host: "mock.invalid", Port: 22, User: "root", PrivateKeyFile: "/demo/mock-key", KnownHostsFile: "/demo/mock-known-hosts"}
		}
		hosts[0].Name = "main proxmox"
		hosts[1].SubnetID = "22222222-2222-4222-8222-222222222222"
		o := OPNsense{ID: "demo-edge", Name: "Demo OPNsense", BaseURL: "https://demo-opn.invalid", SubnetID: "11111111-1111-4111-8111-111111111111", DomainBase: "went.io", AllowlistCIDRs: []string{"192.168.6.0/24", "192.168.7.0/24", "10.8.0.0/24", "fd00:1234::/64"}, UpstreamScheme: "", UpstreamPort: 0, CertificateID: "", DomainReady: false}
		if e := store.Update(func(st *State) error {
			st.Config = Config{Revision: 1, Hosts: hosts, OPNsenses: []OPNsense{o}}
			st.DemoResources = map[string][]Resource{o.ID: {
				{ID: "general", Kind: "caddyGeneral", Fields: map[string]string{"enabled": "1", "accesslist": "aaaaaaaa-4444-4444-8444-444444444444", "ClientIpHeaders": "aaaaaaaa-5555-4555-8555-555555555555", "EnableLayer4": "0", "ProxyProtocolAllow": ""}},
				{ID: "aaaaaaaa-3333-4333-8333-333333333333", Kind: "acl", Fields: map[string]string{"accesslistName": "Shared client LAN and VPN", "clientIps": "10.8.0.0/24,192.168.6.0/24,192.168.7.0/24,fd00:1234::/64", "accesslistInvert": "0", "RequestMatcher": "client_ip", "HttpResponseCode": "403", "description": "External client IP policy with configured proxy trust"}},
				{ID: "aaaaaaaa-4444-4444-8444-444444444444", Kind: "acl", Fields: map[string]string{"accesslistName": "Trusted proxy fixture", "clientIps": "192.0.2.0/24,2001:db8::/32", "accesslistInvert": "0", "RequestMatcher": "remote_ip"}},
				{ID: "aaaaaaaa-5555-4555-8555-555555555555", Kind: "caddyClientIPHeader", Fields: map[string]string{"HeaderType": "X-Forwarded-For"}},
				{ID: "11111111-1111-4111-8111-111111111111", Kind: "subnet", Fields: map[string]string{"subnet": "192.168.6.0/24", "subnet_id": "6", "description": "Container VLAN 6"}},
				{ID: "22222222-2222-4222-8222-222222222222", Kind: "subnet", Fields: map[string]string{"subnet": "192.168.7.0/24", "subnet_id": "7", "description": "Container VLAN 7"}},
				{ID: "aaaaaaaa-1111-4111-8111-111111111111", Kind: "acl", Fields: map[string]string{"accesslistName": "Shared LAN and VPN", "clientIps": "10.8.0.0/24,192.168.6.0/24,192.168.7.0/24,fd00:1234::/64", "accesslistInvert": "0", "RequestMatcher": "remote_ip", "HttpResponseCode": "403", "description": "Operator-managed LAN and VPN policy; also used by manual sites"}},
				{ID: "aaaaaaaa-2222-4222-8222-222222222222", Kind: "acl", Fields: map[string]string{"accesslistName": "Forwarded client policy", "clientIps": "192.168.6.0/24", "accesslistInvert": "1", "RequestMatcher": "client_ip", "HttpResponseCode": "403", "description": "Inverted policy is unsuitable for private routes"}},
			}}
			return nil
		}); e != nil {
			return nil, e
		}
	}
	return p, nil
}
func (p *DemoProviders) Inventory(ctx context.Context, cfg Config) Inventory {
	inv := Inventory{ObservedAt: time.Now().UTC(), Complete: true, Containers: []Container{}, Hosts: []HostInventory{}}
	for _, h := range cfg.Hosts {
		offsets := []int{2, 3, 5, 6, 7}
		names := []string{"paperless", "immich", "vaultwarden", "forgejo", "uptime-kuma"}
		guests := []Guest{{h.VMIDMin, "qemu", h.Node}, {h.VMIDMin + 1, "qemu", h.Node}}
		if h.VMIDMin/100 == 7 {
			offsets = []int{0, 1, 3}
			names = []string{"jellyfin", "homebridge", "grafana"}
			guests = []Guest{}
		}
		for n, offset := range offsets {
			id := h.VMIDMin + offset
			mac, ip, e := DeriveAddress(h, id)
			if e != nil {
				inv.Complete = false
				continue
			}
			guests = append(guests, Guest{id, "lxc", h.Node})
			status := "running"
			if n == 2 {
				status = "stopped"
			}
			dist, version := "debian", "13"
			if n == 1 {
				version = "12"
			}
			if n == 2 {
				dist, version = "alpine", "3.22"
			}
			app, icon := inferApp(names[n])
			c := Container{Key: key(h.ID, id), HostID: h.ID, Node: h.Node, VMID: id, Name: names[n], Status: status, IP: ip, MAC: mac, Distribution: dist, Version: version, DistroSource: "demo", App: app, Icon: icon, Identity: fingerprint([]string{"demo-generation", h.ID, itoa(id)}), Warnings: []string{"DEMO fixture; no infrastructure was contacted"}}
			c.Fingerprint = fingerprint(c)
			inv.Containers = append(inv.Containers, c)
		}
		next, _ := NextVMID(h, guests, true)
		inv.Hosts = append(inv.Hosts, HostInventory{h.ID, true, "", next, guests})
	}
	sortInventory(&inv)
	return inv
}
func (p *DemoProviders) Snapshot(ctx context.Context, o OPNsense) (ProviderSnapshot, error) {
	st := p.Store.Read()
	out := ProviderSnapshot{Resources: []Resource{}, Subnet: "192.168.6.0/24", Ready: true, Issues: []string{}}
	for _, h := range st.Config.Hosts {
		if h.OPNsenseID == o.ID && (h.SubnetID == o.SubnetID || (h.SubnetID == "" && func() bool { base, _ := st.Config.Router(o.ID); return base.SubnetID == o.SubnetID }())) {
			out.Subnet = h.Network
			break
		}
	}
	if st.DemoResources != nil {
		out.Resources = append(out.Resources, st.DemoResources[o.ID]...)
	}
	policy, err := p.CaddyPolicy(ctx, o)
	if err != nil {
		return out, err
	}
	out.ClientIPContext = policy.ClientIPContext
	return out, nil
}
func (p *DemoProviders) Create(ctx context.Context, o OPNsense, kind string, fields map[string]string) (Resource, error) {
	if kind == "subnet" {
		return Resource{}, bad("subnet writes are not supported")
	}
	if _, e := resourceEndpoint(kind); e != nil {
		return Resource{}, e
	}
	seed := fingerprint(newID())
	id := fmt.Sprintf("%s-%s-4%s-8%s-%s", seed[:8], seed[8:12], seed[13:16], seed[17:20], seed[20:32])
	r := Resource{id, kind, clone(fields)}
	e := p.Store.Update(func(st *State) error {
		if st.DemoResources == nil {
			st.DemoResources = map[string][]Resource{}
		}
		st.DemoResources[o.ID] = append(st.DemoResources[o.ID], r)
		return nil
	})
	return r, e
}
func (p *DemoProviders) Set(ctx context.Context, o OPNsense, old Resource, fields map[string]string) (Resource, error) {
	if old.Kind == "subnet" {
		return Resource{}, bad("subnet writes are not supported")
	}
	var result Resource
	e := p.Store.Update(func(st *State) error {
		for i, r := range st.DemoResources[o.ID] {
			if r.ID == old.ID && r.Kind == old.Kind {
				if r.Fingerprint() != old.Fingerprint() {
					return conflict("demo resource changed")
				}
				for k, v := range fields {
					r.Fields[k] = v
				}
				st.DemoResources[o.ID][i] = r
				result = r
				return nil
			}
		}
		return conflict("demo resource is missing")
	})
	return result, e
}
func (p *DemoProviders) Delete(ctx context.Context, o OPNsense, old Resource) error {
	if old.Kind == "subnet" {
		return bad("subnet writes are not supported")
	}
	return p.Store.Update(func(st *State) error {
		rows := st.DemoResources[o.ID]
		for i, r := range rows {
			if r.ID == old.ID && r.Kind == old.Kind {
				if r.Fingerprint() != old.Fingerprint() {
					return conflict("demo resource changed")
				}
				st.DemoResources[o.ID] = append(rows[:i], rows[i+1:]...)
				return nil
			}
		}
		return conflict("demo resource is missing")
	})
}
func (*DemoProviders) Apply(context.Context, OPNsense, string) error { return nil }
func (*DemoProviders) Run(ctx context.Context, h Host, id int, kind string) (RunResult, error) {
	select {
	case <-ctx.Done():
		return RunResult{}, ctx.Err()
	case <-time.After(20 * time.Millisecond):
	}
	if kind == "inspect" || kind == "capabilities" {
		return RunResult{Distribution: "debian", Version: "13", AppUpdate: true}, nil
	}
	return RunResult{}, nil
}
