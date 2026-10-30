package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// Providers is the external I/O boundary. Counts measure work avoided, while
// responses assert observable freshness, guards and recovery behavior.
type displayTestProviders struct {
	Providers
	inventories atomic.Int64
	snapshots   atomic.Int64
	inventory   func(context.Context, Config, int64) Inventory
	snapshot    func(context.Context, OPNsense, int64) (ProviderSnapshot, error)
}

func (p *displayTestProviders) Inventory(ctx context.Context, cfg Config) Inventory {
	n := p.inventories.Add(1)
	if p.inventory != nil {
		return p.inventory(ctx, cfg, n)
	}
	return p.Providers.Inventory(ctx, cfg)
}

func (p *displayTestProviders) Snapshot(ctx context.Context, o OPNsense) (ProviderSnapshot, error) {
	n := p.snapshots.Add(1)
	if p.snapshot != nil {
		return p.snapshot(ctx, o, n)
	}
	return p.Providers.Snapshot(ctx, o)
}

// Display-only fixture auth avoids a periodic session-store goroutine inside
// synthetic time. The real authenticated HTTP lifecycle is tested separately
// in provider_http_test.go and auth_test.go.
type displayTestAuth struct{ *Auth }

func (displayTestAuth) Middleware(next http.Handler) http.Handler { return next }

func displayTestAPI(t *testing.T) (*Service, *displayTestProviders, *Store, http.Handler) {
	t.Helper()
	s, original, store := demoService(t)
	p := &displayTestProviders{Providers: original}
	s.Providers = p
	auth := displayTestAuth{&Auth{config: AuthConfig{Demo: true}}}
	return s, p, store, NewAPI(s, auth, nil)
}

func displayGET[T any](t *testing.T, api http.Handler, ctx context.Context, path string) T {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/"+path, nil).WithContext(ctx)
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	var out T
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("%s: HTTP %d %s", path, w.Code, w.Body.String())
	}
	return out
}

func displayItem(t *testing.T, out Reconciliation, key string) ReconcileItem {
	t.Helper()
	for _, item := range out.Items {
		if item.Key == key {
			return item
		}
	}
	t.Fatalf("missing reconciliation item %s", key)
	return ReconcileItem{}
}

func TestDisplayCancelledWaiterAndConcurrentReloadShareWork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, p, _, api := displayTestAPI(t)
		defer s.Close()
		started, release := make(chan struct{}), make(chan struct{})
		p.inventory = func(ctx context.Context, cfg Config, n int64) Inventory {
			if n == 1 {
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
					return Inventory{}
				}
			}
			return p.Providers.Inventory(ctx, cfg)
		}
		ctx, cancel := context.WithCancel(context.Background())
		first := make(chan Reconciliation, 1)
		go func() { first <- displayGET[Reconciliation](t, api, ctx, "reconciliation") }()
		<-started
		cancel()
		if out := <-first; out.Inventory.Complete {
			t.Fatal("cancelled waiter received current inventory")
		}
		second := make(chan Reconciliation, 1)
		go func() { second <- displayGET[Reconciliation](t, api, context.Background(), "reconciliation") }()
		synctest.Wait()
		if p.inventories.Load() != 1 {
			t.Fatalf("reload restarted a disconnected caller's useful read: %d", p.inventories.Load())
		}
		close(release)
		out := <-second
		inv := displayGET[Inventory](t, api, context.Background(), "inventory")
		if !out.Inventory.Complete || !inv.Complete || !inv.ObservedAt.Equal(out.Inventory.ObservedAt) || p.inventories.Load() != 1 || p.snapshots.Load() != 1 {
			t.Fatalf("read reuse/freshness failed: inventories=%d snapshots=%d", p.inventories.Load(), p.snapshots.Load())
		}
	})
}

func TestDisplayTTLRetainsObservationTimeAndDiscoversExternalChanges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, p, _, api := displayTestAPI(t)
		defer s.Close()
		var changed atomic.Bool
		p.inventory = func(ctx context.Context, cfg Config, _ int64) Inventory {
			inv := p.Providers.Inventory(ctx, cfg)
			if changed.Load() {
				inv.Containers[0].Name = "externally-renamed"
			}
			return inv
		}
		p.snapshot = func(ctx context.Context, o OPNsense, _ int64) (ProviderSnapshot, error) {
			snap, err := p.Providers.Snapshot(ctx, o)
			if changed.Load() {
				snap.Resources = append(snap.Resources, Resource{ID: fixtureResourceID(99), Kind: "domain", Fields: map[string]string{"FromDomain": "immich.went.io"}})
			}
			return snap, err
		}
		first := displayGET[Reconciliation](t, api, context.Background(), "reconciliation")
		changed.Store(true)
		time.Sleep(14 * time.Second)
		warm := displayGET[Reconciliation](t, api, context.Background(), "reconciliation")
		if !warm.Inventory.ObservedAt.Equal(first.Inventory.ObservedAt) || p.inventories.Load() != 1 || p.snapshots.Load() != 1 {
			t.Fatal("cache hit refreshed observation time or repeated work")
		}
		time.Sleep(time.Second)
		fresh := displayGET[Reconciliation](t, api, context.Background(), "reconciliation")
		if !fresh.Inventory.ObservedAt.After(first.Inventory.ObservedAt) || fresh.Inventory.Containers[0].Name != "externally-renamed" || displayItem(t, fresh, "main:604").State != "conflict" || p.inventories.Load() != 2 || p.snapshots.Load() != 2 {
			t.Fatalf("expired observations hid external changes: inventories=%d snapshots=%d", p.inventories.Load(), p.snapshots.Load())
		}
	})
}

func TestDisplayUncertainObservationsAreNotCached(t *testing.T) {
	for _, failure := range []string{"inventory", "host", "old", "future", "snapshot", "prerequisite", "issues"} {
		t.Run(failure, func(t *testing.T) {
			_, p, _, api := displayTestAPI(t)
			p.inventory = func(ctx context.Context, cfg Config, n int64) Inventory {
				inv := p.Providers.Inventory(ctx, cfg)
				if n == 1 {
					switch failure {
					case "inventory":
						inv.Complete = false
					case "host":
						inv.Hosts[0].Complete = false
					case "old":
						inv.ObservedAt = time.Now().Add(-time.Minute)
					case "future":
						inv.ObservedAt = time.Now().Add(time.Minute)
					}
				}
				return inv
			}
			p.snapshot = func(ctx context.Context, o OPNsense, n int64) (ProviderSnapshot, error) {
				snap, err := p.Providers.Snapshot(ctx, o)
				if n == 1 {
					switch failure {
					case "snapshot":
						return ProviderSnapshot{}, unavailable("fixture failure")
					case "prerequisite":
						snap.Ready = false
					case "issues":
						snap.Issues = []string{"prerequisites uncertain"}
					}
				}
				return snap, err
			}
			first := displayGET[Reconciliation](t, api, context.Background(), "reconciliation")
			if (failure == "inventory" || failure == "snapshot") && displayItem(t, first, "main:604").State != "unknown" {
				t.Fatal("failed observation asserted known state")
			}
			second := displayGET[Reconciliation](t, api, context.Background(), "reconciliation")
			if !second.Inventory.Complete || displayItem(t, second, "main:604").State != "missing" {
				t.Fatal("failed observation prevented subsequent successful read")
			}
			displayGET[Reconciliation](t, api, context.Background(), "reconciliation")
			wantInv, wantSnap := int64(2), int64(1)
			if failure == "snapshot" || failure == "prerequisite" || failure == "issues" {
				wantInv, wantSnap = 1, 2
			}
			if p.inventories.Load() != wantInv || p.snapshots.Load() != wantSnap {
				t.Fatalf("uncertain result cached or shared failure retried per host: inventories=%d snapshots=%d", p.inventories.Load(), p.snapshots.Load())
			}
		})
	}
}

func TestDisplayStateChangesInvalidateAndKeepCurrentOperationGuards(t *testing.T) {
	_, p, store, api := displayTestAPI(t)
	displayGET[Reconciliation](t, api, context.Background(), "reconciliation")
	for _, change := range []string{"ownership", "operations", "jobs", "failedStateUpdate"} {
		err := store.Update(func(st *State) error {
			switch change {
			case "ownership":
				st.Owned["main:604"] = Ownership{Key: "main:604", HostID: "main", VMID: 604, DesiredRoute: RouteConfig{Scheme: "https", Port: 8443}}
			case "operations":
				st.Operations["pending"] = Operation{ID: "pending", Key: "main:604", OPNsenseID: "demo-edge", Status: "partial"}
			case "jobs":
				st.Jobs["job"] = Job{ID: "job", Key: "main:604", Status: "interrupted"}
			case "failedStateUpdate":
				return unavailable("fixture failed state update")
			}
			return nil
		})
		if err != nil && change != "failedStateUpdate" {
			t.Fatal(err)
		}
		beforeInv, beforeSnap := p.inventories.Load(), p.snapshots.Load()
		out := displayGET[Reconciliation](t, api, context.Background(), "reconciliation")
		item := displayItem(t, out, "main:604")
		if !item.Owned || item.DesiredRoute == nil || item.DesiredRoute.Port != 8443 || p.inventories.Load() != beforeInv+1 || p.snapshots.Load() != beforeSnap+1 {
			t.Fatalf("%s did not invalidate display authority", change)
		}
		if change != "ownership" && (len(out.Operations) != 1 || !strings.Contains(strings.Join(item.Issues, " "), "recovery required")) {
			t.Fatal("cached reconciliation hid an unfinished operation")
		}
	}
}

func TestDisplayPersistenceUncertaintyWithdrawsAuthority(t *testing.T) {
	_, p, store, api := displayTestAPI(t)
	displayGET[Reconciliation](t, api, context.Background(), "reconciliation")
	// An existing directory cannot be replaced with the atomic state-file
	// rename. This fails persistence deterministically without chmod assumptions.
	store.path = t.TempDir()
	if err := store.Update(func(st *State) error {
		st.Operations["uncertain"] = Operation{ID: "uncertain", Key: "main:604", Status: "running"}
		return nil
	}); err == nil {
		t.Fatal("fixture persistence unexpectedly succeeded")
	}
	out := displayGET[Reconciliation](t, api, context.Background(), "reconciliation")
	if out.Inventory.Complete {
		t.Fatal("uncertain persistence retained display authority")
	}
	for _, item := range out.Items {
		if item.State != "unknown" {
			t.Fatal("uncertain state retained reconciliation authority")
		}
	}
	for _, h := range out.Inventory.Hosts {
		if h.NextVMID != nil || h.Complete {
			t.Fatal("uncertain state retained allocation authority")
		}
	}
	if p.inventories.Load() != 1 || p.snapshots.Load() != 1 {
		t.Fatal("provider reads cannot repair uncertain durable state")
	}
}

func TestDisplayInvalidatedInflightReadJoinsBeforeReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, p, _, api := displayTestAPI(t)
		defer s.Close()
		started, release := make(chan struct{}), make(chan struct{})
		p.inventory = func(ctx context.Context, cfg Config, n int64) Inventory {
			inv := p.Providers.Inventory(ctx, cfg)
			if n == 1 {
				close(started)
				// Deliberately deliver a late successful old observation after
				// cancellation, to exercise admission and bounded worker ownership.
				<-release
			} else {
				inv.Containers[0].Name = "after-mutation-attempt"
			}
			return inv
		}
		results := make(chan Reconciliation, 2)
		go func() { results <- displayGET[Reconciliation](t, api, context.Background(), "reconciliation") }()
		<-started
		if _, err := s.ApplyPlan(context.Background(), "missing-plan"); err == nil {
			t.Fatal("missing plan unexpectedly applied")
		}
		go func() { results <- displayGET[Reconciliation](t, api, context.Background(), "reconciliation") }()
		synctest.Wait()
		if p.inventories.Load() != 1 {
			t.Fatal("replacement overlapped an old worker")
		}
		close(release)
		for range 2 {
			out := <-results
			if !out.Inventory.Complete || out.Inventory.Containers[0].Name != "after-mutation-attempt" {
				t.Fatal("pre-mutation observation restored display authority")
			}
		}
		if p.inventories.Load() != 2 || p.snapshots.Load() != 1 {
			t.Fatalf("replacement was not shared: inventories=%d snapshots=%d", p.inventories.Load(), p.snapshots.Load())
		}
	})
}

func TestDisplayWorkDeadlineAndShutdownReleaseWaiters(t *testing.T) {
	for _, end := range []string{"deadline", "shutdown"} {
		t.Run(end, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, p, _, api := displayTestAPI(t)
				defer s.Close()
				started, joined := make(chan struct{}), make(chan struct{})
				p.inventory = func(ctx context.Context, cfg Config, n int64) Inventory {
					if n == 1 {
						close(started)
						<-ctx.Done()
						close(joined)
						return Inventory{}
					}
					return p.Providers.Inventory(ctx, cfg)
				}
				ctx, cancel := context.WithCancel(context.Background())
				first := make(chan Inventory, 1)
				go func() { first <- displayGET[Inventory](t, api, ctx, "inventory") }()
				<-started
				cancel()
				<-first
				if end == "shutdown" {
					s.Close()
				} else {
					time.Sleep(90 * time.Second)
				}
				<-joined
				synctest.Wait()
				out := displayGET[Inventory](t, api, context.Background(), "inventory")
				if end == "shutdown" {
					if out.Complete || p.inventories.Load() != 1 {
						t.Fatal("shutdown allowed another display worker")
					}
				} else if !out.Complete || p.inventories.Load() != 2 {
					t.Fatal("expired flight stranded future callers")
				}
			})
		})
	}
}

func TestDisplaySettingsBoundariesInvalidateObservations(t *testing.T) {
	s, p, store, api := displayTestAPI(t)
	displayGET[Reconciliation](t, api, context.Background(), "reconciliation")
	for _, field := range []string{"revision", "hostToken", "hostAddress", "hostCA", "routerKey", "routerSecret", "routerAddress", "routerCA"} {
		cfg := store.Read().Config
		edit := ConfigEdit{Revision: cfg.Revision}
		for _, h := range cfg.Hosts {
			edit.Hosts = append(edit.Hosts, HostEdit{Host: h})
		}
		for _, o := range cfg.OPNsenses {
			edit.OPNsenses = append(edit.OPNsenses, OPNsenseEdit{OPNsense: o})
		}
		fixtureSecret := "replacement-fixture-credential"
		switch field {
		case "hostToken":
			edit.Hosts[0].Secret = &fixtureSecret
		case "hostAddress":
			edit.Hosts[0].BaseURL = "https://replacement-host.invalid"
		case "hostCA":
			edit.Hosts[0].CAPEM = "changed fixture trust"
		case "routerKey":
			edit.OPNsenses[0].APIKeyValue = &fixtureSecret
		case "routerSecret":
			edit.OPNsenses[0].Secret = &fixtureSecret
		case "routerAddress":
			edit.OPNsenses[0].BaseURL = "https://replacement-router.invalid"
		case "routerCA":
			edit.OPNsenses[0].CAPEM = "changed fixture trust"
		}
		beforeInv, beforeSnap := p.inventories.Load(), p.snapshots.Load()
		if _, err := s.UpdateConfig(edit); err != nil {
			t.Fatalf("%s: %v", field, err)
		}
		displayGET[Reconciliation](t, api, context.Background(), "reconciliation")
		if p.inventories.Load() != beforeInv+1 || p.snapshots.Load() != beforeSnap+1 {
			t.Fatalf("%s reused observations from previous settings", field)
		}
	}
}

func TestDisplayDistinctConfiguredFirewallsStaySeparate(t *testing.T) {
	_, p, store, api := displayTestAPI(t)
	original := store.Read().Config.OPNsenses[0]
	if err := store.Update(func(st *State) error {
		other := original
		other.ID, other.APIKey = "other-edge", "different-fixture-key"
		st.Config.OPNsenses = append(st.Config.OPNsenses, other)
		st.Config.Hosts[1].OPNsenseID = other.ID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p.snapshot = func(ctx context.Context, _ OPNsense, _ int64) (ProviderSnapshot, error) {
		return p.Providers.Snapshot(ctx, original)
	}
	displayGET[Reconciliation](t, api, context.Background(), "reconciliation")
	displayGET[Reconciliation](t, api, context.Background(), "reconciliation")
	if p.inventories.Load() != 1 || p.snapshots.Load() != 2 {
		t.Fatal("distinct configured firewall identities were conflated by address")
	}
}

func TestDisplaySnapshotReloadAndSettingsRace(t *testing.T) {
	for _, changeSettings := range []bool{false, true} {
		t.Run(map[bool]string{false: "disconnectedReload", true: "changedSettings"}[changeSettings], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, p, store, api := displayTestAPI(t)
				defer s.Close()
				started, release := make(chan struct{}), make(chan struct{})
				p.inventory = func(ctx context.Context, cfg Config, _ int64) Inventory {
					inv := p.Providers.Inventory(ctx, cfg)
					inv.Containers[0].Name = cfg.Hosts[0].Name
					return inv
				}
				p.snapshot = func(ctx context.Context, o OPNsense, n int64) (ProviderSnapshot, error) {
					snap, err := p.Providers.Snapshot(ctx, o)
					if n == 1 {
						close(started)
						<-release // Old success must not survive changed credentials.
					}
					return snap, err
				}
				ctx, cancel := context.WithCancel(context.Background())
				first := make(chan Reconciliation, 1)
				go func() { first <- displayGET[Reconciliation](t, api, ctx, "reconciliation") }()
				<-started
				cancel()
				<-first
				if changeSettings {
					cfg := store.Read().Config
					edit := ConfigEdit{Revision: cfg.Revision}
					for _, h := range cfg.Hosts {
						edit.Hosts = append(edit.Hosts, HostEdit{Host: h})
					}
					for _, o := range cfg.OPNsenses {
						edit.OPNsenses = append(edit.OPNsenses, OPNsenseEdit{OPNsense: o})
					}
					secret := "replacement-fixture-secret"
					edit.OPNsenses[0].Secret = &secret
					edit.OPNsenses[0].BaseURL = "https://replacement-router.invalid"
					edit.OPNsenses[0].CAPEM = "replacement fixture trust"
					edit.Hosts[0].Name = "changed settings"
					if _, err := s.UpdateConfig(edit); err != nil {
						t.Fatal(err)
					}
				}
				second := make(chan Reconciliation, 1)
				go func() { second <- displayGET[Reconciliation](t, api, context.Background(), "reconciliation") }()
				synctest.Wait()
				if p.snapshots.Load() != 1 {
					t.Fatal("snapshot replacement overlapped old worker")
				}
				close(release)
				out := <-second
				wantReads := int64(1)
				if changeSettings {
					wantReads = 2
				}
				if !out.Inventory.Complete || out.Inventory.Containers[0].Name != store.Read().Config.Hosts[0].Name || p.inventories.Load() != wantReads || p.snapshots.Load() != wantReads {
					t.Fatalf("snapshot reload/settings boundary failed: inventories=%d snapshots=%d", p.inventories.Load(), p.snapshots.Load())
				}
			})
		})
	}
}

func TestDisplayCacheNeverSuppliesPlanOrJobInputs(t *testing.T) {
	for _, action := range []string{"plan", "job"} {
		t.Run(action, func(t *testing.T) {
			s, p, _, api := displayTestAPI(t)
			var changed atomic.Bool
			p.inventory = func(ctx context.Context, cfg Config, _ int64) Inventory {
				inv := p.Providers.Inventory(ctx, cfg)
				if changed.Load() {
					for i, c := range inv.Containers {
						if c.Key == "main:604" {
							inv.Containers[i].Identity = ""
							inv.Containers[i].Status = "stopped"
						}
					}
				}
				return inv
			}
			warm := displayGET[Reconciliation](t, api, context.Background(), "reconciliation")
			changed.Store(true)
			var err error
			if action == "plan" {
				_, err = s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604})
			} else {
				c, _ := warm.Inventory.Find("main:604")
				_, err = s.StartJob(context.Background(), JobRequest{HostID: "main", VMID: 604, Kind: "appUpdate", Fingerprint: c.Fingerprint})
			}
			if err == nil || p.inventories.Load() != 2 {
				t.Fatalf("%s used old display authority: error=%v inventories=%d", action, err, p.inventories.Load())
			}
		})
	}
}

func TestDisplayCacheNeverSuppliesApplyInputsAndFailureInvalidates(t *testing.T) {
	s, p, _, api := displayTestAPI(t)
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604})
	if err != nil {
		t.Fatal(err)
	}
	var changed atomic.Bool
	p.inventory = func(ctx context.Context, cfg Config, _ int64) Inventory {
		inv := p.Providers.Inventory(ctx, cfg)
		if changed.Load() {
			inv.Containers[0].Name = "external-edit"
			inv.Containers[0].Fingerprint = "changed-fingerprint"
		}
		return inv
	}
	displayGET[Reconciliation](t, api, context.Background(), "reconciliation")
	beforeInv, beforeSnap := p.inventories.Load(), p.snapshots.Load()
	changed.Store(true)
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err == nil || !strings.Contains(err.Error(), "inventory changed") {
		t.Fatalf("apply skipped fresh inventory: %v", err)
	}
	out := displayGET[Reconciliation](t, api, context.Background(), "reconciliation")
	if out.Inventory.Containers[0].Name != "external-edit" || p.inventories.Load() != beforeInv+2 || p.snapshots.Load() != beforeSnap+1 {
		t.Fatal("failed apply retained display authority")
	}
}

func TestDisplayCacheNeverSuppliesRecoveryInputs(t *testing.T) {
	s, p, store, api := displayTestAPI(t)
	probe := p.Providers.(*probeProviders)
	probe.applyFail = true
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604})
	if err != nil {
		t.Fatal(err)
	}
	op, err := s.ApplyPlan(context.Background(), plan.ID)
	if err == nil || op.Status != "partial" {
		t.Fatal("fixture did not reach recoverable partial operation")
	}
	probe.applyFail = false
	ownedID := store.Read().Owned["main:604"].Resources[0].ID
	var changed atomic.Bool
	p.snapshot = func(ctx context.Context, o OPNsense, _ int64) (ProviderSnapshot, error) {
		snap, err := p.Providers.Snapshot(ctx, o)
		if changed.Load() {
			for _, r := range snap.Resources {
				if r.ID == ownedID {
					r.Fields["description"] = "externally edited owned resource"
				}
			}
		}
		return snap, err
	}
	displayGET[Reconciliation](t, api, context.Background(), "reconciliation")
	beforeInv, beforeSnap := p.inventories.Load(), p.snapshots.Load()
	changed.Store(true)
	if _, err = s.Recover(context.Background(), op.ID); err == nil || !strings.Contains(err.Error(), "owned provider resource") {
		t.Fatalf("recovery skipped fresh resource guards: %v", err)
	}
	if p.inventories.Load() != beforeInv+1 || p.snapshots.Load() != beforeSnap+1 {
		t.Fatal("recovery did not read fresh inventory and snapshot")
	}
}

func TestDisplayResponsesOwnClonesDuringConcurrentDecoration(t *testing.T) {
	s, p, store, api := displayTestAPI(t)
	ensureDemo(t, s)
	shared := p.Providers.Inventory(context.Background(), store.Read().Config)
	p.inventory = func(context.Context, Config, int64) Inventory { return shared }
	first := displayGET[Reconciliation](t, api, context.Background(), "reconciliation")
	if displayItem(t, first, "main:604").State != "healthy" {
		t.Fatal("fixture route is not healthy")
	}
	shared.Containers[0].Name = "provider-owned-buffer-overwritten"
	*shared.Hosts[0].NextVMID = 0
	beforeInv, beforeSnap := p.inventories.Load(), p.snapshots.Load()
	results := make(chan Reconciliation, 12)
	for range 12 {
		go func() { results <- displayGET[Reconciliation](t, api, context.Background(), "reconciliation") }()
	}
	for range 12 {
		out := awaitProviderRead(t, results)
		if out.Inventory.Containers[0].Name != first.Inventory.Containers[0].Name || *out.Inventory.Hosts[0].NextVMID == 0 || displayItem(t, out, "main:604").State != "healthy" {
			t.Fatal("provider or response memory altered a cached observation")
		}
	}
	if p.inventories.Load() != beforeInv || p.snapshots.Load() != beforeSnap {
		t.Fatal("warm concurrent reads repeated external I/O")
	}
}
