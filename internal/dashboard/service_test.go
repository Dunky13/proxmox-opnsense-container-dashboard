package dashboard

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type probeProviders struct {
	*DemoProviders
	mu         sync.Mutex
	inventory  func(Inventory) Inventory
	readFail   bool
	applyFail  bool
	createFail bool
	events     []string
	run        func(context.Context, Host, int, string) (RunResult, error)
}

func (p *probeProviders) Inventory(ctx context.Context, c Config) Inventory {
	out := p.DemoProviders.Inventory(ctx, c)
	if p.inventory != nil {
		return p.inventory(out)
	}
	return out
}
func (p *probeProviders) Snapshot(ctx context.Context, o OPNsense) (ProviderSnapshot, error) {
	if p.readFail {
		return ProviderSnapshot{}, errors.New("provider failed")
	}
	return p.DemoProviders.Snapshot(ctx, o)
}
func (p *probeProviders) Create(ctx context.Context, o OPNsense, k string, f map[string]string) (Resource, error) {
	p.mu.Lock()
	p.events = append(p.events, "create:"+k)
	p.mu.Unlock()
	if p.createFail {
		return Resource{ID: "99999999-1111-4111-8111-123456789012", Kind: k}, unavailable("uncertain create")
	}
	return p.DemoProviders.Create(ctx, o, k, f)
}
func (p *probeProviders) Set(ctx context.Context, o OPNsense, r Resource, f map[string]string) (Resource, error) {
	p.mu.Lock()
	p.events = append(p.events, "set:"+r.Kind+":"+f["enabled"])
	p.mu.Unlock()
	return p.DemoProviders.Set(ctx, o, r, f)
}
func (p *probeProviders) Apply(ctx context.Context, o OPNsense, k string) error {
	p.mu.Lock()
	p.events = append(p.events, "apply:"+k)
	p.mu.Unlock()
	if p.applyFail {
		return unavailable("reconfigure failed")
	}
	return nil
}
func (p *probeProviders) Run(ctx context.Context, h Host, id int, k string) (RunResult, error) {
	if p.run != nil {
		return p.run(ctx, h, id, k)
	}
	return p.DemoProviders.Run(ctx, h, id, k)
}
func demoService(t *testing.T) (*Service, *probeProviders, *Store) {
	t.Helper()
	store, e := OpenStore(t.TempDir(), true)
	if e != nil {
		t.Fatal(e)
	}
	d, e := NewDemo(store)
	if e != nil {
		t.Fatal(e)
	}
	p := &probeProviders{DemoProviders: d}
	s := NewService(store, p)
	t.Cleanup(func() { s.Close(); store.Close() })
	return s, p, store
}
func ensureDemo(t *testing.T, s *Service) Operation {
	t.Helper()
	p, e := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604})
	if e != nil {
		t.Fatal(e)
	}
	op, e := s.ApplyPlan(context.Background(), p.ID)
	if e != nil {
		t.Fatal(e)
	}
	return op
}
func TestPrivateWorkflowDurableOwnershipAndReconfigureRecovery(t *testing.T) {
	s, p, store := demoService(t)
	p.applyFail = true
	plan, e := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604})
	if e != nil {
		t.Fatal(e)
	}
	op, e := s.ApplyPlan(context.Background(), plan.ID)
	if e == nil || op.Status != "partial" || op.ManualRecovery || op.Step != "reconfigure" {
		t.Fatalf("partial %+v %v", op, e)
	}
	own := store.Read().Owned["main:604"]
	if len(own.Resources) != 4 {
		t.Fatalf("lost resource ownership %+v", own)
	}
	snap, e := p.Snapshot(context.Background(), store.Read().Config.OPNsenses[0])
	if e != nil {
		t.Fatal(e)
	}
	domain, acl := Resource{}, Resource{}
	for _, r := range snap.Resources {
		if r.Kind == "domain" {
			domain = r
		}
		if r.Kind == "acl" {
			acl = r
		}
	}
	if domain.Fields["enabled"] != "1" || domain.Fields["accesslist"] != acl.ID || acl.Fields["RequestMatcher"] != "remote_ip" {
		t.Fatal("private ACL missing")
	}
	want := []string{"create:reservation", "create:acl", "create:domain", "create:handle", "set:domain:1", "apply:kea"}
	if fingerprint(p.events) != fingerprint(want) {
		t.Fatalf("unsafe order %+v", p.events)
	}
	if _, e = s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 601}); e == nil {
		t.Fatal("did not block unfinished router")
	}
	p.applyFail = false
	op, e = s.Recover(context.Background(), op.ID)
	if e != nil || op.Status != "succeeded" {
		t.Fatal(op, e)
	}
	result := s.Reconcile(context.Background())
	found := false
	for _, item := range result.Items {
		if item.Key == "main:604" {
			found = true
			if item.State != "healthy" {
				t.Fatalf("reconciliation %+v", item)
			}
		}
	}
	if !found {
		t.Fatal("container missing")
	}
}
func TestStalePlanAndUnownedConflict(t *testing.T) {
	s, p, store := demoService(t)
	plan, e := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604})
	if e != nil {
		t.Fatal(e)
	}
	p.inventory = func(i Inventory) Inventory { i.Containers[0].Fingerprint = "changed"; return i }
	if _, e = s.ApplyPlan(context.Background(), plan.ID); e == nil || len(p.events) != 0 {
		t.Fatal("stale plan wrote provider")
	}
	p.inventory = nil
	o := store.Read().Config.OPNsenses[0]
	if _, e = p.DemoProviders.Create(context.Background(), o, "reservation", map[string]string{"hw_address": "d0:cc:37:00:06:04", "ip_address": "192.168.6.4"}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604}); e == nil {
		t.Fatal("adopted unrelated reservation")
	}
	if len(store.Read().Owned) != 0 {
		t.Fatal("inferred ownership")
	}
}
func TestUncertainCreateNeverAutoRetries(t *testing.T) {
	s, p, _ := demoService(t)
	p.createFail = true
	plan, e := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604})
	if e != nil {
		t.Fatal(e)
	}
	op, e := s.ApplyPlan(context.Background(), plan.ID)
	if e == nil || op.Status != "partial" || !op.ManualRecovery {
		t.Fatal(op, e)
	}
	if _, e = s.Recover(context.Background(), op.ID); e == nil {
		t.Fatal("replayed uncertain create")
	}
	if len(op.Resources) != 1 || op.Resources[0].ID != "99999999-1111-4111-8111-123456789012" || op.Resources[0].Fingerprint != "" {
		t.Fatal("known but unverified provider UUID was lost", op.Resources)
	}
	if len(p.events) != 1 {
		t.Fatal(p.events)
	}
}
func TestStoppedInventoryFailureAndVMIDReusePreventCleanup(t *testing.T) {
	s, p, _ := demoService(t)
	ensureDemo(t, s)
	if _, e := s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 604}); e == nil {
		t.Fatal("deleted live container resources")
	}
	p.inventory = func(i Inventory) Inventory {
		for n := range i.Containers {
			if i.Containers[n].VMID == 604 {
				i.Containers[n].Status = "stopped"
			}
		}
		return i
	}
	if _, e := s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 604}); e == nil {
		t.Fatal("deleted stopped container resources")
	}
	p.inventory = func(i Inventory) Inventory { i.Complete = false; i.Containers = nil; return i }
	out := s.Reconcile(context.Background())
	for _, item := range out.Items {
		if item.Key == "main:604" && item.State != "unknown" {
			t.Fatal(item)
		}
	}
	if _, e := s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 604}); e == nil {
		t.Fatal("cleanup accepted failed inventory")
	}
	p.inventory = withoutDemoCT
	out = s.Reconcile(context.Background())
	for _, item := range out.Items {
		if item.Key == "main:604" && item.State != "ownedOrphan" {
			t.Fatal(item)
		}
	}
	plan, e := s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 604})
	if e != nil {
		t.Fatal(e)
	}
	p.inventory = func(i Inventory) Inventory {
		i = withoutDemoCT(i)
		i.Hosts[0].Guests = append(i.Hosts[0].Guests, Guest{604, "lxc", "migrated"})
		return i
	}
	if _, e = s.ApplyPlan(context.Background(), plan.ID); e == nil {
		t.Fatal("accepted migrated/reused VMID")
	}
	p.inventory = withoutDemoCT
	p.readFail = true
	out = s.Reconcile(context.Background())
	for _, item := range out.Items {
		if item.Key == "main:604" && item.State != "unknown" {
			t.Fatal(item)
		}
	}
}
func withoutDemoCT(i Inventory) Inventory {
	cs := []Container{}
	for _, c := range i.Containers {
		if c.VMID != 604 {
			cs = append(cs, c)
		}
	}
	i.Containers = cs
	for n := range i.Hosts {
		gs := []Guest{}
		for _, g := range i.Hosts[n].Guests {
			if g.VMID != 604 {
				gs = append(gs, g)
			}
		}
		i.Hosts[n].Guests = gs
	}
	return i
}
func TestCleanupResourceChangeAndRecovery(t *testing.T) {
	s, p, store := demoService(t)
	ensureDemo(t, s)
	p.inventory = withoutDemoCT
	plan, e := s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 604})
	if e != nil {
		t.Fatal(e)
	}
	o := store.Read().Config.OPNsenses[0]
	snap, _ := p.Snapshot(context.Background(), o)
	ref, _ := ownedByKind(store.Read().Owned["main:604"], "reservation")
	r, _ := snap.Find(ref.ID, "reservation")
	if _, e = p.DemoProviders.Set(context.Background(), o, r, map[string]string{"description": "external edit"}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ApplyPlan(context.Background(), plan.ID); e == nil {
		t.Fatal("deleted changed owned resource")
	}
	if _, e = p.DemoProviders.Set(context.Background(), o, func() Resource {
		s, _ := p.Snapshot(context.Background(), o)
		r, _ := s.Find(ref.ID, "reservation")
		return r
	}(), map[string]string{"description": r.Fields["description"]}); e != nil {
		t.Fatal(e)
	}
	plan, e = s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 604})
	if e != nil {
		t.Fatal(e)
	}
	p.applyFail = true
	op, e := s.ApplyPlan(context.Background(), plan.ID)
	if e == nil || op.Status != "partial" || op.ManualRecovery {
		t.Fatal(op, e)
	}
	if _, ok := store.Read().Owned["main:604"]; !ok {
		t.Fatal("erased pending cleanup")
	}
	p.applyFail = false
	op, e = s.Recover(context.Background(), op.ID)
	if e != nil || op.Status != "succeeded" {
		t.Fatal(op, e)
	}
	if _, ok := store.Read().Owned["main:604"]; ok {
		t.Fatal("ownership not cleared after confirmed cleanup")
	}
}
func waitJob(t *testing.T, s *Service, id string) Job {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		j, e := s.Job(id)
		if e != nil {
			t.Fatal(e)
		}
		if j.Status != "queued" && j.Status != "running" {
			return j
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("job did not finish")
	return Job{}
}
func TestJobsSerializedStoppedTimeoutAndAcknowledgement(t *testing.T) {
	s, p, _ := demoService(t)
	inv := s.Inventory(context.Background())
	ct, _ := inv.Find("main:604")
	release := make(chan struct{})
	p.run = func(ctx context.Context, h Host, id int, k string) (RunResult, error) {
		if k == "inspect" {
			return RunResult{Distribution: "debian", Version: "13"}, nil
		}
		select {
		case <-ctx.Done():
			return RunResult{}, ctx.Err()
		case <-release:
			return RunResult{RebootRequired: true}, nil
		}
	}
	j, e := s.StartJob(context.Background(), JobRequest{"main", 604, "osUpdate", ct.Fingerprint})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.StartJob(context.Background(), JobRequest{"main", 604, "appUpdate", ct.Fingerprint}); e == nil {
		t.Fatal("allowed duplicate job")
	}
	stopped, _ := inv.Find("main:606")
	if _, e = s.StartJob(context.Background(), JobRequest{"main", 606, "appUpdate", stopped.Fingerprint}); e == nil {
		t.Fatal("updated stopped container")
	}
	close(release)
	j = waitJob(t, s, j.ID)
	if j.Status != "rebootRequired" {
		t.Fatal(j)
	}
	if _, e = s.StartJob(context.Background(), JobRequest{"main", 604, "appUpdate", ct.Fingerprint}); e == nil {
		t.Fatal("allowed unacknowledged reboot state")
	}
	if _, e = s.AcknowledgeJob(j.ID); e != nil {
		t.Fatal(e)
	}
	s.JobTimeout = 20 * time.Millisecond
	p.run = func(ctx context.Context, h Host, id int, k string) (RunResult, error) {
		<-ctx.Done()
		return RunResult{}, ctx.Err()
	}
	j, e = s.StartJob(context.Background(), JobRequest{"main", 604, "appUpdate", ct.Fingerprint})
	if e != nil {
		t.Fatal(e)
	}
	j = waitJob(t, s, j.ID)
	if j.Status != "timedOut" {
		t.Fatal(j)
	}
}

func TestHostRelationshipsAndDiscoveredSubnets(t *testing.T) {
	s, p, store := demoService(t)
	cfg := store.Read().Config
	second := DefaultProfiles()[1]
	second.BaseURL = "https://second-pve.invalid"
	second.Node = "pvedell"
	second.OPNsenseID = cfg.OPNsenses[0].ID
	second.SubnetID = "77777777-1111-4111-8111-123456789012"
	cfg.Hosts[1] = second
	if e := ValidateConfig(cfg); e != nil {
		t.Fatal(e)
	}
	if e := store.Update(func(st *State) error { st.Config = cfg; return nil }); e != nil {
		t.Fatal(e)
	}
	plan, e := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: second.ID, VMID: 702})
	if e != nil {
		t.Fatal(e)
	}
	op, e := s.ApplyPlan(context.Background(), plan.ID)
	if e != nil || op.Status != "succeeded" {
		t.Fatal(op, e)
	}
	snap, _ := p.Snapshot(context.Background(), routerForHost(cfg, second))
	found := false
	for _, r := range snap.Resources {
		if r.Kind == "reservation" {
			found = true
			if r.Fields["subnet"] != "22222222-2222-4222-8222-222222222222" || r.Fields["ip_address"] != "192.168.7.2" || r.Fields["hw_address"] != "d0:cc:37:00:07:02" {
				t.Fatal(r)
			}
		}
	}
	if !found {
		t.Fatal("reservation missing")
	}
	cfg.OPNsenses = nil
	if ValidateConfig(cfg) == nil {
		t.Fatal("accepted dangling host relation")
	}
}

func TestDemoFleetIncludesBothHostsAndRequestedHoles(t *testing.T) {
	s, _, store := demoService(t)
	if err := ValidateConfig(store.Read().Config); err != nil {
		t.Fatal(err)
	}
	inv := s.Inventory(context.Background())
	if !inv.Complete || len(inv.Containers) != 8 || len(inv.Hosts) != 2 {
		t.Fatalf("unexpected demo inventory: %+v", inv)
	}
	for _, host := range inv.Hosts {
		want := map[string]int{"main": 605, "pvedell": 703}[host.HostID]
		if host.NextVMID == nil || *host.NextVMID != want {
			t.Fatalf("%s next VMID: %v, want %d", host.HostID, host.NextVMID, want)
		}
	}
	stopped, ok := inv.Find("main:606")
	if !ok || stopped.Status != "stopped" || stopped.IP != "192.168.6.6" || stopped.MAC != "d0:cc:37:00:06:06" {
		t.Fatalf("stopped fixture: %+v", stopped)
	}
}
func TestProviderPrerequisitesAndContainerConflicts(t *testing.T) {
	s, p, store := demoService(t)
	p.inventory = func(i Inventory) Inventory {
		other := i.Containers[0]
		other.Key = "other:999"
		other.VMID = 999
		other.MAC = "d0:cc:37:00:06:04"
		i.Containers = append(i.Containers, other)
		return i
	}
	if _, e := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604}); e == nil {
		t.Fatal("accepted conflicting container MAC")
	}
	p.inventory = nil
	if e := store.Update(func(st *State) error { st.Config.OPNsenses[0].AllowlistCIDRs = nil; return nil }); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604}); e == nil {
		t.Fatal("private route silently became public")
	}
	plan, e := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604, Public: true})
	if e != nil {
		t.Fatal(e)
	}
	op, e := s.ApplyPlan(context.Background(), plan.ID)
	if e != nil || op.Status != "succeeded" {
		t.Fatal(op, e)
	}
	for _, ref := range store.Read().Owned["main:604"].Resources {
		if ref.Kind == "acl" {
			t.Fatal("unexpected ACL on explicitly public route")
		}
	}
}
func TestUnrelatedReferencesBlockCleanup(t *testing.T) {
	s, p, store := demoService(t)
	ensureDemo(t, s)
	own := store.Read().Owned["main:604"]
	domainID := ""
	for _, r := range own.Resources {
		if r.Kind == "domain" {
			domainID = r.ID
		}
	}
	if e := store.Update(func(st *State) error {
		st.DemoResources[own.OPNsenseID] = append(st.DemoResources[own.OPNsenseID], Resource{ID: "external-subdomain", Kind: "subdomain", Fields: map[string]string{"reverse": domainID}})
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	p.inventory = withoutDemoCT
	if _, e := s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 604}); e == nil {
		t.Fatal("accepted unrelated subdomain reference")
	}
}
func TestUnsupportedOSDoesNotRunUpdate(t *testing.T) {
	s, p, _ := demoService(t)
	executed := false
	p.run = func(ctx context.Context, h Host, id int, k string) (RunResult, error) {
		if k == "inspect" {
			return RunResult{Distribution: "fedora", Version: "44"}, nil
		}
		executed = true
		return RunResult{}, nil
	}
	ct, _ := s.Inventory(context.Background()).Find("main:604")
	j, e := s.StartJob(context.Background(), JobRequest{"main", 604, "osUpdate", ct.Fingerprint})
	if e != nil {
		t.Fatal(e)
	}
	j = waitJob(t, s, j.ID)
	if executed || j.Status != "failed" {
		t.Fatal(j)
	}
}

func TestPerContainerOverridesPlanEffectsAndOwnedVisibilityTransitions(t *testing.T) {
	s, p, store := demoService(t)
	scheme, port := "http", 2283
	req := PlanRequest{Action: "ensure", HostID: "main", VMID: 604, UpstreamScheme: &scheme, UpstreamPort: &port}
	plan, e := s.Plan(context.Background(), req)
	if e != nil {
		t.Fatal(e)
	}
	if plan.DesiredRoute.Port != 2283 || len(plan.Effects) != 4 {
		t.Fatal("missing concrete review effects", plan)
	}
	sawPort := false
	for _, effect := range plan.Effects {
		if effect.Action != "create" || effect.ResourceID != "" {
			t.Fatal(effect)
		}
		for _, change := range effect.Changes {
			if change.Field == "upstreamPort" && change.After == "2283" {
				sawPort = true
			}
		}
	}
	if !sawPort || len(p.events) != 0 {
		t.Fatal("plan did not describe port or wrote providers")
	}
	op, e := s.ApplyPlan(context.Background(), plan.ID)
	if e != nil {
		t.Fatal(op, e)
	}
	original := store.Read().Owned["main:604"]
	domainRef, _ := ownedByKind(original, "domain")
	handleRef, _ := ownedByKind(original, "handle")
	if original.DesiredRoute.Port != 2283 {
		t.Fatal("override not durable")
	}
	inv := s.Inventory(context.Background())
	ct, _ := inv.Find("main:604")
	if ct.DesiredRoute == nil || ct.DesiredRoute.Port != 2283 || ct.DesiredRoute.Public {
		t.Fatal("desired route missing from inventory", ct)
	}
	// Changing global defaults does not overwrite this container's recorded target.
	if e = store.Update(func(st *State) error { st.Config.OPNsenses[0].UpstreamPort = 8000; return nil }); e != nil {
		t.Fatal(e)
	}
	result := s.Reconcile(context.Background())
	for _, item := range result.Items {
		if item.Key == ct.Key && (item.State != "healthy" || item.DesiredRoute == nil || item.DesiredRoute.Port != 2283) {
			t.Fatal(item)
		}
	}
	for _, public := range []bool{true, false} {
		plan, e = s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604, Public: public})
		if e != nil {
			t.Fatal("healthy owned route could not change visibility", e)
		}
		if plan.DesiredRoute.Port != 2283 {
			t.Fatal("omission did not preserve override")
		}
		hasDomainChange := false
		for _, effect := range plan.Effects {
			if effect.Kind == "domain" {
				if effect.ResourceID != domainRef.ID || effect.Action != "update" {
					t.Fatal(effect)
				}
				for _, change := range effect.Changes {
					if change.Field == "accessListId" {
						hasDomainChange = true
					}
				}
			}
		}
		if !hasDomainChange {
			t.Fatal("visibility change not reviewable")
		}
		op, e = s.ApplyPlan(context.Background(), plan.ID)
		if e != nil {
			t.Fatal(op, e)
		}
		own := store.Read().Owned[ct.Key]
		domain, _ := ownedByKind(own, "domain")
		handle, _ := ownedByKind(own, "handle")
		if own.DesiredRoute.Public != public || domain.ID != domainRef.ID || handle.ID != handleRef.ID {
			t.Fatal("route was recreated or visibility not persisted", own)
		}
		snap, _ := p.Snapshot(context.Background(), store.Read().Config.OPNsenses[0])
		d, _ := snap.Find(domain.ID, "domain")
		if public && d.Fields["accesslist"] != "" || !public && d.Fields["accesslist"] == "" {
			t.Fatal("wrong ACL attachment", d)
		}
	}
	// A no-change plan tells the truth and performs no provider writes/reconfigure.
	plan, e = s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604})
	if e != nil {
		t.Fatal(e)
	}
	for _, effect := range plan.Effects {
		if effect.Action != "retain" {
			t.Fatal(effect)
		}
	}
	before := len(p.events)
	if _, e = s.ApplyPlan(context.Background(), plan.ID); e != nil || len(p.events) != before {
		t.Fatal("no-change review did not match actual effects", e)
	}
	// Scheme and port can then change in place for this one app.
	scheme, port = "https", 8443
	plan, e = s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604, UpstreamScheme: &scheme, UpstreamPort: &port})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ApplyPlan(context.Background(), plan.ID); e != nil {
		t.Fatal(e)
	}
	own := store.Read().Owned[ct.Key]
	if own.DesiredRoute.Scheme != "https" || own.DesiredRoute.Port != 8443 {
		t.Fatal(own)
	}
	snap, _ := p.Snapshot(context.Background(), store.Read().Config.OPNsenses[0])
	r, _ := snap.Find(handleRef.ID, "handle")
	if r.Fields["HttpTls"] != "1" || r.Fields["ToPort"] != "8443" {
		t.Fatal(r)
	}
	for _, badPort := range []int{0, -1, 65536} {
		if _, e = s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604, UpstreamPort: &badPort}); e == nil {
			t.Fatal("accepted invalid override")
		}
	}
	scheme = "file"
	if _, e = s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604, UpstreamScheme: &scheme}); e == nil {
		t.Fatal("accepted arbitrary scheme")
	}
}
