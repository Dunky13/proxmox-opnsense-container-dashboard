package dashboard

import (
	"context"
	"strings"
	"testing"
)

func TestReservationOnlyRepairNeedsNoPortAndNeverCreatesProxy(t *testing.T) {
	s, p, st := demoService(t)
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "reservation", HostID: "main", VMID: 604})
	if err != nil {
		t.Fatal(err)
	}
	if plan.DesiredRoute.Port != 0 || len(plan.Effects) != 1 || plan.Effects[0].Kind != "reservation" {
		t.Fatal(plan)
	}
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
		t.Fatal(err)
	}
	own := st.Read().Owned["main:604"]
	if len(own.Resources) != 1 || own.Resources[0].Kind != "reservation" || validRoute(own.DesiredRoute) {
		t.Fatal(own)
	}
	if strings.Join(p.events, ",") != "create:reservation,apply:kea" {
		t.Fatal(p.events)
	}
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err == nil {
		t.Fatal("single use action replayed")
	}
	if _, err = s.Plan(context.Background(), PlanRequest{Action: "reservation", HostID: "main", VMID: 604}); err == nil {
		t.Fatal("existing reservation overwritten")
	}
}

func TestImportedMissingReservationRepairPreservesProxyAndAdvancedFields(t *testing.T) {
	s, p, st := hikyoService(t)
	mutateHikyo(t, st, func(_ *State, rows *[]Resource) { changeHikyo(*rows, hikyoReservation, "next_server", "192.0.2.1") })
	importHikyo(t, s)
	mutateHikyo(t, st, func(_ *State, rows *[]Resource) {
		for i, r := range *rows {
			if r.ID == hikyoReservation {
				*rows = append((*rows)[:i], (*rows)[i+1:]...)
				break
			}
		}
	})
	before := st.Read().DemoResources["demo-edge"]
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "reservation", HostID: "main", VMID: 609})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
		t.Fatal(err)
	}
	after := st.Read().DemoResources["demo-edge"]
	for _, r := range before {
		for _, now := range after {
			if r.ID == now.ID && r.Fingerprint() != now.Fingerprint() {
				t.Fatal("unselected resource changed", r.Kind)
			}
		}
	}
	own := st.Read().Owned["main:609"]
	ref, _ := ownedByKind(own, "reservation")
	snap, err := p.Snapshot(context.Background(), st.Read().Config.OPNsenses[0])
	if err != nil {
		t.Fatal(err)
	}
	r, _ := snap.Find(ref.ID, "reservation")
	if r.Fields["next_server"] != "192.0.2.1" || r.Fields["hostname"] != "hikyo" {
		t.Fatal(r)
	}
}

func TestCleanupBlocksDifferentVMIDUsingOldAddressAndUnselectedForeignIDs(t *testing.T) {
	s, p, st := hikyoService(t)
	importHikyo(t, s)
	removeHikyo(t, p)
	old := p.inventory
	p.inventory = func(inv Inventory) Inventory { inv = old(inv); inv.Containers[0].IP = "192.168.6.9"; return inv }
	if _, err := s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 609}); err == nil {
		t.Fatal("address reuse accepted")
	}
	p.inventory = old
	if _, err := s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 609, ResourceIDs: []string{hikyoParent}}); err == nil {
		t.Fatal("foreign wildcard selected")
	}
	if len(st.Read().Operations) != 1 {
		t.Fatal("blocked selection started operation")
	}
}

func TestCleanupAlreadyAbsentSelectionDoesNotTouchProvider(t *testing.T) {
	s, p, st := hikyoService(t)
	importHikyo(t, s)
	removeHikyo(t, p)
	mutateHikyo(t, st, func(_ *State, rows *[]Resource) {
		for i, r := range *rows {
			if r.ID == hikyoReservation {
				*rows = append((*rows)[:i], (*rows)[i+1:]...)
				break
			}
		}
	})
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 609, ResourceIDs: []string{hikyoReservation}})
	if err != nil {
		t.Fatal(err)
	}
	p.events = nil
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
		t.Fatal(err)
	}
	if len(p.events) != 0 || len(st.Read().Owned["main:609"].Resources) != 2 {
		t.Fatal("absent selection mutated provider or unselected ledger", p.events)
	}
}

func TestReservationCleanupRequiresSelectingLinkedProxy(t *testing.T) {
	s, p, _ := hikyoService(t)
	importHikyo(t, s)
	removeHikyo(t, p)
	_, err := s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 609, ResourceIDs: []string{hikyoReservation}})
	if err == nil {
		t.Fatal("reservation cleanup would release an address while its retained proxy still forwards to it")
	}
}

func TestCleanupRecoveryRejectsReappearedUUIDAndNewForeignReference(t *testing.T) {
	for _, mode := range []string{"reappeared", "foreign-reference", "foreign-address"} {
		t.Run(mode, func(t *testing.T) {
			s, p, st := hikyoService(t)
			importHikyo(t, s)
			removeHikyo(t, p)
			ids := []string{hikyoHandle, hikyoChild}
			if mode == "foreign-address" {
				ids = nil
			}
			plan, err := s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 609, ResourceIDs: ids})
			if err != nil {
				t.Fatal(err)
			}
			p.applyFail = true
			op, err := s.ApplyPlan(context.Background(), plan.ID)
			if err == nil || op.Step != "reconfigure" || op.ManualRecovery {
				t.Fatal(op, err)
			}
			mutateHikyo(t, st, func(_ *State, rows *[]Resource) {
				id := "11111111-2222-4333-8444-555555555555"
				if mode == "reappeared" {
					id = hikyoHandle
				}
				fields := map[string]string{"subdomain": hikyoChild}
				if mode == "foreign-address" {
					fields = map[string]string{"ToDomain": "192.168.6.9"}
				}
				*rows = append(*rows, Resource{ID: id, Kind: "handle", Fields: fields})
			})
			p.applyFail = false
			p.events = nil
			if _, err = s.Recover(context.Background(), op.ID); err == nil {
				t.Fatal("unsafe cleanup recovery accepted")
			}
			if len(p.events) != 0 {
				t.Fatal("unsafe reconfiguration", p.events)
			}
		})
	}
}

func TestEmptyCleanupSelectionAndOperationJobNonoverlap(t *testing.T) {
	s, p, st := demoService(t)
	ensureDemo(t, s)
	p.inventory = withoutDemoCT
	if _, err := s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 604, ResourceIDs: []string{}}); err == nil {
		t.Fatal("empty selection expanded to all resources")
	}
	if err := st.Update(func(next *State) error {
		next.Jobs["pending"] = Job{ID: "pending", HostID: "main", Key: "main:612", Status: "interrupted"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 604}); err == nil {
		t.Fatal("cleanup overlapped uncertain update")
	}
}
