package dashboard

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestRecoveryPreservesServiceOrderAfterRepeatedFailure(t *testing.T) {
	for _, action := range []string{"ensure", "cleanup"} {
		t.Run(action, func(t *testing.T) {
			s, p, store := demoService(t)
			first, second := "kea", "caddy"
			if action == "cleanup" {
				ensureDemo(t, s)
				p.inventory = withoutDemoCT
				first, second = "caddy", "kea"
			}
			plan, err := s.Plan(context.Background(), PlanRequest{Action: action, HostID: "main", VMID: 604})
			if err != nil {
				t.Fatal(err)
			}
			p.applyFail = true
			op, err := s.ApplyPlan(context.Background(), plan.ID)
			if err == nil || op.Status != "partial" || op.Step != "reconfigure" {
				t.Fatal(op, err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				p.events = nil
				if _, err := s.Recover(context.Background(), op.ID); err == nil {
					t.Fatal("recovery unexpectedly succeeded")
				}
				if strings.Join(p.events, ",") != "apply:"+first {
					t.Fatalf("unsafe recovery order: %v", p.events)
				}
				if _, ok := store.Read().Owned[op.Key]; !ok {
					t.Fatal("discarded pending ownership")
				}
			}
			p.applyFail = false
			p.events = nil
			recovered, err := s.Recover(context.Background(), op.ID)
			if err != nil || recovered.Status != "succeeded" {
				t.Fatal(recovered, err)
			}
			if strings.Join(p.events, ",") != "apply:"+first+",apply:"+second {
				t.Fatalf("wrong successful recovery order: %v", p.events)
			}
			_, remains := store.Read().Owned[op.Key]
			if remains != (action == "ensure") {
				t.Fatalf("unexpected ownership after %s", action)
			}
		})
	}
}

func TestUnavailableLegacyRouteNeverUsesFirewallDefaults(t *testing.T) {
	s, p, store := demoService(t)
	ensureDemo(t, s)
	own := store.Read().Owned["main:604"]
	ref, _ := ownedByKind(own, "handle")
	snap, err := p.Snapshot(context.Background(), store.Read().Config.OPNsenses[0])
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := snap.Find(ref.ID, "handle")
	if err = p.DemoProviders.Delete(context.Background(), store.Read().Config.OPNsenses[0], handler); err != nil {
		t.Fatal(err)
	}
	if err = store.Update(func(st *State) error {
		legacy := st.Owned[own.Key]
		legacy.DesiredRoute = RouteConfig{}
		st.Owned[own.Key] = legacy
		st.Config.OPNsenses[0].UpstreamScheme = "https"
		st.Config.OPNsenses[0].UpstreamPort = 15443
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	result := s.Reconcile(context.Background())
	for _, container := range result.Inventory.Containers {
		if container.Key == own.Key && container.DesiredRoute != nil {
			t.Fatal("invented container upstream", container.DesiredRoute)
		}
	}
	matched := false
	for _, item := range result.Items {
		if item.Key == own.Key {
			matched = true
			if item.DesiredRoute != nil || !item.RouteUnavailable || item.State != "mismatch" {
				t.Fatal("legacy route was not explicitly unavailable", item)
			}
		}
	}
	if !matched {
		t.Fatal("legacy container missing from reconciliation")
	}
	if _, err = s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604}); err == nil {
		t.Fatal("legacy repair accepted without deliberate app values")
	}
	scheme, port := "http", 8443
	if _, err = s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604, UpstreamScheme: &scheme}); err == nil {
		t.Fatal("legacy repair accepted without app port")
	}
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604, UpstreamScheme: &scheme, UpstreamPort: &port})
	if err != nil || plan.DesiredRoute.Scheme != scheme || plan.DesiredRoute.Port != port {
		t.Fatal(plan, err)
	}
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
		t.Fatal(err)
	}
	if got := store.Read().Owned[own.Key].DesiredRoute; got.Scheme != scheme || got.Port != port {
		t.Fatal("repair did not persist deliberate app values", got)
	}
}

func TestRecoveryRejectsStaleTargetAndConflictingAddress(t *testing.T) {
	for _, change := range []string{"ip", "hostname", "digest", "duplicate IP"} {
		t.Run(change, func(t *testing.T) {
			s, p, _ := demoService(t)
			p.applyFail = true
			plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604})
			if err != nil {
				t.Fatal(err)
			}
			op, err := s.ApplyPlan(context.Background(), plan.ID)
			if err == nil || op.Step != "reconfigure" {
				t.Fatal("expected service-application failure", op, err)
			}
			p.applyFail = false
			p.events = nil
			p.inventory = func(inv Inventory) Inventory {
				for i := range inv.Containers {
					c := &inv.Containers[i]
					if change == "duplicate IP" && c.Key == "main:603" {
						c.IP = "192.168.6.4"
					} else if c.Key == "main:604" && change != "duplicate IP" {
						c.Fingerprint += "-changed"
						if change == "ip" {
							c.IP = "192.168.6.99"
						}
						if change == "hostname" {
							c.Name = "different-app"
						}
					}
				}
				return inv
			}
			if _, err := s.Recover(context.Background(), op.ID); err == nil || len(p.events) != 0 {
				t.Fatalf("stale recovery applied services: %v, events %v", err, p.events)
			}
		})
	}
}

type sortedACLProvider struct{ *probeProviders }

func (p sortedACLProvider) Create(ctx context.Context, o OPNsense, kind string, fields map[string]string) (Resource, error) {
	if kind == "acl" {
		fields = clone(fields)
		parts := strings.Split(fields["clientIps"], ",")
		sort.Strings(parts)
		fields["clientIps"] = strings.Join(parts, ",")
	}
	return p.probeProviders.Create(ctx, o, kind, fields)
}

func TestPrivateRouteAcceptsProviderSortedCIDRs(t *testing.T) {
	s, p, _ := demoService(t)
	s.Providers = sortedACLProvider{p}
	ensureDemo(t, s)
	for _, item := range s.Reconcile(context.Background()).Items {
		if item.Key == "main:604" && item.State != "healthy" {
			t.Fatalf("ordered CIDRs caused mismatch: %+v", item)
		}
	}
}

func TestEnsureRecreatesOnlyMissingOwnedResource(t *testing.T) {
	for _, kind := range []string{"reservation", "acl", "domain", "handle"} {
		t.Run(kind, func(t *testing.T) {
			s, p, store := demoService(t)
			ensureDemo(t, s)
			own := store.Read().Owned["main:604"]
			ref, _ := ownedByKind(own, kind)
			o := store.Read().Config.OPNsenses[0]
			snap, _ := p.Snapshot(context.Background(), o)
			resource, _ := snap.Find(ref.ID, kind)
			if err := p.DemoProviders.Delete(context.Background(), o, resource); err != nil {
				t.Fatal(err)
			}
			plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604})
			if err != nil {
				t.Fatal("cannot plan missing resource repair", err)
			}
			found := false
			for _, effect := range plan.Effects {
				if effect.Kind == kind && effect.Action == "create" {
					found = true
				}
			}
			if !found {
				t.Fatal("repair creation absent from review", plan.Effects)
			}
			if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
				t.Fatal(err)
			}
			current := store.Read().Owned[own.Key]
			for _, before := range own.Resources {
				after, ok := ownedByKind(current, before.Kind)
				if !ok || (before.Kind == kind && before.ID == after.ID) || (before.Kind != kind && before.ID != after.ID) {
					t.Fatalf("unexpected ownership replacement: before %+v after %+v", before, after)
				}
			}
			for _, item := range s.Reconcile(context.Background()).Items {
				if item.Key == own.Key && item.State != "healthy" {
					t.Fatalf("repair did not restore route: %+v", item)
				}
			}
		})
	}
}

func TestMissingOwnedReservationCannotAdoptForeignReplacement(t *testing.T) {
	s, p, store := demoService(t)
	ensureDemo(t, s)
	o := store.Read().Config.OPNsenses[0]
	snap, _ := p.Snapshot(context.Background(), o)
	ref, _ := ownedByKind(store.Read().Owned["main:604"], "reservation")
	r, _ := snap.Find(ref.ID, ref.Kind)
	if err := p.DemoProviders.Delete(context.Background(), o, r); err != nil {
		t.Fatal(err)
	}
	foreign, err := p.DemoProviders.Create(context.Background(), o, r.Kind, r.Fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604}); err == nil {
		t.Fatal("adopted replacement resource", foreign)
	}
}

func TestOSUpdateRetainsRebootStatusAfterVerboseOutput(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\ndd if=/dev/zero bs=4096 count=20 2>/dev/null | tr '\\000' x\nprintf '\\nDASHBOARD_REBOOT_REQUIRED\\n'\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	h := Host{SSH: SSHConfig{Enabled: true, Host: "mock.invalid", Port: 22, User: "root", PrivateKeyFile: "mock-key", KnownHostsFile: "mock-hosts"}}
	result, err := (OpenSSH{}).Run(context.Background(), h, 604, "osUpdate")
	if err != nil || !result.RebootRequired {
		t.Fatalf("verbose output hid reboot requirement: %+v %v", result, err)
	}
}

func TestPublicRouteCanForgetAlreadyAbsentDetachedACL(t *testing.T) {
	s, p, store := demoService(t)
	ensureDemo(t, s)
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604, Public: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
		t.Fatal(err)
	}
	o := store.Read().Config.OPNsenses[0]
	ref, _ := ownedByKind(store.Read().Owned["main:604"], "acl")
	snap, _ := p.Snapshot(context.Background(), o)
	acl, _ := snap.Find(ref.ID, "acl")
	if err = p.DemoProviders.Delete(context.Background(), o, acl); err != nil {
		t.Fatal(err)
	}
	plan, err = s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604, Public: true})
	if err != nil {
		t.Fatal(err)
	}
	p.events = nil
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil || len(p.events) != 0 {
		t.Fatalf("ledger repair must not touch provider: %v %v", err, p.events)
	}
	if _, ok := ownedByKind(store.Read().Owned["main:604"], "acl"); ok {
		t.Fatal("absent detached ACL remained in ownership")
	}
}

func TestCleanupRetainsMissingResourceAndDeletesVerifiedRemainder(t *testing.T) {
	s, p, store := demoService(t)
	ensureDemo(t, s)
	o := store.Read().Config.OPNsenses[0]
	ref, _ := ownedByKind(store.Read().Owned["main:604"], "handle")
	snap, _ := p.Snapshot(context.Background(), o)
	r, _ := snap.Find(ref.ID, ref.Kind)
	if err := p.DemoProviders.Delete(context.Background(), o, r); err != nil {
		t.Fatal(err)
	}
	p.inventory = withoutDemoCT
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 604})
	if err != nil {
		t.Fatal(err)
	}
	for _, effect := range plan.Effects {
		if effect.Kind == "handle" && effect.Action != "retain" {
			t.Fatal("preview pretends an absent resource will be deleted", effect)
		}
	}
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, exists := store.Read().Owned["main:604"]; exists {
		t.Fatal("cleanup retained finished ownership")
	}
}

func TestMissingResourceRepairRefusesUnownedDependencies(t *testing.T) {
	s, p, store := demoService(t)
	ensureDemo(t, s)
	o := store.Read().Config.OPNsenses[0]
	ref, _ := ownedByKind(store.Read().Owned["main:604"], "domain")
	snap, _ := p.Snapshot(context.Background(), o)
	r, _ := snap.Find(ref.ID, ref.Kind)
	if err := p.DemoProviders.Delete(context.Background(), o, r); err != nil {
		t.Fatal(err)
	}
	if _, err := p.DemoProviders.Create(context.Background(), o, "subdomain", map[string]string{"reverse": ref.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604}); err == nil {
		t.Fatal("repair ignored unowned dependency")
	}
}

func TestRebootMarkerCanSpanEveryWriteBoundary(t *testing.T) {
	for split := 1; split < len(rebootMarker); split++ {
		out := &rebootOutput{}
		for _, text := range []string{strings.Repeat("x", 70000), rebootMarker[:split], rebootMarker[split:]} {
			if _, err := out.Write([]byte(text)); err != nil {
				t.Fatal(err)
			}
		}
		if !out.found || len(out.tail) >= len(rebootMarker) {
			t.Fatalf("split %d lost marker or retained unbounded data", split)
		}
	}
}

type subnetChangingProvider struct {
	*probeProviders
	differentSubnet bool
	changeOnApply   bool
}

func (p *subnetChangingProvider) Snapshot(ctx context.Context, o OPNsense) (ProviderSnapshot, error) {
	snap, err := p.probeProviders.Snapshot(ctx, o)
	if p.differentSubnet {
		snap.Subnet = "192.168.99.0/24"
		for i, r := range snap.Resources {
			if r.Kind == "subnet" && r.Fields["subnet"] == "192.168.6.0/24" {
				snap.Resources[i].Fields["subnet"] = "192.168.99.0/24"
			}
		}
	}
	return snap, err
}

func (p *subnetChangingProvider) Apply(ctx context.Context, o OPNsense, service string) error {
	err := p.probeProviders.Apply(ctx, o, service)
	if err == nil && p.changeOnApply {
		p.differentSubnet = true
	}
	return err
}

func TestRecoveryChecksSubnetBeforeEveryServiceApplication(t *testing.T) {
	for _, afterFirst := range []bool{false, true} {
		t.Run(itoa(map[bool]int{false: 0, true: 1}[afterFirst]), func(t *testing.T) {
			s, p, _ := demoService(t)
			p.applyFail = true
			plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604})
			if err != nil {
				t.Fatal(err)
			}
			op, err := s.ApplyPlan(context.Background(), plan.ID)
			if err == nil || op.Step != "reconfigure" {
				t.Fatal(op, err)
			}
			p.applyFail, p.events = false, nil
			s.Providers = &subnetChangingProvider{p, !afterFirst, afterFirst}
			if _, err = s.Recover(context.Background(), op.ID); err == nil {
				t.Fatal("recovered against changed subnet")
			}
			want := 0
			if afterFirst {
				want = 1
			}
			if len(p.events) != want {
				t.Fatal("wrong service application count", p.events)
			}
		})
	}
}

func TestOrphanWithMissingResourcesRemainsReviewableForCleanup(t *testing.T) {
	s, p, store := demoService(t)
	ensureDemo(t, s)
	o := store.Read().Config.OPNsenses[0]
	snap, _ := p.Snapshot(context.Background(), o)
	ref, _ := ownedByKind(store.Read().Owned["main:604"], "handle")
	r, _ := snap.Find(ref.ID, ref.Kind)
	if err := p.DemoProviders.Delete(context.Background(), o, r); err != nil {
		t.Fatal(err)
	}
	p.inventory = withoutDemoCT
	found := false
	for _, item := range s.Reconcile(context.Background()).Items {
		if item.Key == "main:604" {
			found = true
			if item.State != "ownedOrphan" {
				t.Fatal("safe orphan is hidden from cleanup UI", item)
			}
		}
	}
	if !found {
		t.Fatal("orphan absent from reconciliation")
	}
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 604})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
		t.Fatal(err)
	}
}

func TestObservedAndExpectedAddressesAreSeparateInInventory(t *testing.T) {
	s, p, _ := demoService(t)
	p.inventory = func(inv Inventory) Inventory {
		for i := range inv.Containers {
			if inv.Containers[i].Key == "main:604" {
				inv.Containers[i].IP, inv.Containers[i].MAC = "192.168.6.99", "d0:cc:37:00:06:99"
			}
		}
		return inv
	}
	for _, inv := range []Inventory{s.Inventory(context.Background()), s.Reconcile(context.Background()).Inventory} {
		b, _ := json.Marshal(inv)
		var wire struct {
			Containers []struct {
				Key         string `json:"key"`
				IP          string `json:"ip"`
				MAC         string `json:"mac"`
				ExpectedIP  string `json:"expectedIp"`
				ExpectedMAC string `json:"expectedMac"`
			} `json:"containers"`
		}
		if err := json.Unmarshal(b, &wire); err != nil {
			t.Fatal(err)
		}
		for _, c := range wire.Containers {
			if c.Key == "main:604" && (c.IP != "192.168.6.99" || c.ExpectedIP != "192.168.6.4" || c.MAC != "d0:cc:37:00:06:99" || c.ExpectedMAC != "d0:cc:37:00:06:04") {
				t.Fatal("lost observed/expected distinction", c)
			}
		}
	}
}

func TestMissingResourceDoesNotHideUnsafeOrphanConditions(t *testing.T) {
	for _, condition := range []string{"changed", "dependency", "failed read"} {
		t.Run(condition, func(t *testing.T) {
			s, p, store := demoService(t)
			ensureDemo(t, s)
			o := store.Read().Config.OPNsenses[0]
			snap, _ := p.Snapshot(context.Background(), o)
			own := store.Read().Owned["main:604"]
			ref, _ := ownedByKind(own, "handle")
			r, _ := snap.Find(ref.ID, ref.Kind)
			if err := p.DemoProviders.Delete(context.Background(), o, r); err != nil {
				t.Fatal(err)
			}
			want := "mismatch"
			switch condition {
			case "changed":
				ref, _ = ownedByKind(own, "reservation")
				r, _ = snap.Find(ref.ID, ref.Kind)
				if _, err := p.DemoProviders.Set(context.Background(), o, r, map[string]string{"description": "external change"}); err != nil {
					t.Fatal(err)
				}
			case "dependency":
				ref, _ = ownedByKind(own, "domain")
				if _, err := p.DemoProviders.Create(context.Background(), o, "subdomain", map[string]string{"reverse": ref.ID}); err != nil {
					t.Fatal(err)
				}
				want = "conflict"
			case "failed read":
				p.readFail = true
				want = "unknown"
			}
			p.inventory = withoutDemoCT
			for _, item := range s.Reconcile(context.Background()).Items {
				if item.Key == own.Key && item.State != want {
					t.Fatal(condition, item)
				}
			}
		})
	}
}
