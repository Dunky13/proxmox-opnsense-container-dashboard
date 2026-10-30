package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// All provider reads and writes in this fixture cross the real HTTP adapters.
// The TLS peer is exclusively this test server; no infrastructure is contacted.
type cleanupHTTPFixture struct {
	mu          sync.Mutex
	rows        []Resource
	events      []string
	occupied    bool
	reuseCT     bool
	incomplete  bool
	failDelete  bool
	loseDelete  bool
	failService string
	afterEvent  func(string)
}

func (f *cleanupHTTPFixture) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	path := r.URL.Path
	if strings.HasPrefix(path, "/api/caddy/reverse_proxy/get_header/") {
		writeJSON(w, 200, map[string]any{"header": map[string]string{"HeaderType": "X-Forwarded-For"}})
		return
	}
	if strings.HasPrefix(path, "/api2/json/") {
		var data any
		switch strings.TrimPrefix(path, "/api2/json") {
		case "/access/permissions":
			data = map[string]any{"/": map[string]int{"VM.Audit": 1}}
		case "/nodes":
			data = []any{map[string]any{"node": "main", "status": "online"}, map[string]any{"node": "pvedell", "status": "online"}}
		case "/cluster/status":
			data = []any{map[string]any{"type": "cluster", "quorate": 1}}
		case "/cluster/resources":
			if f.incomplete {
				http.Error(w, "incomplete", 500)
				return
			}
			data = []any{}
			if f.occupied {
				data = []any{map[string]any{"vmid": 609, "type": "qemu", "node": "unconfigured-node", "status": "stopped"}}
			}
			if f.reuseCT {
				data = []any{map[string]any{"vmid": 612, "type": "lxc", "node": "main", "name": "reused-address", "status": "stopped"}}
			}
		case "/nodes/main/lxc/612/config":
			data = map[string]any{"hostname": "reused-address", "ostype": "debian", "digest": "reuse-digest", "net0": "name=eth0,hwaddr=D0:CC:37:00:06:09,ip=192.168.6.9/24", "rootfs": "local:vm-612-disk-0,size=8G", "description": "container-dashboard-generation=12345678-1111-4111-8111-123456789612"}
		default:
			t.Errorf("unexpected PVE endpoint %s", path)
		}
		writeJSON(w, 200, map[string]any{"data": data})
		return
	}
	for _, kind := range []string{"reservation", "domain", "subdomain", "handle", "acl", "subnet"} {
		ep, _ := resourceEndpoint(kind)
		prefix := "/api/" + ep.Controller + "/"
		if path == prefix+"search_"+ep.Command {
			rows := []any{}
			for _, row := range f.rows {
				if row.Kind == kind {
					rows = append(rows, map[string]any{"uuid": row.ID})
				}
			}
			writeJSON(w, 200, map[string]any{"total": len(rows), "rows": rows})
			return
		}
		for i, row := range f.rows {
			if row.Kind != kind {
				continue
			}
			if path == prefix+"get_"+ep.Command+"/"+row.ID {
				writeJSON(w, 200, map[string]any{ep.Wrapper: row.Fields})
				return
			}
			if path == prefix+"del_"+ep.Command+"/"+row.ID {
				if r.Method != "POST" {
					t.Error("delete must be POST")
				}
				event := "delete:" + kind
				f.events = append(f.events, event)
				if !f.failDelete || f.loseDelete {
					f.rows = append(f.rows[:i], f.rows[i+1:]...)
				}
				if f.afterEvent != nil {
					f.afterEvent(event)
				}
				if f.failDelete {
					http.Error(w, "lost or failed delete response", 500)
					return
				}
				writeJSON(w, 200, map[string]string{"result": "deleted"})
				return
			}
		}
	}
	switch path {
	case "/api/caddy/general/get":
		for _, r := range f.rows {
			if r.Kind == "caddyGeneral" {
				writeJSON(w, 200, map[string]any{"caddy": map[string]any{"general": r.Fields}})
				return
			}
		}
	case "/api/kea/dhcpv4/get":
		writeJSON(w, 200, map[string]any{"dhcpv4": map[string]any{"general": map[string]string{"enabled": "1"}}})
		return
	case "/api/kea/service/status", "/api/caddy/service/status":
		writeJSON(w, 200, map[string]string{"status": "running"})
		return
	case "/api/caddy/service/reconfigure", "/api/kea/service/reconfigure":
		service := "caddy"
		if strings.Contains(path, "/kea/") {
			service = "kea"
		}
		event := "apply:" + service
		f.events = append(f.events, event)
		if f.afterEvent != nil {
			f.afterEvent(event)
		}
		if f.failService == service {
			http.Error(w, "service failure", 500)
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ok"})
		return
	}
	t.Errorf("unexpected provider endpoint %s %s", r.Method, path)
	http.NotFound(w, r)
}
func externalHTTPService(t *testing.T) (*Service, *cleanupHTTPFixture, *Store) {
	t.Helper()
	s, _, st := hikyoService(t)
	f := &cleanupHTTPFixture{rows: clone(st.Read().DemoResources["demo-edge"])}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.serve(t, w, r) }))
	t.Cleanup(server.Close)
	if err := st.Update(func(next *State) error {
		for i := range next.Config.Hosts {
			h := &next.Config.Hosts[i]
			h.BaseURL = server.URL
			h.CAPEM = trustServer(server)
			h.TokenID = "test@pve!audit"
			h.TokenSecret = "synthetic"
			h.Node = h.ID
			h.SSH.Enabled = false
		}
		o := &next.Config.OPNsenses[0]
		o.BaseURL = server.URL
		o.CAPEM = trustServer(server)
		o.APIKey = "synthetic"
		o.APISecret = "synthetic"
		o.CertificateID = ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s.Providers = &RealProviders{SSH: sshRunFunc(func(context.Context, Host, int, string) (RunResult, error) {
		return RunResult{}, errors.New("test forbids SSH")
	})}
	return s, f, st
}
func httpExternalPlan(t *testing.T, s *Service, ids ...string) Plan {
	t.Helper()
	st := s.Store.Read()
	h, _ := st.Config.Host("main")
	o := routerForHost(st.Config, h)
	snap, err := s.Providers.Snapshot(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	refs := []OwnedResource{}
	for _, id := range ids {
		for _, r := range snap.Resources {
			if r.ID == id {
				refs = append(refs, OwnedResource{ID: r.ID, Kind: r.Kind, Fingerprint: r.Fingerprint()})
			}
		}
	}
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "externalCleanup", HostID: "main", VMID: 609, SelectedResources: refs, ExternalUseAcknowledged: true})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
func recoveryRequest(op Operation) PlanRequest {
	return PlanRequest{Action: "externalCleanup", HostID: "main", VMID: 609, RecoveryID: op.ID, SelectedResources: op.ExternalCleanup.Targets, ExternalUseAcknowledged: true}
}
func TestExternalCleanupHTTPOrderingAndNoOwnership(t *testing.T) {
	s, f, st := externalHTTPService(t)
	plan := httpExternalPlan(t, s, hikyoReservation, hikyoChild, hikyoHandle)
	op, err := s.ApplyPlan(context.Background(), plan.ID)
	if err != nil || op.Status != "succeeded" {
		t.Fatal(op, err)
	}
	want := []string{"delete:handle", "delete:subdomain", "apply:caddy", "delete:reservation", "apply:kea"}
	if fingerprint(f.events) != fingerprint(want) {
		t.Fatalf("unsafe provider sequence %v", f.events)
	}
	if len(st.Read().Owned) != 0 || len(st.Read().ExternalCleanups) != 1 {
		t.Fatal("external authority entered ownership or lost evidence")
	}
}
func TestExternalCleanupHTTPPartialRecovery(t *testing.T) {
	for _, failure := range []string{"delete-not-sent", "delete-lost-response", "caddy", "kea"} {
		t.Run(failure, func(t *testing.T) {
			s, f, st := externalHTTPService(t)
			plan := httpExternalPlan(t, s, hikyoReservation, hikyoChild, hikyoHandle)
			if strings.HasPrefix(failure, "delete") {
				f.failDelete = true
				f.loseDelete = failure == "delete-lost-response"
			} else {
				f.failService = failure
			}
			op, err := s.ApplyPlan(context.Background(), plan.ID)
			if err == nil || op.Status != "partial" || len(st.Read().Owned) != 0 {
				t.Fatal(op, err)
			}
			before := len(f.events)
			if _, err = s.ApplyPlan(context.Background(), plan.ID); err == nil {
				t.Fatal("lost response retried original plan")
			}
			if _, err = s.Recover(context.Background(), op.ID); err == nil {
				t.Fatal("generic owned recovery accepted external journal")
			}
			if before != len(f.events) {
				t.Fatal("blocked request wrote provider")
			}
			if failure == "caddy" {
				for _, e := range f.events {
					if e == "delete:reservation" {
						t.Fatal("released reservation before successful Caddy removal")
					}
				}
			}
			f.failDelete = false
			f.failService = ""
			fresh, err := s.Plan(context.Background(), recoveryRequest(op))
			if err != nil {
				t.Fatal(err)
			}
			resumed, err := s.ApplyPlan(context.Background(), fresh.ID)
			if err != nil || resumed.Status != "succeeded" || resumed.ID != op.ID {
				t.Fatal(resumed, err)
			}
			if failure == "delete-lost-response" {
				n := 0
				for _, e := range f.events {
					if e == "delete:handle" {
						n++
					}
				}
				if n != 1 {
					t.Fatal("retried absent UUID", f.events)
				}
			}
			if len(st.Read().ExternalCleanups[op.ID].PlanIDs) != 2 || len(st.Read().Owned) != 0 {
				t.Fatal("review or deletion evidence lost")
			}
		})
	}
}
func TestExternalCleanupHTTPFreshGuardsAndFailedRecovery(t *testing.T) {
	for _, change := range []string{"new-reference", "address-reuse", "observed-ct-address-reuse", "changed-policy", "reappeared-uuid", "incomplete-occupancy"} {
		t.Run(change, func(t *testing.T) {
			s, f, st := externalHTTPService(t)
			plan := httpExternalPlan(t, s, hikyoReservation, hikyoChild, hikyoHandle)
			f.failService = "caddy"
			op, err := s.ApplyPlan(context.Background(), plan.ID)
			if err == nil || op.Status != "partial" {
				t.Fatal(op, err)
			}
			f.failService = ""
			switch change {
			case "new-reference":
				f.rows = append(f.rows, Resource{ID: "c1111111-1111-4111-8111-111111111111", Kind: "handle", Fields: map[string]string{"ToDomain": "192.168.6.9"}})
			case "address-reuse":
				f.occupied = true
			case "observed-ct-address-reuse":
				f.reuseCT = true
			case "changed-policy":
				for i := range f.rows {
					if f.rows[i].Kind == "caddyGeneral" {
						f.rows[i].Fields["ProxyProtocolAllow"] = "192.0.2.0/24"
					}
				}
			case "reappeared-uuid":
				for _, r := range st.Read().ExternalCleanups[op.ID].Baseline.Resources {
					if r.ID == hikyoHandle {
						f.rows = append(f.rows, r)
					}
				}
			case "incomplete-occupancy":
				f.incomplete = true
			}
			before := len(f.events)
			if _, err = s.Plan(context.Background(), recoveryRequest(op)); err == nil {
				t.Fatal("unsafe recovery plan accepted", change)
			}
			if before != len(f.events) || st.Read().Operations[op.ID].Status == "succeeded" || len(st.Read().Owned) != 0 {
				t.Fatal("failed recovery mutated or claimed success")
			}
		})
	}
}
func TestExternalCleanupHTTPGuardBeforeNextDeleteAndApply(t *testing.T) {
	for _, trigger := range []string{"delete:handle", "apply:caddy"} {
		t.Run(trigger, func(t *testing.T) {
			s, f, _ := externalHTTPService(t)
			plan := httpExternalPlan(t, s, hikyoReservation, hikyoChild, hikyoHandle)
			f.afterEvent = func(event string) {
				if event == trigger {
					f.occupied = true
				}
			}
			op, err := s.ApplyPlan(context.Background(), plan.ID)
			if err == nil || op.Status != "partial" {
				t.Fatal("mid-workflow reuse ignored", op, err)
			}
			for _, e := range f.events {
				if e == "delete:reservation" || e == "apply:kea" {
					t.Fatal("released reused address", f.events)
				}
			}
		})
	}
}
func TestExternalCleanupRejectsStaleExpiryAndExactBindings(t *testing.T) {
	for _, change := range []string{"fingerprint", "new-reference", "uuid", "occupancy", "expired", "config"} {
		t.Run(change, func(t *testing.T) {
			s, f, st := externalHTTPService(t)
			p := httpExternalPlan(t, s, hikyoReservation, hikyoChild, hikyoHandle)
			switch change {
			case "fingerprint":
				for i := range f.rows {
					if f.rows[i].ID == hikyoHandle {
						f.rows[i].Fields["ToPort"] = "9090"
					}
				}
			case "new-reference":
				f.rows = append(f.rows, Resource{ID: "c1111111-1111-4111-8111-111111111111", Kind: "handle", Fields: map[string]string{"ToDomain": "192.168.6.9"}})
			case "uuid":
				for i := range f.rows {
					if f.rows[i].ID == hikyoHandle {
						f.rows[i].ID = "c1111111-1111-4111-8111-111111111111"
					}
				}
			case "occupancy":
				f.occupied = true
			case "expired":
				_ = st.Update(func(next *State) error {
					q := next.Plans[p.ID]
					q.ExpiresAt = time.Now().Add(-time.Second)
					next.Plans[p.ID] = q
					return nil
				})
			case "config":
				_ = st.Update(func(next *State) error { next.Config.Revision++; return nil })
			}
			if _, err := s.ApplyPlan(context.Background(), p.ID); err == nil || len(f.events) != 0 {
				t.Fatal("stale plan wrote", change, err, f.events)
			}
		})
	}
}

func TestExternalCleanupRestartRetainsPrivateDeletionEvidence(t *testing.T) {
	s, f, st := externalHTTPService(t)
	p := httpExternalPlan(t, s, hikyoReservation, hikyoChild, hikyoHandle)
	f.failDelete = true
	f.loseDelete = true
	op, err := s.ApplyPlan(context.Background(), p.ID)
	if err == nil || op.Status != "partial" {
		t.Fatal(op, err)
	}
	path := filepath.Dir(st.path)
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	s.Store = reopened
	if len(reopened.Read().Owned) != 0 || len(reopened.Read().ExternalCleanups[op.ID].Baseline.Resources) == 0 {
		t.Fatal("restart lost external evidence")
	}
	before := len(f.events)
	if _, err = s.Recover(context.Background(), op.ID); err == nil || before != len(f.events) {
		t.Fatal("restart admitted generic recovery")
	}
	f.failDelete = false
	fresh, err := s.Plan(context.Background(), recoveryRequest(op))
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := s.ApplyPlan(context.Background(), fresh.ID)
	if err != nil || recovered.Status != "succeeded" {
		t.Fatal(recovered, err)
	}
	raw, _ := json.Marshal(s.Operations())
	if strings.Contains(string(raw), "baseline") || strings.Contains(string(raw), "basicauth") {
		t.Fatal("private evidence reached Activity")
	}
}

func addExternalForeignRouter(t *testing.T, s *Service, rows []Resource) *cleanupHTTPFixture {
	t.Helper()
	f := &cleanupHTTPFixture{rows: clone(rows)}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.serve(t, w, r) }))
	t.Cleanup(server.Close)
	if err := s.Store.Update(func(st *State) error {
		o := clone(st.Config.OPNsenses[0])
		o.ID = "other-firewall"
		o.BaseURL = server.URL
		o.CAPEM = trustServer(server)
		st.Config.OPNsenses = append(st.Config.OPNsenses, o)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return f
}
func TestExternalCleanupHTTPRetainedHandleOnAnotherFirewall(t *testing.T) {
	s, f, _ := externalHTTPService(t)
	foreign := []Resource{}
	for _, r := range f.rows {
		if r.ID != hikyoReservation && r.ID != hikyoChild && r.ID != hikyoHandle {
			foreign = append(foreign, r)
		}
	}
	foreign = append(foreign, Resource{ID: "c1111111-1111-4111-8111-111111111111", Kind: "handle", Fields: map[string]string{"ToDomain": "192.168.6.9"}})
	other := addExternalForeignRouter(t, s, foreign)
	req := externalRequest(t, s, []string{hikyoReservation, hikyoChild, hikyoHandle}, true)
	if _, err := s.Plan(context.Background(), req); err == nil {
		t.Fatal("reservation cleanup ignored retained handle on another configured firewall")
	}
	if len(f.events) != 0 || len(other.events) != 0 {
		t.Fatal("protected cross-firewall reference wrote provider")
	}
}

func TestExternalCleanupHTTPOtherFirewallFreshGuards(t *testing.T) {
	for _, phase := range []string{"apply-admission", "between-deletes", "recovery"} {
		t.Run(phase, func(t *testing.T) {
			s, f, _ := externalHTTPService(t)
			rows := []Resource{}
			for _, r := range f.rows {
				if r.ID != hikyoReservation && r.ID != hikyoChild && r.ID != hikyoHandle {
					rows = append(rows, r)
				}
			}
			other := addExternalForeignRouter(t, s, rows)
			p := httpExternalPlan(t, s, hikyoReservation, hikyoChild, hikyoHandle)
			addReference := func() {
				other.mu.Lock()
				defer other.mu.Unlock()
				other.rows = append(other.rows, Resource{ID: "c1111111-1111-4111-8111-111111111111", Kind: "handle", Fields: map[string]string{"ToDomain": "192.168.6.9"}})
			}
			if phase == "apply-admission" {
				addReference()
			}
			if phase == "between-deletes" {
				f.afterEvent = func(event string) {
					if event == "delete:handle" {
						addReference()
					}
				}
			}
			if phase == "recovery" {
				f.failService = "caddy"
			}
			op, err := s.ApplyPlan(context.Background(), p.ID)
			if err == nil {
				t.Fatal("other firewall reference was ignored", op)
			}
			for _, event := range f.events {
				if event == "delete:reservation" || event == "apply:kea" {
					t.Fatal("released referenced address", f.events)
				}
			}
			if phase == "recovery" {
				addReference()
				f.failService = ""
				if _, err = s.Plan(context.Background(), recoveryRequest(op)); err == nil {
					t.Fatal("recovery ignored new cross-firewall reference")
				}
			}
			if len(other.events) != 0 {
				t.Fatal("external context must remain read-only")
			}
		})
	}
}
