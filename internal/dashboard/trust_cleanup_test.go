package dashboard

import (
	"context"
	"testing"
)

type unreadableTrustProvider struct {
	*probeProviders
	mask        bool
	invalid     bool
	afterDelete bool
}

func (p *unreadableTrustProvider) Snapshot(ctx context.Context, o OPNsense) (ProviderSnapshot, error) {
	snap, err := p.probeProviders.Snapshot(ctx, o)
	if p.mask {
		for i, r := range snap.Resources {
			if r.Kind == "caddyGeneral" {
				if p.invalid {
					snap.Resources[i].Fields["accesslist"] = "invalid-identity"
				} else {
					delete(snap.Resources[i].Fields, "accesslist")
				}
			}
		}
	}
	return snap, err
}
func (p *unreadableTrustProvider) Delete(ctx context.Context, o OPNsense, r Resource) error {
	err := p.DemoProviders.Delete(ctx, o, r)
	if err == nil && p.afterDelete {
		p.mask = true
	}
	return err
}

func TestOwnedACLCleanupRequiresObservedValidGlobalTrust(t *testing.T) {
	for _, phase := range []string{"planning", "invalid-identity", "during-cleanup"} {
		t.Run(phase, func(t *testing.T) {
			s, p, store := demoService(t)
			ensureDemo(t, s)
			p.inventory = withoutDemoCT
			own := store.Read().Owned["main:604"]
			ref, ok := ownedByKind(own, "acl")
			if !ok {
				t.Fatal("missing generated ACL fixture")
			}
			wrapper := &unreadableTrustProvider{probeProviders: p, mask: phase != "during-cleanup", invalid: phase == "invalid-identity", afterDelete: phase == "during-cleanup"}
			s.Providers = wrapper
			plan, err := s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 604})
			if phase != "during-cleanup" {
				if err == nil {
					t.Fatal("cleanup accepted unknown global reference")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.ApplyPlan(context.Background(), plan.ID); err == nil {
				t.Fatal("cleanup deleted ACL after trust observation failed")
			}
			found := false
			for _, r := range store.Read().DemoResources["demo-edge"] {
				if r.Kind == "acl" && r.ID == ref.ID {
					found = true
				}
			}
			if !found {
				t.Fatal("owned ACL was deleted under unknown global trust")
			}
		})
	}
}

func TestUnknownGlobalTrustDoesNotBlockCleanupWithoutAnACLToDelete(t *testing.T) {
	for _, mode := range []string{"shared-policy", "proven-absent-acl"} {
		t.Run(mode, func(t *testing.T) {
			s, p, store := demoService(t)
			if mode == "shared-policy" {
				selectShared(t, store, sharedDemoACL)
			}
			ensureDemo(t, s)
			p.inventory = withoutDemoCT
			if mode == "proven-absent-acl" {
				ref, _ := ownedByKind(store.Read().Owned["main:604"], "acl")
				router := store.Read().Config.OPNsenses[0]
				snap, err := p.Snapshot(context.Background(), router)
				if err != nil {
					t.Fatal(err)
				}
				r, _ := snap.Find(ref.ID, "acl")
				if err = p.DemoProviders.Delete(context.Background(), router, r); err != nil {
					t.Fatal(err)
				}
			}
			s.Providers = &unreadableTrustProvider{probeProviders: p, mask: true}
			plan, err := s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 604})
			if err != nil {
				t.Fatal("unrelated cleanup blocked", err)
			}
			if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUnknownGlobalTrustDoesNotBlockReservationRepair(t *testing.T) {
	for _, mode := range []string{"missing", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			s, p, store := demoService(t)
			ensureDemo(t, s)
			own := store.Read().Owned["main:604"]
			acl, _ := ownedByKind(own, "acl")
			reservation, _ := ownedByKind(own, "reservation")
			o := store.Read().Config.OPNsenses[0]
			snap, err := p.Snapshot(context.Background(), o)
			if err != nil {
				t.Fatal(err)
			}
			r, _ := snap.Find(reservation.ID, "reservation")
			if err = p.DemoProviders.Delete(context.Background(), o, r); err != nil {
				t.Fatal(err)
			}
			s.Providers = &unreadableTrustProvider{probeProviders: p, mask: true, invalid: mode == "malformed"}
			plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604})
			if err != nil {
				t.Fatal("reservation repair blocked by unrelated global trust", err)
			}
			if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
				t.Fatal(err)
			}
			current := store.Read().Owned[own.Key]
			repaired, ok := ownedByKind(current, "reservation")
			if !ok || repaired.ID == reservation.ID {
				t.Fatal("reservation was not recreated")
			}
			retained, ok := ownedByKind(current, "acl")
			if !ok || retained != acl {
				t.Fatal("unrelated ACL changed", retained)
			}
		})
	}
}
