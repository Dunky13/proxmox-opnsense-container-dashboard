package dashboard

import (
	"context"
	"strings"
	"testing"
)

type importLifecycleProbe struct {
	*probeProviders
	deletes    []string
	afterSet   func(Resource, map[string]string)
	afterApply func()
}

func (p *importLifecycleProbe) Delete(ctx context.Context, o OPNsense, r Resource) error {
	p.deletes = append(p.deletes, r.Kind+":"+r.ID)
	return p.DemoProviders.Delete(ctx, o, r)
}
func (p *importLifecycleProbe) Set(ctx context.Context, o OPNsense, r Resource, f map[string]string) (Resource, error) {
	out, err := p.probeProviders.Set(ctx, o, r, f)
	if err == nil && p.afterSet != nil {
		p.afterSet(r, f)
	}
	return out, err
}
func (p *importLifecycleProbe) Apply(ctx context.Context, o OPNsense, k string) error {
	err := p.probeProviders.Apply(ctx, o, k)
	if err == nil && p.afterApply != nil {
		p.afterApply()
	}
	return err
}
func removeHikyo(t *testing.T, p *probeProviders) {
	t.Helper()
	old := p.inventory
	p.inventory = func(i Inventory) Inventory {
		i = old(i)
		containers := []Container{}
		for _, c := range i.Containers {
			if c.VMID != 609 {
				containers = append(containers, c)
			}
		}
		i.Containers = containers
		for n := range i.Hosts {
			guests := []Guest{}
			for _, g := range i.Hosts[n].Guests {
				if g.VMID != 609 {
					guests = append(guests, g)
				}
			}
			i.Hosts[n].Guests = guests
		}
		return i
	}
}
func requireApplyOrder(t *testing.T, events []string, want string) {
	t.Helper()
	out := []string{}
	for _, e := range events {
		if strings.HasPrefix(e, "apply:") {
			out = append(out, e)
		}
	}
	if strings.Join(out, ",") != want {
		t.Fatal("wrong service order", events)
	}
}
func TestExistingNativeImportLifecyclePreservesSharedParentSiblingAndACL(t *testing.T) {
	s, p, store := hikyoService(t)
	wrapper := &importLifecycleProbe{probeProviders: p}
	s.Providers = wrapper
	mutateHikyo(t, store, func(_ *State, r *[]Resource) {
		changeHikyo(*r, hikyoHandle, "health_uri", "/health")
		changeHikyo(*r, hikyoHandle, "health_interval", "30s")
		changeHikyo(*r, hikyoChild, "description", "Original per-app description")
		*r = append(*r,
			Resource{ID: "11111111-2222-4333-8444-555555555555", Kind: "subdomain", Fields: map[string]string{"enabled": "1", "reverse": hikyoParent, "FromDomain": "sibling.went.io", "accesslist": ""}},
			Resource{ID: "11111111-2222-4333-8444-666666666666", Kind: "handle", Fields: map[string]string{"enabled": "1", "reverse": hikyoParent, "subdomain": "11111111-2222-4333-8444-555555555555", "ToDomain": "192.168.6.50", "ToPort": "3000"}},
		)
	})
	before := store.Read().DemoResources["demo-edge"]
	importHikyo(t, s)
	for _, public := range []bool{true, false, true, false} {
		p.events = nil
		scheme, port := "https", 8443
		plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 609, Public: public, UpstreamScheme: &scheme, UpstreamPort: &port})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range plan.Effects {
			if e.ResourceID == hikyoParent && e.Action != "retain" {
				t.Fatal("parent mutation", e)
			}
		}
		if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
			t.Fatal(err)
		}
		requireApplyOrder(t, p.events, "apply:kea,apply:caddy")
		own := store.Read().Owned["main:609"]
		if len(own.Resources) != 3 || own.DesiredRoute != (RouteConfig{Scheme: "https", Port: 8443, Public: public}) {
			t.Fatal("managed route lost", own)
		}
		snap, err := p.Snapshot(context.Background(), store.Read().Config.OPNsenses[0])
		if err != nil {
			t.Fatal(err)
		}
		handle, _ := snap.Find(hikyoHandle, "handle")
		child, _ := snap.Find(hikyoChild, "subdomain")
		acl := ""
		if !public {
			acl = clientDemoACL
		}
		if handle.Fields["reverse"] != hikyoParent || handle.Fields["subdomain"] != hikyoChild || handle.Fields["health_uri"] != "/health" || child.Fields["description"] != "Original per-app description" || child.Fields["accesslist"] != acl {
			t.Fatal("native or advanced fields overwritten", handle, child)
		}
		if !public && (own.SharedAccessList == nil || own.SharedAccessList.ClientIPContextFingerprint == "") {
			t.Fatal("private client_ip context missing", own)
		}
		if item := hikyoItem(t, s); item.State != "healthy" {
			t.Fatal(item)
		}
		requireExternalUnchanged(t, before, store.Read().DemoResources["demo-edge"])
	}
	// A matching subsequent plan is a no-op even with retained advanced fields.
	p.events = nil
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 609})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
		t.Fatal(err)
	}
	if len(p.events) != 0 {
		t.Fatal("matching native ensure wrote providers", p.events)
	}
	removeHikyo(t, p)
	if item := hikyoItem(t, s); item.State != "ownedOrphan" {
		t.Fatal("not a cleanup candidate", item)
	}
	plan, err = s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 609})
	if err != nil {
		t.Fatal(err)
	}
	p.events = nil
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
		t.Fatal(err)
	}
	if strings.Join(wrapper.deletes, ",") != "handle:"+hikyoHandle+",subdomain:"+hikyoChild+",reservation:"+hikyoReservation {
		t.Fatal("wrong deletion order", wrapper.deletes)
	}
	requireApplyOrder(t, p.events, "apply:caddy,apply:kea")
	if _, exists := store.Read().Owned["main:609"]; exists {
		t.Fatal("cleanup retained ownership")
	}
	requireExternalUnchanged(t, before, store.Read().DemoResources["demo-edge"])
}

func TestExistingImportedPrivateRouteAndClientIPTrustBinding(t *testing.T) {
	for _, matcher := range []string{"remote_ip", "client_ip"} {
		t.Run(matcher, func(t *testing.T) {
			s, p, store := hikyoService(t)
			mutateHikyo(t, store, func(_ *State, r *[]Resource) {
				changeHikyo(*r, hikyoChild, "accesslist", clientDemoACL)
				changeHikyo(*r, clientDemoACL, "RequestMatcher", matcher)
			})
			before := store.Read().DemoResources["demo-edge"]
			plan, err := s.Plan(context.Background(), PlanRequest{Action: "import", HostID: "main", VMID: 609, Public: true})
			if err != nil {
				t.Fatal(err)
			}
			if plan.Public || plan.DesiredRoute.Public || plan.SharedAccessList == nil {
				t.Fatal("import changed actual private exposure", plan)
			}
			if matcher == "client_ip" && plan.SharedAccessList.ClientIPContextFingerprint == "" {
				t.Fatal("unbound client_ip")
			}
			if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
				t.Fatal(err)
			}
			if len(p.events) != 0 || fingerprint(before) != fingerprint(store.Read().DemoResources["demo-edge"]) {
				t.Fatal("private import wrote")
			}
			plan, err = s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 609})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
				t.Fatal(err)
			}
			if matcher == "client_ip" {
				mutateHikyo(t, store, func(_ *State, r *[]Resource) { changeHikyo(*r, trustDemoACL, "clientIps", "198.51.100.0/24") })
				if item := hikyoItem(t, s); item.State != "mismatch" {
					t.Fatal("trust drift hidden", item)
				}
				// An explicit fresh plan can observe current external policy without a new
				// certificate attestation or manual setup field.
				plan, err = s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 609})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	t.Run("unsafe-trust-prevents-import", func(t *testing.T) {
		s, _, store := hikyoService(t)
		mutateHikyo(t, store, func(_ *State, r *[]Resource) {
			changeHikyo(*r, hikyoChild, "accesslist", clientDemoACL)
			changeHikyo(*r, trustDemoACL, "clientIps", "0.0.0.0/0")
		})
		if _, err := s.Plan(context.Background(), PlanRequest{Action: "import", HostID: "main", VMID: 609}); err == nil {
			t.Fatal("unsafe client_ip import")
		}
	})
	t.Run("stale-trust-prevents-import-apply", func(t *testing.T) {
		s, p, store := hikyoService(t)
		mutateHikyo(t, store, func(_ *State, r *[]Resource) { changeHikyo(*r, hikyoChild, "accesslist", clientDemoACL) })
		plan, err := s.Plan(context.Background(), PlanRequest{Action: "import", HostID: "main", VMID: 609})
		if err != nil {
			t.Fatal(err)
		}
		mutateHikyo(t, store, func(_ *State, r *[]Resource) { changeHikyo(*r, clientDemoHeader, "HeaderType", "Cf-Connecting-Ip") })
		if _, err = s.ApplyPlan(context.Background(), plan.ID); err == nil {
			t.Fatal("stale client_ip import")
		}
		if len(p.events) != 0 {
			t.Fatal("stale import wrote", p.events)
		}
	})
}

func TestExistingNativeMissingResourceRepairKeepsLayoutAndAdvancedSettings(t *testing.T) {
	for _, kind := range []string{"reservation", "subdomain", "handle"} {
		t.Run(kind, func(t *testing.T) {
			s, p, store := hikyoService(t)
			mutateHikyo(t, store, func(_ *State, r *[]Resource) {
				changeHikyo(*r, hikyoChild, "DynDns", "1")
				changeHikyo(*r, hikyoHandle, "health_uri", "/health")
			})
			before := store.Read().DemoResources["demo-edge"]
			importHikyo(t, s)
			own := store.Read().Owned["main:609"]
			ref, _ := ownedByKind(own, kind)
			mutateHikyo(t, store, func(_ *State, r *[]Resource) {
				for i, x := range *r {
					if x.Kind == kind && x.ID == ref.ID {
						*r = append((*r)[:i], (*r)[i+1:]...)
						break
					}
				}
			})
			plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 609, Public: true})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, e := range plan.Effects {
				if e.Kind == kind && e.Action == "create" {
					found = true
				}
				if e.Kind == "domain" && e.Action != "retain" {
					t.Fatal("repair flattened child", e)
				}
			}
			if !found {
				t.Fatal("repair not reviewed", plan.Effects)
			}
			if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
				t.Fatal(err)
			}
			after := store.Read().Owned["main:609"]
			for _, old := range own.Resources {
				now, exists := ownedByKind(after, old.Kind)
				if !exists || (old.Kind == kind && now.ID == old.ID) || (old.Kind != kind && now.ID != old.ID) {
					t.Fatal("wrong repair identity", old, now)
				}
			}
			child, _ := ownedByKind(after, "subdomain")
			handle, _ := ownedByKind(after, "handle")
			snap, _ := p.Snapshot(context.Background(), store.Read().Config.OPNsenses[0])
			sr, _ := snap.Find(child.ID, "subdomain")
			hr, _ := snap.Find(handle.ID, "handle")
			if sr.Fields["reverse"] != hikyoParent || sr.Fields["DynDns"] != "1" || hr.Fields["subdomain"] != child.ID || hr.Fields["reverse"] != hikyoParent || hr.Fields["health_uri"] != "/health" {
				t.Fatal("repair lost layout/settings", sr, hr)
			}
			if item := hikyoItem(t, s); item.State != "healthy" {
				t.Fatal(item)
			}
			requireExternalUnchanged(t, before, store.Read().DemoResources["demo-edge"])
		})
	}
}

func TestExistingNativeRecoveryAndContextGuards(t *testing.T) {
	for _, phase := range []string{"after-reservation", "before-enable", "first-apply", "second-apply", "recovery-first", "recovery-second"} {
		t.Run(phase, func(t *testing.T) {
			s, p, store := hikyoService(t)
			importHikyo(t, s)
			wrapper := &importLifecycleProbe{probeProviders: p}
			s.Providers = wrapper
			before := store.Read().DemoResources["demo-edge"]
			plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 609})
			if err != nil {
				t.Fatal(err)
			}
			mutate := func() {
				mutateHikyo(t, store, func(_ *State, r *[]Resource) { changeHikyo(*r, hikyoParent, "DnsChallenge", "0") })
			}
			switch phase {
			case "after-reservation":
				wrapper.afterSet = func(r Resource, _ map[string]string) {
					if r.Kind == "reservation" {
						wrapper.afterSet = nil
						mutate()
					}
				}
			case "before-enable":
				wrapper.afterSet = func(r Resource, f map[string]string) {
					if r.Kind == "subdomain" && f["enabled"] == "0" {
						wrapper.afterSet = nil
						mutate()
					}
				}
			case "first-apply":
				wrapper.afterSet = func(r Resource, f map[string]string) {
					if r.Kind == "subdomain" && f["enabled"] == "1" {
						wrapper.afterSet = nil
						mutate()
					}
				}
			case "second-apply":
				wrapper.afterApply = func() { wrapper.afterApply = nil; mutate() }
			default:
				p.applyFail = true
			}
			op, err := s.ApplyPlan(context.Background(), plan.ID)
			if err == nil || op.Status != "partial" {
				t.Fatal("unsafe context change accepted", op, err)
			}
			if strings.HasPrefix(phase, "recovery") {
				if op.ManualRecovery || op.Step != "reconfigure" {
					t.Fatal("missing recovery binding", op)
				}
				p.applyFail = false
				p.events = nil
				if phase == "recovery-first" {
					mutate()
				} else {
					wrapper.afterApply = func() { wrapper.afterApply = nil; mutate() }
				}
				if _, err = s.Recover(context.Background(), op.ID); err == nil {
					t.Fatal("recovery ignored shared parent change")
				}
			}
			applies := 0
			for _, e := range p.events {
				if strings.HasPrefix(e, "apply:") {
					applies++
				}
				if strings.Contains(e, "domain") && !strings.Contains(e, "subdomain") {
					t.Fatal("parent/standalone write", p.events)
				}
				if strings.Contains(e, ":acl") {
					t.Fatal("external ACL write", p.events)
				}
				if (phase == "after-reservation" || phase == "before-enable") && e == "set:subdomain:1" {
					t.Fatal("enabled child after parent context drift")
				}
			}
			want := 0
			if phase == "second-apply" || phase == "recovery-second" {
				want = 1
			}
			if applies != want {
				t.Fatal("unguarded service application", p.events)
			}
			// Restore only the external edit, then verify a recorded reconfigure can
			// recover. Earlier interrupted writes remain manual recovery boundaries.
			mutateHikyo(t, store, func(_ *State, r *[]Resource) { changeHikyo(*r, hikyoParent, "DnsChallenge", "1") })
			if op.Step == "reconfigure" && !op.ManualRecovery {
				p.events = nil
				if _, err = s.Recover(context.Background(), op.ID); err != nil {
					t.Fatal("cannot recover verified native route", err)
				}
				requireApplyOrder(t, p.events, "apply:kea,apply:caddy")
			}
			requireExternalUnchanged(t, before, store.Read().DemoResources["demo-edge"])
		})
	}
}

func TestExistingNativeCleanupRecoveryOrderAndDependencyProtection(t *testing.T) {
	t.Run("cleanup-recovery", func(t *testing.T) {
		s, p, store := hikyoService(t)
		wrapper := &importLifecycleProbe{probeProviders: p}
		s.Providers = wrapper
		before := store.Read().DemoResources["demo-edge"]
		importHikyo(t, s)
		removeHikyo(t, p)
		plan, err := s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 609})
		if err != nil {
			t.Fatal(err)
		}
		p.applyFail = true
		op, err := s.ApplyPlan(context.Background(), plan.ID)
		if err == nil || op.ManualRecovery || op.Step != "reconfigure" {
			t.Fatal(op, err)
		}
		for n := 0; n < 2; n++ {
			p.events = nil
			if _, err = s.Recover(context.Background(), op.ID); err == nil {
				t.Fatal("failed cleanup recovery succeeded")
			}
			requireApplyOrder(t, p.events, "apply:caddy")
			if _, exists := store.Read().Owned["main:609"]; !exists {
				t.Fatal("failed recovery discarded ownership")
			}
		}
		p.applyFail = false
		p.events = nil
		if _, err = s.Recover(context.Background(), op.ID); err != nil {
			t.Fatal(err)
		}
		requireApplyOrder(t, p.events, "apply:caddy,apply:kea")
		requireExternalUnchanged(t, before, store.Read().DemoResources["demo-edge"])
	})
	for _, relation := range []string{"reverse", "subdomain"} {
		t.Run("foreign-"+relation, func(t *testing.T) {
			s, p, store := hikyoService(t)
			importHikyo(t, s)
			mutateHikyo(t, store, func(_ *State, r *[]Resource) {
				*r = append(*r, Resource{ID: "11111111-2222-4333-8444-555555555555", Kind: "handle", Fields: map[string]string{relation: hikyoChild}})
			})
			if _, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 609, Public: true}); err == nil {
				t.Fatal("maintenance changed shared app route")
			}
			removeHikyo(t, p)
			if _, err := s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 609}); err == nil {
				t.Fatal("cleanup ignored foreign native dependency")
			}
		})
	}
}

func TestExistingStandaloneImportPreservesCertificateAndAdvancedFields(t *testing.T) {
	s, p, store := hikyoService(t)
	mutateHikyo(t, store, func(_ *State, r *[]Resource) {
		for i := range *r {
			if (*r)[i].ID == hikyoChild {
				(*r)[i].Kind = "domain"
				delete((*r)[i].Fields, "reverse")
				(*r)[i].Fields["CustomCertificate"] = "11111111-2222-4333-8444-555555555555"
				(*r)[i].Fields["DnsChallenge"] = "1"
			}
		}
		changeHikyo(*r, hikyoHandle, "reverse", hikyoChild)
		changeHikyo(*r, hikyoHandle, "subdomain", "")
		changeHikyo(*r, hikyoHandle, "health_uri", "/ready")
	})
	before := store.Read().DemoResources["demo-edge"]
	importHikyo(t, s)
	own := store.Read().Owned["main:609"]
	if own.RouteLayout.Kind != "domain" || own.RouteLayout.Parent != nil {
		t.Fatal(own)
	}
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 609, Public: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
		t.Fatal(err)
	}
	snap, _ := p.Snapshot(context.Background(), store.Read().Config.OPNsenses[0])
	domain, _ := snap.Find(hikyoChild, "domain")
	handle, _ := snap.Find(hikyoHandle, "handle")
	if domain.Fields["CustomCertificate"] != "11111111-2222-4333-8444-555555555555" || domain.Fields["DnsChallenge"] != "1" || handle.Fields["health_uri"] != "/ready" {
		t.Fatal("advanced standalone fields changed", domain, handle)
	}
	requireExternalUnchanged(t, before, store.Read().DemoResources["demo-edge"])
}

func TestExistingNativeCanUseGeneratedPrivatePolicyWithoutOwningSharedLists(t *testing.T) {
	s, p, store := hikyoService(t)
	wrapper := &importLifecycleProbe{probeProviders: p}
	s.Providers = wrapper
	importHikyo(t, s)
	mutateHikyo(t, store, func(st *State, _ *[]Resource) { st.Config.OPNsenses[0].PrivateAccessListID = ""; st.Config.Revision++ })
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 609})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
		t.Fatal(err)
	}
	own := store.Read().Owned["main:609"]
	acl, exists := ownedByKind(own, "acl")
	if !exists || acl.ID == clientDemoACL || len(own.Resources) != 4 {
		t.Fatal("wrong private ACL ownership", own)
	}
	removeHikyo(t, p)
	plan, err = s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 609})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
		t.Fatal(err)
	}
	if strings.Join(wrapper.deletes, ",") != "handle:"+hikyoHandle+",subdomain:"+hikyoChild+",acl:"+acl.ID+",reservation:"+hikyoReservation {
		t.Fatal("wrong generated cleanup", wrapper.deletes)
	}
}

func TestExistingNestedAdvancedReservationIsPreservedAndMissingRepairExplained(t *testing.T) {
	s, p, store := hikyoService(t)
	mutateHikyo(t, store, func(_ *State, r *[]Resource) {
		changeHikyo(*r, hikyoReservation, "option_data.example", "opaque-existing-option")
	})
	importHikyo(t, s)
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 609, Public: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
		t.Fatal(err)
	}
	snap, err := p.Snapshot(context.Background(), store.Read().Config.OPNsenses[0])
	if err != nil {
		t.Fatal(err)
	}
	reservation, _ := snap.Find(hikyoReservation, "reservation")
	if reservation.Fields["option_data.example"] != "opaque-existing-option" {
		t.Fatal("advanced reservation option lost", reservation)
	}
	mutateHikyo(t, store, func(_ *State, r *[]Resource) {
		for i, x := range *r {
			if x.ID == hikyoReservation {
				*r = append((*r)[:i], (*r)[i+1:]...)
				break
			}
		}
	})
	p.events = nil
	if _, err = s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 609, Public: true}); err == nil || !strings.Contains(err.Error(), "nested advanced settings") {
		t.Fatal("missing advanced repair lacks exact blocker", err)
	}
	if len(p.events) != 0 {
		t.Fatal("nested repair flattened settings", p.events)
	}
}
