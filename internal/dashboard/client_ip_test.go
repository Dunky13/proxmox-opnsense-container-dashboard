package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientIPPolicyCompatibility(t *testing.T) {
	r := Resource{ID: sharedDemoACL, Kind: "acl", Fields: map[string]string{"clientIps": "10.8.0.0/24", "RequestMatcher": "client_ip", "accesslistInvert": "0"}}
	general := Resource{ID: "general", Kind: "caddyGeneral", Fields: map[string]string{"accesslist": "", "ClientIpHeaders": ""}}
	fields := map[string]any{"accesslist": "", "ClientIpHeaders": ""}
	c := resolveClientIPContext(general, fields, nil, func(string) (string, error) { t.Fatal("fetched unselected header"); return "", nil })
	a := describePolicyAccessList(r, ProviderSnapshot{ClientIPContext: c})
	if !a.Suitable {
		t.Fatal("valid native client_ip policy rejected:", a.Reason)
	}
}

const clientDemoACL = "aaaaaaaa-3333-4333-8333-333333333333"
const trustDemoACL = "aaaaaaaa-4444-4444-8444-444444444444"
const clientDemoHeader = "aaaaaaaa-5555-4555-8555-555555555555"

func TestLayer4EnabledWithoutRulesUsesHTTPPolicyContext(t *testing.T) {
	for _, mode := range []string{"empty", "empty-arrays", "malformed-header-array", "configured", "failed", "missing-rows", "incomplete", "proxy-protocol"} {
		t.Run(mode, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Fatal("policy discovery wrote")
				}
				switch r.URL.Path {
				case "/api/caddy/reverse_proxy/search_access_list":
					writeJSON(w, 200, map[string]any{"total": 1, "rows": []map[string]string{{"uuid": clientDemoACL}}})
				case "/api/caddy/reverse_proxy/get_access_list/" + clientDemoACL:
					writeJSON(w, 200, map[string]any{"accesslist": map[string]string{"accesslistName": "LAN VPN", "RequestMatcher": "client_ip", "clientIps": "10.8.0.0/24", "accesslistInvert": "0"}})
				case "/api/caddy/general/get":
					allow := ""
					if mode == "proxy-protocol" {
						allow = "192.0.2.0/24"
					}
					general := map[string]any{"enabled": "1", "accesslist": "", "ClientIpHeaders": "", "EnableLayer4": "1", "ProxyProtocolAllow": allow}
					if mode == "empty-arrays" {
						general["accesslist"] = []any{}
						general["ClientIpHeaders"] = []any{}
					}
					if mode == "malformed-header-array" {
						general["ClientIpHeaders"] = []any{clientDemoHeader}
					}
					writeJSON(w, 200, map[string]any{"caddy": map[string]any{"general": general}})
				case "/api/caddy/reverse_proxy/search_layer4":
					if r.URL.Query().Get("rowCount") != "-1" || r.URL.Query().Get("current") != "1" {
						t.Fatal("partial Layer 4 request")
					}
					switch mode {
					case "configured":
						writeJSON(w, 200, map[string]any{"total": 1, "rows": []map[string]string{{"uuid": vlan6Subnet}}})
					case "failed":
						w.WriteHeader(503)
					case "missing-rows":
						writeJSON(w, 200, map[string]int{"total": 0})
					case "incomplete":
						writeJSON(w, 200, map[string]any{"total": 1, "rows": []any{}})
					default:
						writeJSON(w, 200, map[string]any{"total": 0, "rows": []any{}})
					}
				default:
					t.Error("unexpected policy dependency", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			policy, err := (&RealProviders{}).CaddyPolicy(context.Background(), OPNsense{BaseURL: srv.URL, CAPEM: trustServer(srv), APIKey: "fixture-key", APISecret: "fixture-secret"})
			if err != nil {
				t.Fatal(err)
			}
			if policy.ClientIPContext.Suitable != (mode == "empty" || mode == "empty-arrays") {
				t.Fatal(mode, policy.ClientIPContext)
			}
			if mode == "empty" && !policy.ClientIPContext.Layer4EmptyVerified {
				t.Fatal("empty Layer 4 model not bound")
			}
		})
	}
}

func TestNewLayer4RulesInvalidatePrivatePreparationAndRecovery(t *testing.T) {
	for _, phase := range []string{"preparation", "recovery-first", "recovery-second"} {
		t.Run(phase, func(t *testing.T) {
			s, p, store := demoService(t)
			selectShared(t, store, clientDemoACL)
			changeFixture(t, store, "general", "EnableLayer4", "1")
			wrapper := &dependencyChangingProvider{probeProviders: p}
			s.Providers = wrapper
			plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604})
			if err != nil || !plan.SharedAccessList.ClientIPContext.Layer4EmptyVerified {
				t.Fatal(plan, err)
			}
			mutate := func() {
				if err := store.Update(func(st *State) error {
					st.DemoResources["demo-edge"] = append(st.DemoResources["demo-edge"], Resource{ID: vlan6Subnet, Kind: "caddyLayer4", Fields: map[string]string{"enabled": "1"}})
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "preparation" {
				wrapper.afterWrite = mutate
			} else {
				p.applyFail = true
			}
			op, err := s.ApplyPlan(context.Background(), plan.ID)
			if err == nil {
				t.Fatal("expected partial operation")
			}
			if phase == "preparation" {
				for _, event := range p.events {
					if event == "set:domain:1" || strings.HasPrefix(event, "apply:") {
						t.Fatal("enabled route after Layer 4 rules appeared", p.events)
					}
				}
				return
			}
			p.applyFail = false
			p.events = nil
			if phase == "recovery-first" {
				mutate()
			} else {
				wrapper.afterApply = mutate
			}
			if _, err = s.Recover(context.Background(), op.ID); err == nil {
				t.Fatal("recovered with changed listener model")
			}
			want := 0
			if phase == "recovery-second" {
				want = 1
			}
			if len(p.events) != want {
				t.Fatal("unexpected service applications", p.events)
			}
		})
	}
}

func TestClientIPContextRestrictionAndObservability(t *testing.T) {
	for _, tc := range []struct{ name, field, value string }{
		{"empty-trust", "clientIps", ""}, {"all-v4", "clientIps", "0.0.0.0/0"},
		{"combined-v4", "clientIps", "0.0.0.0/1,128.0.0.0/1"}, {"combined-v6", "clientIps", "::/1,8000::/1"},
		{"noncanonical", "clientIps", "192.0.2.1/24"}, {"mapped", "clientIps", "::ffff:192.0.2.0/120"},
		{"missing-trust", "delete", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p, store := demoService(t)
			selectShared(t, store, clientDemoACL)
			changeFixture(t, store, trustDemoACL, tc.field, tc.value)
			discovery, err := s.AccessLists(context.Background(), "demo-edge")
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, a := range discovery.AccessLists {
				if a.ID == clientDemoACL {
					found = true
					if a.Suitable || a.Reason == "" {
						t.Fatal(a)
					}
				}
			}
			if !found {
				t.Fatal("hid unsuitable policy")
			}
			if _, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604}); err == nil {
				t.Fatal("unsafe client policy accepted")
			}
			if len(p.events) != 0 {
				t.Fatal("planning wrote", p.events)
			}
			// The same unavailable context must not prohibit public or unrelated remote_ip policies.
			plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604, Public: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
				t.Fatal(err)
			}
			selectShared(t, store, sharedDemoACL)
			plan, err = s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604})
			if err != nil {
				t.Fatal("remote policy inherited client context failure", err)
			}
			if plan.SharedAccessList.ClientIPContextFingerprint != "" {
				t.Fatal("changed legacy remote reference")
			}
			if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
				t.Fatal(err)
			}
			p.inventory = withoutDemoCT
			plan, err = s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 604})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
				t.Fatal(err)
			}
			for _, event := range p.events {
				if strings.Contains(event, ":acl") {
					t.Fatal("external policy write", p.events)
				}
			}
		})
	}
	for _, tc := range []struct{ name, id, field, value string }{
		{"missing-general", "general", "delete", ""}, {"missing-header", clientDemoHeader, "delete", ""},
		{"bad-header", clientDemoHeader, "HeaderType", "X-Forwarded-For\nAuthorization"},
		{"unresolved-header", "general", "ClientIpHeaders", vlan6Subnet},
		{"multiple-trust", "general", "accesslist", trustDemoACL + "," + sharedDemoACL},
		{"layer4", "general", "EnableLayer4", "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, store := demoService(t)
			selectShared(t, store, clientDemoACL)
			changeFixture(t, store, tc.id, tc.field, tc.value)
			if tc.name == "layer4" {
				if err := store.Update(func(st *State) error {
					st.DemoResources["demo-edge"] = append(st.DemoResources["demo-edge"], Resource{ID: vlan6Subnet, Kind: "caddyLayer4", Fields: map[string]string{"enabled": "1"}})
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604}); err == nil {
				t.Fatal("unresolved context enabled route")
			}
		})
	}
}
func TestClientIPNoProxyFallbackAndSelectionShapes(t *testing.T) {
	s, p, store := demoService(t)
	selectShared(t, store, clientDemoACL)
	changeFixture(t, store, "general", "accesslist", "")
	changeFixture(t, store, "general", "ClientIpHeaders", "")
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604})
	if err != nil {
		t.Fatal(err)
	}
	c := plan.SharedAccessList.ClientIPContext
	if !c.Suitable || len(c.TrustedProxyCIDRs) != 0 || !strings.Contains(c.Summary, "no global trusted proxies") || !strings.Contains(c.Summary, "X-Forwarded-For") {
		t.Fatal(c)
	}
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
		t.Fatal(err)
	}
	snap, _ := p.Snapshot(context.Background(), store.Read().Config.OPNsenses[0])
	// Trust uses only clientIps, regardless of the trust row's domain-ACL fields.
	trust, _ := snap.Find(trustDemoACL, "acl")
	trust.Fields["RequestMatcher"] = "expression"
	trust.Fields["accesslistInvert"] = "1"
	fields := map[string]any{"accesslist": map[string]any{trustDemoACL: map[string]any{"selected": true}}, "ClientIpHeaders": map[string]any{clientDemoHeader: map[string]any{"selected": 1.0}, vlan7Subnet: map[string]any{"selected": "1"}, vlan6Subnet: map[string]any{"selected": false}}}
	general := Resource{Fields: map[string]string{"accesslist": trustDemoACL}}
	c = resolveClientIPContext(general, fields, []Resource{trust}, func(id string) (string, error) {
		if id == clientDemoHeader {
			return "Cf-Connecting-Ip", nil
		}
		if id == vlan7Subnet {
			return "X-Forwarded-For", nil
		}
		t.Fatal("read unselected header")
		return "", nil
	})
	if !c.Suitable || c.HeaderOrder != "notExposed" || len(c.Headers) != 2 || !strings.Contains(c.Summary, "unverified") {
		t.Fatal(c)
	}
	for _, value := range []any{nil, 1.0, map[string]any{trustDemoACL: map[string]any{"value": "label"}}, map[string]any{trustDemoACL: map[string]any{"selected": "maybe"}}} {
		fields["accesslist"] = value
		if c = resolveClientIPContext(general, fields, []Resource{trust}, func(string) (string, error) { t.Fatal("read header after malformed trust"); return "", nil }); c.Suitable {
			t.Fatal("malformed trust treated as no proxies", value)
		}
	}
}

type clientContextChangingProvider struct {
	*dependencyChangingProvider
	afterEnable func()
}

func (p *clientContextChangingProvider) Set(ctx context.Context, o OPNsense, r Resource, fields map[string]string) (Resource, error) {
	out, err := p.dependencyChangingProvider.Set(ctx, o, r, fields)
	if err == nil && r.Kind == "domain" && fields["enabled"] == "1" && p.afterEnable != nil {
		f := p.afterEnable
		p.afterEnable = nil
		f()
	}
	return out, err
}
func TestClientIPContextGuardsEveryMutationAndRecoveryApply(t *testing.T) {
	changes := []struct{ name, id, field, value string }{
		{"trust-cidrs", trustDemoACL, "clientIps", "198.51.100.0/24"},
		{"trust-selection", "general", "accesslist", sharedDemoACL},
		{"header-type", clientDemoHeader, "HeaderType", "Cf-Connecting-Ip"},
		{"header-selection", "general", "ClientIpHeaders", ""},
		{"layer4", "general", "EnableLayer4", "1"},
		{"proxy-protocol", "general", "ProxyProtocolAllow", "192.0.2.0/24"},
		{"unavailable", "general", "delete", ""},
	}
	for _, change := range changes {
		for _, phase := range []string{"stale", "preparation", "before-enable", "first-apply", "second-apply", "recovery-first", "recovery-second"} {
			t.Run(change.name+"/"+phase, func(t *testing.T) {
				s, p, store := demoService(t)
				selectShared(t, store, clientDemoACL)
				wrapper := &clientContextChangingProvider{dependencyChangingProvider: &dependencyChangingProvider{probeProviders: p}}
				s.Providers = wrapper
				plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604})
				if err != nil {
					t.Fatal(err)
				}
				if plan.SharedAccessList.ClientIPContextFingerprint == "" {
					t.Fatal("unbound context")
				}
				mutate := func() { changeFixture(t, store, change.id, change.field, change.value) }
				switch phase {
				case "stale":
					mutate()
				case "preparation":
					wrapper.afterWrite = mutate
				case "before-enable":
					wrapper.writeKind = "handle"
					wrapper.afterWrite = mutate
				case "first-apply":
					wrapper.afterEnable = mutate
				case "second-apply":
					wrapper.afterApply = mutate
				default:
					p.applyFail = true
				}
				op, err := s.ApplyPlan(context.Background(), plan.ID)
				if err == nil {
					t.Fatal("accepted changed dependency")
				}
				if strings.HasPrefix(phase, "recovery") {
					if op.Step != "reconfigure" || op.ManualRecovery {
						t.Fatal(op)
					}
					p.applyFail = false
					p.events = nil
					if phase == "recovery-first" {
						mutate()
					} else {
						wrapper.afterApply = mutate
					}
					if _, err = s.Recover(context.Background(), op.ID); err == nil {
						t.Fatal("recovery accepted context change")
					}
				}
				applies := 0
				for _, event := range p.events {
					if strings.Contains(event, ":acl") {
						t.Fatal("external policy write", event)
					}
					if strings.HasPrefix(event, "apply:") {
						applies++
					}
					if (phase == "stale" || phase == "preparation" || phase == "before-enable") && event == "set:domain:1" {
						t.Fatal("enabled after dependency change", p.events)
					}
				}
				want := 0
				if phase == "second-apply" || phase == "recovery-second" {
					want = 1
				}
				if applies != want {
					t.Fatal("unguarded service application", p.events)
				}
				if phase == "stale" && len(p.events) != 0 {
					t.Fatal("stale plan wrote")
				}
				for _, own := range store.Read().Owned {
					for _, r := range own.Resources {
						if r.ID == trustDemoACL || r.ID == clientDemoACL || r.ID == clientDemoHeader {
							t.Fatal("claimed external context", r)
						}
					}
				}
			})
		}
	}
}
func TestLegacyClientIPReferencesCannotAcquireContextAuthority(t *testing.T) {
	for _, phase := range []string{"plan", "recovery"} {
		t.Run(phase, func(t *testing.T) {
			s, p, store := demoService(t)
			selectShared(t, store, clientDemoACL)
			plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604})
			if err != nil {
				t.Fatal(err)
			}
			if phase == "plan" {
				if err = store.Update(func(st *State) error {
					plan.SharedAccessList.ClientIPContext = nil
					plan.SharedAccessList.ClientIPContextFingerprint = ""
					st.Plans[plan.ID] = plan
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if _, err = s.ApplyPlan(context.Background(), plan.ID); err == nil {
					t.Fatal("legacy plan gained authority")
				}
			} else {
				p.applyFail = true
				op, err := s.ApplyPlan(context.Background(), plan.ID)
				if err == nil {
					t.Fatal("expected interrupted apply")
				}
				if err = store.Update(func(st *State) error {
					op.SharedAccessList.ClientIPContext = nil
					op.SharedAccessList.ClientIPContextFingerprint = ""
					st.Operations[op.ID] = op
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				p.events = nil
				p.applyFail = false
				if _, err = s.Recover(context.Background(), op.ID); err == nil {
					t.Fatal("legacy operation gained authority")
				}
			}
			if len(p.events) != 0 {
				t.Fatal("wrote before refusing legacy authority", p.events)
			}
		})
	}
}

func TestCaddyPolicyHTTPSIsolationAndFailureReporting(t *testing.T) {
	for _, mode := range []string{"valid", "no-proxies", "general-denied", "headers-denied", "missing-header", "bad-header", "broad-trust", "missing-selections", "malformed-selections", "acl-denied", "incomplete-acls"} {
		t.Run(mode, func(t *testing.T) {
			calls := []string{}
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.URL.Path)
				if r.Method != "GET" {
					t.Error("policy read wrote", r.Method)
				}
				key, secret, ok := r.BasicAuth()
				if !ok || key != "fixture-key" || secret != "fixture-secret" {
					t.Error("credentials not supplied")
				}
				switch r.URL.Path {
				case "/api/caddy/reverse_proxy/search_access_list":
					if mode == "acl-denied" {
						w.WriteHeader(403)
						return
					}
					total := 3
					if mode == "incomplete-acls" {
						total = 4
					}
					writeJSON(w, 200, map[string]any{"total": total, "rows": []map[string]string{{"uuid": clientDemoACL}, {"uuid": trustDemoACL}, {"uuid": sharedDemoACL}}})
				case "/api/caddy/reverse_proxy/get_access_list/" + clientDemoACL, "/api/caddy/reverse_proxy/get_access_list/" + trustDemoACL, "/api/caddy/reverse_proxy/get_access_list/" + sharedDemoACL:
					matcher := "remote_ip"
					cidrs := "192.0.2.0/24,2001:db8::/32"
					if strings.HasSuffix(r.URL.Path, clientDemoACL) {
						matcher = "client_ip"
						cidrs = "10.8.0.0/24"
					}
					if mode == "broad-trust" && strings.HasSuffix(r.URL.Path, trustDemoACL) {
						cidrs = "0.0.0.0/1,128.0.0.0/1"
					}
					writeJSON(w, 200, map[string]any{"accesslist": map[string]any{"accesslistName": "Fixture policy", "RequestMatcher": map[string]any{matcher: map[string]any{"value": "translated label", "selected": true}}, "clientIps": cidrs, "accesslistInvert": "0"}})
				case "/api/caddy/general/get":
					if mode == "general-denied" {
						w.WriteHeader(403)
						return
					}
					general := map[string]any{"enabled": "0", "accesslist": map[string]any{trustDemoACL: map[string]any{"selected": 1.0}}, "ClientIpHeaders": map[string]any{clientDemoHeader: map[string]any{"selected": true}, vlan6Subnet: map[string]any{"selected": false}}, "EnableLayer4": "0", "ProxyProtocolAllow": "", "credential": "general-secret-never-retain"}
					if mode == "no-proxies" {
						general["accesslist"] = map[string]any{trustDemoACL: map[string]any{"selected": 0}}
					}
					if mode == "missing-selections" {
						delete(general, "ClientIpHeaders")
					}
					if mode == "malformed-selections" {
						general["accesslist"] = map[string]any{trustDemoACL: map[string]any{"value": "label without selection"}}
					}
					writeJSON(w, 200, map[string]any{"caddy": map[string]any{"general": general}})
				case "/api/caddy/reverse_proxy/get_header/" + clientDemoHeader:
					if mode == "headers-denied" {
						w.WriteHeader(403)
						return
					}
					if mode == "missing-header" {
						writeJSON(w, 200, map[string]any{"header": nil})
						return
					}
					kind := "Cf-Connecting-Ip"
					if mode == "bad-header" {
						kind = "Cf-Connecting-Ip Other"
					}
					writeJSON(w, 200, map[string]any{"header": map[string]any{"HeaderType": kind, "HeaderValue": "header-secret-never-retain", "HeaderReplace": "replace-secret-never-retain"}})
				default:
					t.Errorf("unrelated or unselected policy read %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			s, _, store := demoService(t)
			if err := store.Update(func(st *State) error {
				o := &st.Config.OPNsenses[0]
				o.BaseURL = srv.URL
				o.CAPEM = trustServer(srv)
				o.APIKey = "fixture-key"
				o.APISecret = "fixture-secret"
				o.CertificateID = ""
				o.DomainReady = false
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			s.Providers = &RealProviders{}
			result, err := s.AccessLists(context.Background(), "demo-edge")
			failedInventory := mode == "acl-denied" || mode == "incomplete-acls"
			if (err != nil) != failedInventory {
				t.Fatal("error confused with empty inventory", result, err)
			}
			if failedInventory {
				return
			}
			if len(result.AccessLists) != 3 {
				t.Fatal("policy context error hid ACL rows", result)
			}
			for _, a := range result.AccessLists {
				if a.ID == clientDemoACL && a.Suitable != (mode == "valid" || mode == "no-proxies") {
					t.Fatal("incorrect client policy suitability", a)
				}
				if a.ID == sharedDemoACL && !a.Suitable {
					t.Fatal("remote policy depended on client context", a)
				}
			}
			raw, _ := json.Marshal(result)
			if strings.Contains(string(raw), "secret-never-retain") || strings.Contains(string(raw), "HeaderValue") {
				t.Fatal("secret metadata retained")
			}
			if len(calls) == 0 {
				t.Fatal("no reads")
			}
		})
	}
}
func TestGlobalTrustedProxyACLIsNeverMutatedOrClaimed(t *testing.T) {
	s, p, store := demoService(t)
	ensureDemo(t, s)
	own := store.Read().Owned["main:604"]
	acl, _ := ownedByKind(own, "acl")
	changeFixture(t, store, "general", "accesslist", acl.ID)
	if err := store.Update(func(st *State) error { st.Config.OPNsenses[0].AllowlistCIDRs = []string{"10.9.0.0/24"}; return nil }); err != nil {
		t.Fatal(err)
	}
	p.events = nil
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err == nil {
		t.Fatal("modified global trusted proxy ACL")
	}
	for _, event := range p.events {
		if strings.Contains(event, ":acl") {
			t.Fatal("wrote protected global policy", p.events)
		}
	}
	snap, _ := p.Snapshot(context.Background(), store.Read().Config.OPNsenses[0])
	after, _ := snap.Find(acl.ID, "acl")
	if after.Fingerprint() != acl.Fingerprint {
		t.Fatal("global trust changed")
	}
	// A shared client policy cannot rely on a list another dashboard app owns.
	selectShared(t, store, clientDemoACL)
	discovery, err := s.AccessLists(context.Background(), "demo-edge")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range discovery.AccessLists {
		if a.ID == clientDemoACL && a.Suitable {
			t.Fatal("exposed dashboard-owned trust as stable external context")
		}
	}
}
