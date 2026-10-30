package dashboard

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestOwnedHostnameDriftAcceptIsReviewedMetadataOnly(t *testing.T) {
	s, p, st := hikyoService(t)
	importHikyo(t, s)
	mutateHikyo(t, st, func(_ *State, rows *[]Resource) { changeHikyo(*rows, hikyoReservation, "hostname", "hikyo") })
	item := hikyoItem(t, s)
	if !item.CanAcceptChanges || len(item.Drift) != 1 || item.Drift[0].ID != hikyoReservation || len(item.Drift[0].Changes) != 1 || item.Drift[0].Changes[0].Before != "speedtest-tracker" {
		t.Fatalf("%+v", item)
	}
	before := fingerprint(st.Read().DemoResources)
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "accept", HostID: "main", VMID: 609, Public: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Effects) != 1 || plan.Effects[0].Action != "acknowledge" {
		t.Fatal(plan)
	}
	op, err := s.ApplyPlan(context.Background(), plan.ID)
	if err != nil || op.Status != "succeeded" {
		t.Fatal(op, err)
	}
	if before != fingerprint(st.Read().DemoResources) || len(p.events) != 0 {
		t.Fatal("metadata accept wrote provider")
	}
	if item = hikyoItem(t, s); item.State != "healthy" {
		t.Fatal(item)
	}
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err == nil {
		t.Fatal("accept replayed")
	}
}

func TestOwnedAcceptBlocksSignificantUnknownMissingAndStaleChanges(t *testing.T) {
	for _, field := range []string{"subnet", "hw_address", "ip_address", "authSecret", "hostname"} {
		t.Run(field, func(t *testing.T) {
			s, _, st := hikyoService(t)
			importHikyo(t, s)
			mutateHikyo(t, st, func(_ *State, rows *[]Resource) {
				changeHikyo(*rows, hikyoReservation, "hostname", "hikyo")
				changeHikyo(*rows, hikyoReservation, field, "PRIVATE_SENTINEL")
			})
			item := hikyoItem(t, s)
			if item.CanAcceptChanges {
				t.Fatal("unsafe accept")
			}
			if field == "authSecret" {
				raw, _ := json.Marshal(item)
				if strings.Contains(string(raw), "PRIVATE_SENTINEL") {
					t.Fatal("secret reflected")
				}
			}
			if _, err := s.Plan(context.Background(), PlanRequest{Action: "accept", HostID: "main", VMID: 609, Public: true}); err == nil {
				t.Fatal("unsafe plan")
			}
		})
	}
	s, _, st := hikyoService(t)
	importHikyo(t, s)
	mutateHikyo(t, st, func(_ *State, rows *[]Resource) { changeHikyo(*rows, hikyoReservation, "hostname", "hikyo") })
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "accept", HostID: "main", VMID: 609, Public: true})
	if err != nil {
		t.Fatal(err)
	}
	mutateHikyo(t, st, func(_ *State, rows *[]Resource) { changeHikyo(*rows, hikyoHandle, "ToPort", "9999") })
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err == nil {
		t.Fatal("stale accept")
	}
}

func TestSelectedCleanupRetainsUnselectedReservation(t *testing.T) {
	s, p, st := hikyoService(t)
	importHikyo(t, s)
	removeHikyo(t, p)
	if _, err := s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 609, ResourceIDs: []string{hikyoChild}}); err == nil {
		t.Fatal("retained handle lost parent")
	}
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 609, ResourceIDs: []string{hikyoHandle, hikyoChild}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Effects) != 2 {
		t.Fatal(plan.Effects)
	}
	for _, effect := range plan.Effects {
		if effect.ResourceID == hikyoReservation {
			t.Fatal("unselected reservation effect")
		}
	}
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
		t.Fatal(err)
	}
	own := st.Read().Owned["main:609"]
	if len(own.Resources) != 1 || own.Resources[0].ID != hikyoReservation || !own.Public {
		t.Fatal("unselected ledger or route intent lost", own)
	}
}
