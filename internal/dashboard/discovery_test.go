package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const sharedDemoACL = "aaaaaaaa-1111-4111-8111-111111111111"
const vlan6Subnet = "11111111-1111-4111-8111-111111111111"
const vlan7Subnet = "22222222-2222-4222-8222-222222222222"

func selectShared(t *testing.T, store *Store, id string) {
	t.Helper()
	if err := store.Update(func(st *State) error { st.Config.OPNsenses[0].PrivateAccessListID = id; return nil }); err != nil {
		t.Fatal(err)
	}
}
func changeFixture(t *testing.T, store *Store, id, field, value string) {
	t.Helper()
	if err := store.Update(func(st *State) error {
		for i, r := range st.DemoResources["demo-edge"] {
			if r.ID == id {
				if field == "delete" {
					st.DemoResources["demo-edge"] = append(st.DemoResources["demo-edge"][:i], st.DemoResources["demo-edge"][i+1:]...)
				} else {
					st.DemoResources["demo-edge"][i].Fields[field] = value
				}
				return nil
			}
		}
		t.Fatal("fixture identity not found", id)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoveryUsesSavedAuthAndOnlyItsController(t *testing.T) {
	for _, kind := range []string{"acl", "subnet"} {
		t.Run(kind, func(t *testing.T) {
			mode := "valid"
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Fatal("discovery wrote", r.Method)
				}
				user, pass, ok := r.BasicAuth()
				if !ok || user != "saved-key" || pass != "saved-secret" {
					t.Error("did not use saved credentials")
				}
				ep, _ := resourceEndpoint(kind)
				switch r.URL.Path {
				case "/api/" + ep.Controller + "/search_" + ep.Command:
					if r.URL.Query().Get("current") != "1" || r.URL.Query().Get("rowCount") != "-1" {
						t.Error("unbounded/partial discovery")
					}
					switch mode {
					case "failed":
						w.WriteHeader(403)
						_, _ = w.Write([]byte("saved-secret private-body"))
					case "empty":
						writeJSON(w, 200, map[string]any{"total": 0, "rows": []any{}})
					case "incomplete":
						writeJSON(w, 200, map[string]any{"total": 2, "rows": []any{map[string]string{"uuid": sharedDemoACL}}})
					case "malformed":
						writeJSON(w, 200, map[string]any{"total": 0})
					default:
						writeJSON(w, 200, map[string]any{"total": 1, "rows": []any{map[string]string{"uuid": sharedDemoACL, "accesslistName": "translated search value"}}})
					}
				case "/api/" + ep.Controller + "/get_" + ep.Command + "/" + sharedDemoACL:
					fields := map[string]any{"accesslistName": "LAN and VPN", "description": "Shared manually", "clientIps": map[string]any{"fd00:1234::/64": map[string]any{"value": "IPv6 label", "selected": 1}, "10.8.0.0/24": map[string]any{"value": "VPN label", "selected": true}}, "RequestMatcher": map[string]any{"client_ip": map[string]any{"value": "translated", "selected": 0}, "remote_ip": map[string]any{"value": "translated remote", "selected": 1}}, "accesslistInvert": "0"}
					if kind == "subnet" {
						fields = map[string]any{"subnet": "192.168.6.0/23", "subnet_id": "6", "description": "VLAN parent", "option_data": map[string]any{"routers": "192.168.6.1"}}
					}
					writeJSON(w, 200, map[string]any{ep.Wrapper: fields})
				default:
					t.Errorf("unrelated discovery prerequisite %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			s, _, store := demoService(t)
			err := store.Update(func(st *State) error {
				o := &st.Config.OPNsenses[0]
				o.BaseURL = srv.URL
				o.CAPEM = trustServer(srv)
				o.APIKey = "saved-key"
				o.APISecret = "saved-secret"
				o.SubnetID = ""
				o.CertificateID = ""
				o.DomainReady = false
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			s.Providers = &RealProviders{}
			auth, err := NewAuth(context.Background(), AuthConfig{AdminToken: "test-bearer"})
			if err != nil {
				t.Fatal(err)
			}
			api := NewAPI(s, auth, nil)
			endpoint := "access-lists"
			if kind == "subnet" {
				endpoint = "subnets"
			}
			for _, modeValue := range []string{"valid", "empty", "failed", "incomplete", "malformed"} {
				mode = modeValue
				r := httptest.NewRequest("GET", "/api/v1/opnsenses/demo-edge/"+endpoint, nil)
				r.Header.Set("Authorization", "Bearer test-bearer")
				w := httptest.NewRecorder()
				api.ServeHTTP(w, r)
				valid := mode == "valid" || mode == "empty"
				if (w.Code == 200) != valid {
					t.Fatalf("%s status %d %s", mode, w.Code, w.Body.String())
				}
				if strings.Contains(w.Body.String(), "saved-secret") || strings.Contains(w.Body.String(), "saved-key") || strings.Contains(w.Body.String(), "private-body") {
					t.Fatal("secret leak")
				}
				if valid && kind == "acl" {
					var out AccessListDiscovery
					if err = json.Unmarshal(w.Body.Bytes(), &out); err != nil {
						t.Fatal(err)
					}
					if out.AccessLists == nil || out.Revision != store.Read().Config.Revision {
						t.Fatal(out)
					}
					if mode == "valid" && (len(out.AccessLists) != 1 || !out.AccessLists[0].Suitable || out.AccessLists[0].Name != "LAN and VPN" || strings.Join(out.AccessLists[0].CIDRs, ",") != "10.8.0.0/24,fd00:1234::/64") {
						t.Fatal(out)
					}
					if mode == "empty" && len(out.AccessLists) != 0 {
						t.Fatal(out)
					}
				}
				if valid && kind == "subnet" {
					var out SubnetDiscovery
					if err = json.Unmarshal(w.Body.Bytes(), &out); err != nil {
						t.Fatal(err)
					}
					if out.Subnets == nil || (mode == "valid" && (len(out.Subnets) != 1 || out.Subnets[0].CIDR != "192.168.6.0/23")) {
						t.Fatal(out)
					}
				}
			}
			w := httptest.NewRecorder()
			api.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/opnsenses/demo-edge/"+endpoint, nil))
			if w.Code != 401 {
				t.Fatal("unauthenticated discovery", w.Code)
			}
		})
	}
}

func TestSettingsSharedSelectionPreservesSecretsAndRevision(t *testing.T) {
	s, _, store := demoService(t)
	if err := store.Update(func(st *State) error {
		st.Config.OPNsenses[0].APIKey = "private-key"
		st.Config.OPNsenses[0].APISecret = "private-secret"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st := store.Read()
	edit := ConfigEdit{Revision: st.Config.Revision}
	for _, h := range st.Config.Hosts {
		edit.Hosts = append(edit.Hosts, HostEdit{Host: h})
	}
	o := st.Config.OPNsenses[0]
	o.PrivateAccessListID = sharedDemoACL
	o.SubnetID = ""
	o.UpstreamScheme = ""
	o.UpstreamPort = 0
	o.CertificateID = ""
	o.DomainReady = false
	edit.OPNsenses = []OPNsenseEdit{{OPNsense: o}}
	view, err := s.UpdateConfig(edit)
	if err != nil {
		t.Fatal(err)
	}
	if view.Revision != edit.Revision+1 || view.OPNsenses[0].PrivateAccessListID != sharedDemoACL || !view.OPNsenses[0].HasAPIKey || !view.OPNsenses[0].HasAPISecret || view.OPNsenses[0].APISecret != "" {
		t.Fatal(view)
	}
	if store.Read().Config.OPNsenses[0].APISecret != "private-secret" {
		t.Fatal("secret lost")
	}
	if _, err = s.UpdateConfig(edit); err == nil {
		t.Fatal("accepted stale settings")
	}
	edit.Revision = view.Revision
	edit.OPNsenses[0].PrivateAccessListID = "bad"
	if _, err = s.UpdateConfig(edit); err == nil {
		t.Fatal("accepted non-UUID")
	}
}

func TestSharedListReuseTwoAppsAndCleanupProtectsExternalPolicy(t *testing.T) {
	s, p, store := demoService(t)
	selectShared(t, store, sharedDemoACL)
	o := store.Read().Config.OPNsenses[0]
	snap, _ := p.Snapshot(context.Background(), o)
	shared, _ := snap.Find(sharedDemoACL, "acl")
	// A manual site and both apps refer to the same external list.
	if _, err := p.DemoProviders.Create(context.Background(), o, "domain", map[string]string{"FromDomain": "manual.went.io", "accesslist": sharedDemoACL}); err != nil {
		t.Fatal(err)
	}
	for _, vmid := range []int{603, 604} {
		plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: vmid})
		if err != nil {
			t.Fatal(err)
		}
		if plan.SharedAccessList == nil || plan.SharedAccessList.ID != sharedDemoACL {
			t.Fatal("missing review binding", plan)
		}
		reviewed := false
		for _, effect := range plan.Effects {
			if effect.Kind == "acl" && effect.ResourceID == sharedDemoACL && effect.Action == "retain" && strings.Contains(effect.Name, "unchanged") {
				reviewed = true
			}
		}
		if !reviewed {
			t.Fatal("shared reference absent from review")
		}
		if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
			t.Fatal(err)
		}
		own := store.Read().Owned[key("main", vmid)]
		if len(own.Resources) != 3 {
			t.Fatal("shared ACL claimed", own)
		}
		for _, ref := range own.Resources {
			if ref.Kind == "acl" {
				t.Fatal("shared ACL in ownership")
			}
		}
		snap, _ = p.Snapshot(context.Background(), o)
		ref, _ := ownedByKind(own, "domain")
		domain, _ := snap.Find(ref.ID, "domain")
		if domain.Fields["accesslist"] != sharedDemoACL {
			t.Fatal(domain)
		}
	}
	for _, event := range p.events {
		if strings.Contains(event, ":acl") {
			t.Fatal("mutated shared ACL", event)
		}
	}
	p.inventory = withoutDemoCT
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 604})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
		t.Fatal(err)
	}
	snap, _ = p.Snapshot(context.Background(), o)
	after, exists := snap.Find(sharedDemoACL, "acl")
	if !exists || after.Fingerprint() != shared.Fingerprint() {
		t.Fatal("shared list changed during cleanup")
	}
	if _, exists = store.Read().Owned["main:603"]; !exists {
		t.Fatal("sibling ownership removed")
	}
}

func TestSharedPrivatePolicySafetyDoesNotBlockPublicOrCleanup(t *testing.T) {
	for _, tc := range []struct{ field, value string }{{"delete", ""}, {"clientIps", ""}, {"clientIps", "0.0.0.0/0"}, {"clientIps", "0.0.0.0/1,128.0.0.0/1"}, {"clientIps", "::/1,8000::/1"}, {"clientIps", "192.168.6.1/24"}, {"accesslistInvert", "1"}, {"RequestMatcher", "client_ip"}} {
		t.Run(tc.field+tc.value, func(t *testing.T) {
			s, _, store := demoService(t)
			selectShared(t, store, sharedDemoACL)
			changeFixture(t, store, sharedDemoACL, tc.field, tc.value)
			if _, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604}); err == nil {
				t.Fatal("unsafe private plan")
			}
			plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604, Public: true})
			if err != nil {
				t.Fatal("private policy blocked public", err)
			}
			if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
	s, p, store := demoService(t)
	selectShared(t, store, sharedDemoACL)
	ensureDemo(t, s)
	changeFixture(t, store, sharedDemoACL, "delete", "")
	p.inventory = withoutDemoCT
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 604})
	if err != nil {
		t.Fatal("missing shared list blocked owned cleanup", err)
	}
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
		t.Fatal(err)
	}
}

func TestSwitchGeneratedSharedAndPublicPreservesOwnedUUIDs(t *testing.T) {
	s, p, store := demoService(t)
	ensureDemo(t, s)
	original := store.Read().Owned["main:604"]
	acl, _ := ownedByKind(original, "acl")
	domain, _ := ownedByKind(original, "domain")
	handler, _ := ownedByKind(original, "handle")
	snap, _ := p.Snapshot(context.Background(), store.Read().Config.OPNsenses[0])
	originalACL, _ := snap.Find(acl.ID, "acl")
	for _, mode := range []struct {
		id     string
		public bool
	}{{sharedDemoACL, false}, {sharedDemoACL, true}, {"", false}} {
		selectShared(t, store, mode.id)
		plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604, Public: mode.public})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
			t.Fatal(err)
		}
		own := store.Read().Owned["main:604"]
		d, _ := ownedByKind(own, "domain")
		h, _ := ownedByKind(own, "handle")
		a, _ := ownedByKind(own, "acl")
		if d.ID != domain.ID || h.ID != handler.ID || a.ID != acl.ID {
			t.Fatal("replaced surviving resources", own)
		}
		snap, _ = p.Snapshot(context.Background(), store.Read().Config.OPNsenses[0])
		after, _ := snap.Find(acl.ID, "acl")
		if after.Fingerprint() != originalACL.Fingerprint() {
			t.Fatal("detached owned ACL modified")
		}
	}
	selectShared(t, store, acl.ID)
	if _, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 603}); err == nil {
		t.Fatal("adopted another application's owned ACL")
	}
}

type dependencyChangingProvider struct {
	*probeProviders
	writeKind  string
	afterWrite func()
	afterApply func()
}

func (p *dependencyChangingProvider) Create(ctx context.Context, o OPNsense, kind string, fields map[string]string) (Resource, error) {
	r, err := p.probeProviders.Create(ctx, o, kind, fields)
	if err == nil && p.afterWrite != nil && (p.writeKind == "" || p.writeKind == kind) {
		action := p.afterWrite
		p.afterWrite = nil
		action()
	}
	return r, err
}
func (p *dependencyChangingProvider) Apply(ctx context.Context, o OPNsense, service string) error {
	err := p.probeProviders.Apply(ctx, o, service)
	if err == nil && p.afterApply != nil {
		action := p.afterApply
		p.afterApply = nil
		action()
	}
	return err
}
func TestReviewedDependenciesRejectStalePreparationAndEveryRecoveryApply(t *testing.T) {
	for _, dependency := range []string{"acl", "subnet"} {
		for _, phase := range []string{"stale", "preparation", "before-enable", "recovery", "second-recovery"} {
			t.Run(dependency+"/"+phase, func(t *testing.T) {
				s, p, store := demoService(t)
				selectShared(t, store, sharedDemoACL)
				change := func() {
					if dependency == "acl" {
						changeFixture(t, store, sharedDemoACL, "clientIps", "10.99.0.0/24")
					} else {
						changeFixture(t, store, vlan6Subnet, "description", "topology edited")
					}
				}
				wrapper := &dependencyChangingProvider{probeProviders: p}
				s.Providers = wrapper
				plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604})
				if err != nil {
					t.Fatal(err)
				}
				switch phase {
				case "stale":
					change()
				case "preparation":
					wrapper.afterWrite = change
				case "before-enable":
					wrapper.writeKind = "handle"
					wrapper.afterWrite = change
				default:
					p.applyFail = true
				}
				op, err := s.ApplyPlan(context.Background(), plan.ID)
				if phase == "stale" || phase == "preparation" || phase == "before-enable" {
					if err == nil {
						t.Fatal("accepted changed dependency")
					}
					for _, event := range p.events {
						if event == "set:domain:1" || strings.HasPrefix(event, "apply:") {
							t.Fatal("enabled/applied after dependency change", p.events)
						}
					}
					if phase == "stale" && len(p.events) != 0 {
						t.Fatal("stale plan wrote", p.events)
					}
					return
				}
				if err == nil || op.Step != "reconfigure" {
					t.Fatal(op, err)
				}
				p.applyFail = false
				p.events = nil
				if phase == "recovery" {
					change()
				} else {
					wrapper.afterApply = change
				}
				if _, err = s.Recover(context.Background(), op.ID); err == nil {
					t.Fatal("recovered changed dependency")
				}
				want := 0
				if phase == "second-recovery" {
					want = 1
				}
				if len(p.events) != want {
					t.Fatal("did not guard every service application", p.events)
				}
			})
		}
	}
}

func TestAutoSubnetContainmentVLANsAmbiguityAndCleanupOriginalIdentity(t *testing.T) {
	s, p, store := demoService(t)
	for _, tc := range []struct {
		host   string
		vmid   int
		subnet string
	}{{"main", 604, vlan6Subnet}, {"pvedell", 702, vlan7Subnet}} {
		plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: tc.host, VMID: tc.vmid})
		if err != nil {
			t.Fatal(err)
		}
		if plan.ResolvedSubnet == nil || plan.ResolvedSubnet.ID != tc.subnet {
			t.Fatal(plan)
		}
		if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
			t.Fatal(err)
		}
		own := store.Read().Owned[key(tc.host, tc.vmid)]
		ref, _ := ownedByKind(own, "reservation")
		if ref.SubnetID != tc.subnet {
			t.Fatal("reservation subnet identity not recorded", ref)
		}
	}
	// Host derivation /24 is distinct from the discovered DHCP CIDR.
	changeFixture(t, store, vlan6Subnet, "subnet", "192.168.6.0/23")
	if _, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 603}); err != nil {
		t.Fatal("forced exact host/DHCP CIDR equality", err)
	}
	if _, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "pvedell", VMID: 701}); err == nil {
		t.Fatal("selected first of overlapping subnets")
	}
	changeFixture(t, store, vlan6Subnet, "subnet", "192.168.99.0/24")
	if _, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 603}); err == nil {
		t.Fatal("accepted missing matching subnet")
	}
	p.inventory = withoutDemoCT
	if err := store.Update(func(st *State) error {
		st.Config.Hosts[0].Network = "10.0.6.0/24"
		st.Config.OPNsenses[0].SubnetID = vlan7Subnet
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 604})
	if err != nil {
		t.Fatal("cleanup followed current topology instead of owned UUID", err)
	}
	saw := false
	for _, effect := range plan.Effects {
		if effect.Kind == "reservation" {
			for _, field := range effect.Changes {
				if field.Field == "subnetId" && field.Before == vlan6Subnet {
					saw = true
				}
			}
		}
	}
	if !saw {
		t.Fatal("cleanup lost original subnet identity")
	}
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
		t.Fatal(err)
	}
	for _, event := range p.events {
		if strings.Contains(event, ":subnet") {
			t.Fatal("subnet mutation", event)
		}
	}
}

func TestAppRoutesAreIndependentAndPersistWithoutFirewallDefaults(t *testing.T) {
	s, p, store := demoService(t)
	type app struct {
		vmid, port int
		scheme     string
	}
	for _, a := range []app{{603, 8081, "http"}, {604, 9443, "https"}} {
		scheme, port := a.scheme, a.port
		plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: a.vmid, UpstreamScheme: &scheme, UpstreamPort: &port})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
			t.Fatal(err)
		}
	}
	sibling := store.Read().Owned["main:604"]
	scheme, port := "https", 8443
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 603, UpstreamScheme: &scheme, UpstreamPort: &port})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
		t.Fatal(err)
	}
	if fingerprint(store.Read().Owned["main:604"]) != fingerprint(sibling) {
		t.Fatal("changing one app changed another")
	}
	// No custom certificate on normal create; TLS remains automatic.
	snap, _ := p.Snapshot(context.Background(), store.Read().Config.OPNsenses[0])
	for _, r := range snap.Resources {
		if r.Kind == "domain" {
			if _, set := r.Fields["CustomCertificate"]; set {
				t.Fatal("injected custom certificate")
			}
		}
	}
	s.Close()
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(strings.TrimSuffix(store.path, "/state.json"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	fresh, err := NewDemo(reopened)
	if err != nil {
		t.Fatal(err)
	}
	next := NewService(reopened, fresh)
	defer next.Close()
	plan, err = next.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604})
	if err != nil || plan.DesiredRoute.Port != 9443 || plan.DesiredRoute.Scheme != "https" {
		t.Fatal("app route did not survive restart", plan, err)
	}
}

func TestOneProxmoxHostCanResolveTargetsAcrossVLANProfiles(t *testing.T) {
	s, p, store := demoService(t)
	if err := store.Update(func(st *State) error {
		st.Config.Hosts = st.Config.Hosts[:1]
		st.Config.Hosts[0].AddressProfiles = []AddressProfile{{701, 799, "192.168.7.0/24", "d0:cc:37:00:07"}}
		return ValidateConfig(st.Config)
	}); err != nil {
		t.Fatal(err)
	}
	p.inventory = func(inv Inventory) Inventory {
		c := Container{Key: "main:702", HostID: "main", Node: "main", VMID: 702, Name: "vlan-seven", Status: "running", IP: "192.168.7.2", MAC: "d0:cc:37:00:07:02", Identity: "same-host-vlan-seven", Warnings: []string{}}
		c.Fingerprint = fingerprint(c)
		inv.Containers = append(inv.Containers, c)
		inv.Hosts[0].Guests = append(inv.Hosts[0].Guests, Guest{702, "lxc", "main"})
		return inv
	}
	for _, tc := range []struct {
		vmid   int
		subnet string
	}{{604, vlan6Subnet}, {702, vlan7Subnet}} {
		plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: tc.vmid})
		if err != nil || plan.ResolvedSubnet == nil || plan.ResolvedSubnet.ID != tc.subnet {
			t.Fatal(plan, err)
		}
		if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
			t.Fatal(err)
		}
	}
	cfg := store.Read().Config
	cfg.Hosts[0].AddressProfiles = append(cfg.Hosts[0].AddressProfiles, AddressProfile{701, 799, "192.168.7.0/24", "d0:cc:37:00:07"})
	if ValidateConfig(cfg) == nil {
		t.Fatal("ambiguous host profiles accepted")
	}
}
