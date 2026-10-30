package dashboard

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestExternalOrphanHintsRemainProtectedAndRequireCompleteObservation(t *testing.T) {
	s, p, st := hikyoService(t)
	for _, candidate := range s.Reconcile(context.Background()).ProtectedCandidates {
		if candidate.Eligible && candidate.VMID == 609 {
			t.Fatal("present container classified as eligible orphan")
		}
	}
	removeHikyo(t, p)
	mutateHikyo(t, st, func(_ *State, rows *[]Resource) { changeHikyo(*rows, hikyoChild, "basicauth", "PRIVATE_AUTH_SENTINEL") })
	result := s.Reconcile(context.Background())
	ids := map[string]bool{}
	for _, candidate := range result.ProtectedCandidates {
		ids[candidate.Resource.ID] = true
		if candidate.Resource.ID != hikyoReservation && candidate.Eligible {
			t.Fatal(candidate)
		}
	}
	if !ids[hikyoReservation] || !ids[hikyoChild] || !ids[hikyoParent] {
		t.Fatal("profile/domain hint or wildcard protection", ids)
	}
	raw, _ := json.Marshal(result.ProtectedCandidates)
	if strings.Contains(string(raw), "PRIVATE_AUTH_SENTINEL") {
		t.Fatal("authentication value leaked")
	}
	if len(p.events) > 0 || len(st.Read().Owned) > 0 || len(st.Read().Operations) > 0 {
		t.Fatal("discovery mutated or claimed external records")
	}
	old := p.inventory
	p.inventory = func(inv Inventory) Inventory { inv = old(inv); inv.Complete = false; return inv }
	if len(s.Reconcile(context.Background()).ProtectedCandidates) != 0 {
		t.Fatal("incomplete inventory asserted orphan hints")
	}
}
