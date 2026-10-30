package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	hikyoReservation = "a1111111-1111-4111-8111-111111111609"
	hikyoSubnet      = "a2222222-2222-4222-8222-222222222006"
	hikyoParent      = "a3333333-3333-4333-8333-333333333000"
	hikyoChild       = "a4444444-4444-4444-8444-444444444609"
	hikyoHandle      = "a5555555-5555-4555-8555-555555555609"
)

func hikyoService(t *testing.T) (*Service, *probeProviders, *Store) {
	t.Helper()
	s, p, store := demoService(t)
	p.inventory = func(inv Inventory) Inventory {
		mac, ip, err := DeriveAddress(store.Read().Config.Hosts[0], 609)
		if err != nil {
			t.Fatal(err)
		}
		c := Container{Key: "main:609", HostID: "main", Node: "main", VMID: 609, Name: "hikyo", Status: "running", IP: ip, MAC: mac, Identity: "enrolled-hikyo-generation", Warnings: []string{}}
		c.Fingerprint = fingerprint(c)
		inv.Containers = append(inv.Containers, c)
		for i := range inv.Hosts {
			if inv.Hosts[i].HostID == "main" {
				inv.Hosts[i].Guests = append(inv.Hosts[i].Guests, Guest{609, "lxc", "main"})
			}
		}
		sortInventory(&inv)
		return inv
	}
	if err := store.Update(func(st *State) error {
		st.Config.OPNsenses[0].PrivateAccessListID = "aaaaaaaa-3333-4333-8333-333333333333"
		rows := st.DemoResources["demo-edge"]
		for i := range rows {
			if rows[i].Kind == "subnet" && rows[i].Fields["subnet"] == "192.168.6.0/24" {
				rows[i].ID = hikyoSubnet
			}
		}
		st.DemoResources["demo-edge"] = append(rows,
			Resource{ID: hikyoReservation, Kind: "reservation", Fields: map[string]string{"subnet": hikyoSubnet, "ip_address": "192.168.6.9", "hw_address": "d0:cc:37:00:06:09", "hostname": "speedtest-tracker", "description": "Existing reservation"}},
			Resource{ID: hikyoParent, Kind: "domain", Fields: map[string]string{"enabled": "1", "FromDomain": "*.went.io", "accesslist": "", "DisableTls": "0", "DnsChallenge": "1", "CustomCertificate": "", "description": "External wildcard"}},
			Resource{ID: hikyoChild, Kind: "subdomain", Fields: map[string]string{"enabled": "1", "reverse": hikyoParent, "FromDomain": "hikyo.went.io", "accesslist": "", "description": ""}},
			Resource{ID: hikyoHandle, Kind: "handle", Fields: map[string]string{"enabled": "1", "reverse": hikyoParent, "subdomain": hikyoChild, "HandleType": "handle", "HandleDirective": "reverse_proxy", "ToDomain": "192.168.6.9", "ToPort": "8080", "HttpTls": "0", "HttpTlsInsecureSkipVerify": "0", "accesslist": "", "description": ""}},
			Resource{ID: "b3333333-3333-4333-8333-333333333000", Kind: "domain", Fields: map[string]string{"enabled": "1", "FromDomain": "*.dbugit.io", "accesslist": ""}},
			Resource{ID: "b4444444-4444-4444-8444-444444444609", Kind: "subdomain", Fields: map[string]string{"enabled": "1", "reverse": "b3333333-3333-4333-8333-333333333000", "FromDomain": "hikyo.dbugit.io", "accesslist": ""}},
			Resource{ID: "b5555555-5555-4555-8555-555555555609", Kind: "handle", Fields: map[string]string{"enabled": "1", "reverse": "b3333333-3333-4333-8333-333333333000", "subdomain": "b4444444-4444-4444-8444-444444444609", "HandleDirective": "reverse_proxy", "ToDomain": "192.168.3.240", "ToPort": "443", "HttpTls": "1", "HttpTlsInsecureSkipVerify": "1"}},
		)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return s, p, store
}

func TestExistingHikyoDiagnosticsExposeReviewedImport(t *testing.T) {
	s, _, _ := hikyoService(t)
	r := s.Reconcile(context.Background())
	for _, item := range r.Items {
		if item.Key != "main:609" {
			continue
		}
		raw, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `"importAvailable":true`) || !strings.Contains(string(raw), hikyoChild) || !strings.Contains(string(raw), "speedtest-tracker") || !strings.Contains(string(raw), `"port":8080`) {
			t.Fatalf("matching native existing route lacks reviewed import diagnostics: %s", raw)
		}
		if strings.Contains(string(raw), "192.168.3.240") {
			t.Fatalf("unrelated Hikyo route selected: %s", raw)
		}
		return
	}
	t.Fatal("Hikyo missing from reconciliation")
}

func mutateHikyo(t *testing.T, store *Store, fn func(*State, *[]Resource)) {
	t.Helper()
	if err := store.Update(func(st *State) error {
		rows := st.DemoResources["demo-edge"]
		fn(st, &rows)
		st.DemoResources["demo-edge"] = rows
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func changeHikyo(rows []Resource, id, field, value string) {
	for i := range rows {
		if rows[i].ID == id {
			rows[i].Fields[field] = value
		}
	}
}
func importHikyo(t *testing.T, s *Service) Operation {
	t.Helper()
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "import", HostID: "main", VMID: 609})
	if err != nil {
		t.Fatal(err)
	}
	op, err := s.ApplyPlan(context.Background(), plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	return op
}
func hikyoItem(t *testing.T, s *Service) ReconcileItem {
	t.Helper()
	for _, item := range s.Reconcile(context.Background()).Items {
		if item.Key == "main:609" {
			return item
		}
	}
	t.Fatal("missing Hikyo reconciliation")
	return ReconcileItem{}
}
func requireExternalUnchanged(t *testing.T, before, after []Resource) {
	t.Helper()
	for _, r := range before {
		if r.ID == hikyoReservation || r.ID == hikyoChild || r.ID == hikyoHandle {
			continue
		}
		found := false
		for _, now := range after {
			if r.ID == now.ID && r.Kind == now.Kind {
				found = true
				if r.Fingerprint() != now.Fingerprint() {
					t.Fatalf("external %s %s changed", r.Kind, r.ID)
				}
			}
		}
		if !found {
			t.Fatalf("external %s %s deleted", r.Kind, r.ID)
		}
	}
}
func TestExistingHikyoImportIsMetadataOnlyAndThenReviewedEnsure(t *testing.T) {
	s, p, store := hikyoService(t)
	before := store.Read().DemoResources["demo-edge"]
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "import", HostID: "main", VMID: 609})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != "import" || plan.DesiredRoute != (RouteConfig{Scheme: "http", Port: 8080, Public: true}) || !plan.Public {
		t.Fatalf("wrong observed plan: %+v", plan)
	}
	if !strings.Contains(strings.Join(plan.ApplyNotes, " "), "speedtest-tracker") || !strings.Contains(strings.Join(plan.ApplyNotes, " "), "NO provider") {
		t.Fatal("missing ownership/hostname/provider review notes", plan.ApplyNotes)
	}
	adopted := map[string]bool{}
	for _, e := range plan.Effects {
		if e.Action == "adopt" {
			adopted[e.ResourceID] = true
		}
		if e.ResourceID == hikyoParent && e.Action != "retain" {
			t.Fatal("parent adopted", e)
		}
	}
	if len(adopted) != 3 || !adopted[hikyoChild] || !adopted[hikyoHandle] || !adopted[hikyoReservation] {
		t.Fatal("incorrect ownership", adopted)
	}
	if len(store.Read().Owned) != 0 {
		t.Fatal("planning transferred ownership")
	}
	op, err := s.ApplyPlan(context.Background(), plan.ID)
	if err != nil || op.Status != "succeeded" || op.Action != "import" {
		t.Fatal(op, err)
	}
	if len(p.events) != 0 || fingerprint(before) != fingerprint(store.Read().DemoResources["demo-edge"]) {
		t.Fatal("import wrote provider config", p.events)
	}
	own := store.Read().Owned["main:609"]
	if len(own.Resources) != 3 || own.RouteLayout == nil || own.RouteLayout.Kind != "subdomain" || own.RouteLayout.Parent.ID != hikyoParent {
		t.Fatal("native layout lost", own)
	}
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err == nil {
		t.Fatal("consumed import reapplied")
	}
	item := hikyoItem(t, s)
	if !item.Owned || item.State != "mismatch" {
		t.Fatal("hostname drift should remain a managed mismatch", item)
	}
	plan, err = s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 609, Public: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.DesiredRoute.Port != 8080 {
		t.Fatal("ensure reset existing port", plan)
	}
	hostname := false
	for _, e := range plan.Effects {
		if e.Kind == "domain" && e.Action != "retain" {
			t.Fatal("child flattened into standalone domain", e)
		}
		for _, c := range e.Changes {
			if c.Field == "hostname" && c.Before == "speedtest-tracker" && c.After == "hikyo" {
				hostname = true
			}
		}
	}
	if !hostname {
		t.Fatal("review did not expose hostname drift", plan.Effects)
	}
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
		t.Fatal(err)
	}
	item = hikyoItem(t, s)
	if item.State != "healthy" {
		t.Fatal("ensure did not resolve managed drift", item)
	}
	requireExternalUnchanged(t, before, store.Read().DemoResources["demo-edge"])
}

func TestExistingImportRejectsUnsafeOrAmbiguousCandidates(t *testing.T) {
	cases := []struct {
		name, want string
		change     func(*State, *[]Resource)
	}{
		{"wrong-mac", "both must match", func(_ *State, r *[]Resource) { changeHikyo(*r, hikyoReservation, "hw_address", "d0:cc:37:00:06:08") }},
		{"wrong-ip", "both must match", func(_ *State, r *[]Resource) { changeHikyo(*r, hikyoReservation, "ip_address", "192.168.6.8") }},
		{"duplicate-reservation", "IP/MAC candidates", func(_ *State, r *[]Resource) {
			var v Resource
			for _, x := range *r {
				if x.ID == hikyoReservation {
					v = clone(x)
				}
			}
			v.ID = "11111111-2222-4333-8444-555555555555"
			*r = append(*r, v)
		}},
		{"duplicate-uuid", "duplicate provider", func(_ *State, r *[]Resource) {
			for _, x := range *r {
				if x.ID == hikyoChild {
					*r = append(*r, x)
					break
				}
			}
		}},
		{"duplicate-cross-kind", "duplicate provider", func(_ *State, r *[]Resource) {
			*r = append(*r, Resource{ID: hikyoReservation, Kind: "domain", Fields: map[string]string{"FromDomain": "unrelated.example.test"}})
		}},
		{"duplicate-domain", "found 2", func(_ *State, r *[]Resource) {
			*r = append(*r, Resource{ID: "11111111-2222-4333-8444-555555555555", Kind: "domain", Fields: map[string]string{"FromDomain": "hikyo.went.io"}})
		}},
		{"wrong-target", "instead of the intended IPv4", func(_ *State, r *[]Resource) { changeHikyo(*r, hikyoHandle, "ToDomain", "192.168.3.240") }},
		{"wrong-reverse", "inconsistent reverse/subdomain", func(_ *State, r *[]Resource) {
			changeHikyo(*r, hikyoHandle, "reverse", "b3333333-3333-4333-8333-333333333000")
		}},
		{"multiple-handles", "related handle; found 2", func(_ *State, r *[]Resource) {
			for _, x := range *r {
				if x.ID == hikyoHandle {
					v := clone(x)
					v.ID = "11111111-2222-4333-8444-555555555555"
					*r = append(*r, v)
					break
				}
			}
		}},
		{"wrong-subnet", "uniquely resolved KEA subnet", func(_ *State, r *[]Resource) {
			changeHikyo(*r, hikyoReservation, "subnet", "22222222-2222-4222-8222-222222222222")
		}},
		{"overlap-subnet", "multiple KEA subnets", func(_ *State, r *[]Resource) {
			*r = append(*r, Resource{ID: "11111111-2222-4333-8444-555555555555", Kind: "subnet", Fields: map[string]string{"subnet": "192.168.6.0/25"}})
		}},
		{"inherited-parent-policy", "inherited or layered", func(_ *State, r *[]Resource) {
			changeHikyo(*r, hikyoParent, "accesslist", "aaaaaaaa-3333-4333-8333-333333333333")
		}},
		{"unknown-parent-policy", "must be observed", func(_ *State, r *[]Resource) {
			for i := range *r {
				if (*r)[i].ID == hikyoParent {
					delete((*r)[i].Fields, "accesslist")
				}
			}
		}},
		{"unselected-private-policy", "configured external private policy", func(_ *State, r *[]Resource) {
			changeHikyo(*r, hikyoChild, "accesslist", "aaaaaaaa-1111-4111-8111-111111111111")
		}},
		{"insecure-upstream", "HttpTlsInsecureSkipVerify", func(_ *State, r *[]Resource) { changeHikyo(*r, hikyoHandle, "HttpTlsInsecureSkipVerify", "1") }},
		{"handle-path", "HandlePath", func(_ *State, r *[]Resource) { changeHikyo(*r, hikyoHandle, "HandlePath", "/app/*") }},
		{"handle-acl", "accesslist", func(_ *State, r *[]Resource) {
			changeHikyo(*r, hikyoHandle, "accesslist", "aaaaaaaa-3333-4333-8333-333333333333")
		}},
		{"unsafe-scheme", "upstream TLS scheme", func(_ *State, r *[]Resource) { changeHikyo(*r, hikyoHandle, "HttpTls", "2") }},
		{"upstream-set", "instead of the intended IPv4", func(_ *State, r *[]Resource) { changeHikyo(*r, hikyoHandle, "ToDomain", "192.168.6.9,192.168.6.10") }},
		{"disabled-child", "is disabled", func(_ *State, r *[]Resource) { changeHikyo(*r, hikyoChild, "enabled", "0") }},
		{"foreign-child-reference", "unrelated resource", func(_ *State, r *[]Resource) {
			*r = append(*r, Resource{ID: "11111111-2222-4333-8444-555555555555", Kind: "subdomain", Fields: map[string]string{"reverse": hikyoChild}})
		}},
		{"owned-reservation", "already belongs", func(st *State, r *[]Resource) {
			st.Owned["main:777"] = Ownership{OPNsenseID: "demo-edge", Resources: []OwnedResource{{ID: hikyoReservation, Kind: "reservation", Fingerprint: "known"}}}
		}},
		{"owned-parent", "parent", func(st *State, r *[]Resource) {
			st.Owned["main:777"] = Ownership{OPNsenseID: "demo-edge", Resources: []OwnedResource{{ID: hikyoParent, Kind: "domain", Fingerprint: "known"}}}
		}},
		{"unfinished-operation", "unfinished operations", func(st *State, r *[]Resource) {
			st.Operations["pending"] = Operation{OPNsenseID: "demo-edge", Status: "partial"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, p, store := hikyoService(t)
			mutateHikyo(t, store, tc.change)
			item := hikyoItem(t, s)
			if item.ImportAvailable || !strings.Contains(strings.Join(item.ImportIssues, "; "), tc.want) {
				t.Fatalf("wrong eligibility: %+v", item)
			}
			if _, err := s.Plan(context.Background(), PlanRequest{Action: "import", HostID: "main", VMID: 609}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatal("wrong rejection", err)
			}
			if len(p.events) != 0 {
				t.Fatal("unsafe candidate caused writes", p.events)
			}
		})
	}
}

func TestExistingImportRejectsStaleReviewAtApply(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*testing.T, *Service, *probeProviders, *Store, Plan)
	}{
		{"provider-field", func(t *testing.T, _ *Service, _ *probeProviders, st *Store, _ Plan) {
			mutateHikyo(t, st, func(_ *State, r *[]Resource) { changeHikyo(*r, hikyoReservation, "hostname", "changed-after-review") })
		}},
		{"parent-fingerprint", func(t *testing.T, _ *Service, _ *probeProviders, st *Store, _ Plan) {
			mutateHikyo(t, st, func(_ *State, r *[]Resource) { changeHikyo(*r, hikyoParent, "DnsChallenge", "0") })
		}},
		{"wrong-ref", func(t *testing.T, _ *Service, _ *probeProviders, st *Store, _ Plan) {
			mutateHikyo(t, st, func(_ *State, r *[]Resource) {
				changeHikyo(*r, hikyoHandle, "subdomain", "b4444444-4444-4444-8444-444444444609")
			})
		}},
		{"subnet-fingerprint", func(t *testing.T, _ *Service, _ *probeProviders, st *Store, _ Plan) {
			mutateHikyo(t, st, func(_ *State, r *[]Resource) { changeHikyo(*r, hikyoSubnet, "subnet_id", "66") })
		}},
		{"config-hash", func(t *testing.T, _ *Service, _ *probeProviders, st *Store, _ Plan) {
			mutateHikyo(t, st, func(st *State, _ *[]Resource) { st.Config.OPNsenses[0].DomainBase = "changed.example.test" })
		}},
		{"revision", func(t *testing.T, _ *Service, _ *probeProviders, st *Store, _ Plan) {
			mutateHikyo(t, st, func(st *State, _ *[]Resource) { st.Config.Revision++ })
		}},
		{"expiry", func(t *testing.T, _ *Service, _ *probeProviders, st *Store, p Plan) {
			mutateHikyo(t, st, func(st *State, _ *[]Resource) { p.ExpiresAt = time.Now().Add(-time.Second); st.Plans[p.ID] = p })
		}},
		{"active-operation", func(t *testing.T, _ *Service, _ *probeProviders, st *Store, _ Plan) {
			mutateHikyo(t, st, func(st *State, _ *[]Resource) {
				st.Operations["other"] = Operation{OPNsenseID: "demo-edge", Status: "running"}
			})
		}},
		{"ownership", func(t *testing.T, _ *Service, _ *probeProviders, st *Store, _ Plan) {
			mutateHikyo(t, st, func(st *State, _ *[]Resource) {
				st.Owned["other"] = Ownership{OPNsenseID: "demo-edge", Resources: []OwnedResource{{Kind: "handle", ID: hikyoHandle}}}
			})
		}},
		{"container-identity", func(_ *testing.T, _ *Service, p *probeProviders, _ *Store, _ Plan) {
			old := p.inventory
			p.inventory = func(i Inventory) Inventory {
				i = old(i)
				for n := range i.Containers {
					if i.Containers[n].VMID == 609 {
						i.Containers[n].Identity = "new-generation"
					}
				}
				return i
			}
		}},
		{"container-fingerprint", func(_ *testing.T, _ *Service, p *probeProviders, _ *Store, _ Plan) {
			old := p.inventory
			p.inventory = func(i Inventory) Inventory {
				i = old(i)
				for n := range i.Containers {
					if i.Containers[n].VMID == 609 {
						i.Containers[n].Fingerprint = "changed-config"
					}
				}
				return i
			}
		}},
		{"incomplete-inventory", func(_ *testing.T, _ *Service, p *probeProviders, _ *Store, _ Plan) {
			old := p.inventory
			p.inventory = func(i Inventory) Inventory { i = old(i); i.Complete = false; return i }
		}},
		{"unavailable-provider", func(_ *testing.T, _ *Service, p *probeProviders, _ *Store, _ Plan) { p.readFail = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, p, store := hikyoService(t)
			plan, err := s.Plan(context.Background(), PlanRequest{Action: "import", HostID: "main", VMID: 609})
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(t, s, p, store, plan)
			if _, err = s.ApplyPlan(context.Background(), plan.ID); err == nil {
				t.Fatal("stale import accepted")
			}
			if len(p.events) != 0 {
				t.Fatal("stale import wrote providers", p.events)
			}
			if _, exists := store.Read().Owned["main:609"]; exists {
				t.Fatal("stale import transferred ownership")
			}
			if len(store.Read().Operations) != 0 && tc.name != "active-operation" {
				t.Fatal("stale import created operation")
			}
		})
	}
}

func TestExistingImportDurableResponseLossAndPersistenceFailure(t *testing.T) {
	t.Run("response-loss-restart", func(t *testing.T) {
		s, p, store := hikyoService(t)
		plan, err := s.Plan(context.Background(), PlanRequest{Action: "import", HostID: "main", VMID: 609})
		if err != nil {
			t.Fatal(err)
		}
		op, err := s.ApplyPlan(context.Background(), plan.ID)
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
		if err = store.Close(); err != nil {
			t.Fatal(err)
		}
		next, err := OpenStore(filepath.Dir(store.path), true)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { next.Close() })
		np := &probeProviders{DemoProviders: &DemoProviders{Store: next}, inventory: p.inventory}
		ns := NewService(next, np)
		t.Cleanup(ns.Close)
		if next.Read().Operations[op.ID].Status != "succeeded" || next.Read().Owned["main:609"].RouteLayout.Parent.ID != hikyoParent {
			t.Fatal("import not durable")
		}
		if _, err = ns.ApplyPlan(context.Background(), plan.ID); err == nil {
			t.Fatal("lost response enabled replay")
		}
		if len(np.events) != 0 {
			t.Fatal("restart reapplied provider writes", np.events)
		}
	})
	t.Run("durable-failure", func(t *testing.T) {
		s, p, store := hikyoService(t)
		plan, err := s.Plan(context.Background(), PlanRequest{Action: "import", HostID: "main", VMID: 609})
		if err != nil {
			t.Fatal(err)
		}
		path := store.path
		// A directory cannot be atomically replaced with the state file. Force a
		// persistence failure without depending on permissions (tests may run root).
		store.path = filepath.Join(t.TempDir(), "state-as-directory")
		if err = os.Mkdir(store.path, 0700); err != nil {
			t.Fatal(err)
		}
		op, err := s.ApplyPlan(context.Background(), plan.ID)
		store.path = path
		if err == nil || op.Status == "succeeded" {
			t.Fatal("failed transaction reported success", op, err)
		}
		st := store.Read()
		if len(st.Owned) != 0 || len(st.Operations) != 0 || len(st.Plans) != 1 {
			t.Fatal("partial durable import state", st.Owned, st.Operations)
		}
		if _, err = s.ApplyPlan(context.Background(), plan.ID); err == nil {
			t.Fatal("poisoned store accepted replay")
		}
		if len(p.events) != 0 {
			t.Fatal("persistence failure wrote providers", p.events)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var disk State
		if err = json.Unmarshal(raw, &disk); err != nil {
			t.Fatal(err)
		}
		if len(disk.Owned) != 0 || len(disk.Operations) != 0 || len(disk.Plans) != 1 {
			t.Fatal("partial disk transaction")
		}
	})
}

func TestExistingImportUsesExistingPlanApplyAPI(t *testing.T) {
	s, p, _ := hikyoService(t)
	auth, err := NewAuth(context.Background(), AuthConfig{Demo: true})
	if err != nil {
		t.Fatal(err)
	}
	api := NewAPI(s, auth, nil)
	call := func(path, body string, want int) []byte {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s got %d want %d: %s", path, rec.Code, want, rec.Body.String())
		}
		return rec.Body.Bytes()
	}
	var plan Plan
	if err = json.Unmarshal(call("/api/v1/plans", `{"action":"import","hostId":"main","vmid":609}`, 201), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Action != "import" || !validRoute(plan.DesiredRoute) {
		t.Fatal("nonstandard import plan", plan)
	}
	var op Operation
	if err = json.Unmarshal(call("/api/v1/plans/"+plan.ID+"/apply", `{}`, 200), &op); err != nil {
		t.Fatal(err)
	}
	if op.Status != "succeeded" || op.PlanID != plan.ID || op.Action != "import" {
		t.Fatal("nonstandard import operation", op)
	}
	call("/api/v1/plans/"+plan.ID+"/apply", `{}`, 404)
	if len(p.events) != 0 {
		t.Fatal("API import wrote provider state", p.events)
	}
}

func TestExistingImportCannotFlattenSharedStandaloneParent(t *testing.T) {
	s, _, store := hikyoService(t)
	mutateHikyo(t, store, func(_ *State, r *[]Resource) {
		for i := range *r {
			if (*r)[i].ID == hikyoChild {
				(*r)[i].Kind = "domain"
				delete((*r)[i].Fields, "reverse")
			}
		}
		changeHikyo(*r, hikyoHandle, "reverse", hikyoChild)
		changeHikyo(*r, hikyoHandle, "subdomain", "")
		*r = append(*r, Resource{ID: "11111111-2222-4333-8444-555555555555", Kind: "subdomain", Fields: map[string]string{"enabled": "1", "reverse": hikyoChild, "FromDomain": "another.went.io", "accesslist": ""}})
	})
	item := hikyoItem(t, s)
	if item.ImportAvailable || item.ExistingRoute != nil || !strings.Contains(strings.Join(item.ImportIssues, ";"), "shared parent") {
		t.Fatal(item)
	}
	if _, err := s.Plan(context.Background(), PlanRequest{Action: "import", HostID: "main", VMID: 609}); err == nil {
		t.Fatal("imported shared parent")
	}
}

func TestExistingChildDomainBlocksEnsureWithoutMatchingReservation(t *testing.T) {
	s, p, store := hikyoService(t)
	mutateHikyo(t, store, func(_ *State, r *[]Resource) {
		for i, x := range *r {
			if x.ID == hikyoReservation {
				*r = append((*r)[:i], (*r)[i+1:]...)
				break
			}
		}
	})
	if _, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 609, Public: true}); err == nil || !strings.Contains(err.Error(), "subdomain "+hikyoChild) {
		t.Fatal("native collision missed", err)
	}
	if len(p.events) != 0 {
		t.Fatal("ensure wrote over native child", p.events)
	}
}

func TestExistingConcurrentImportConsumesPlanExactlyOnce(t *testing.T) {
	s, p, store := hikyoService(t)
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "import", HostID: "main", VMID: 609})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for n := 0; n < 2; n++ {
		go func() { <-start; _, err := s.ApplyPlan(context.Background(), plan.ID); results <- err }()
	}
	close(start)
	successes := 0
	for n := 0; n < 2; n++ {
		if <-results == nil {
			successes++
		}
	}
	st := store.Read()
	if successes != 1 || len(st.Owned) != 1 || len(st.Operations) != 1 || len(st.Plans) != 0 || len(p.events) != 0 {
		t.Fatal("non-atomic concurrent import", successes, len(st.Owned), len(st.Operations), len(st.Plans), p.events)
	}
}
