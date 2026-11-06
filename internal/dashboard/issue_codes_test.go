package dashboard

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

var knownIssueCodes = map[string]bool{
	IssueReadIncomplete: true, IssueRouteUnavailable: true, IssueSubnetUnresolved: true,
	IssueSubnetChanged: true, IssueIdentityChanged: true, IssueOwnedChanged: true,
	IssueHostProfileMismatch: true, IssueRouteLayoutChanged: true, IssueForeignReferences: true,
	IssueRouteSpecInvalid: true, IssueSharedAccessListChanged: true, IssueOwnedMissing: true,
	IssueOwnedDiffers: true, IssueUnownedConflict: true, IssueNotConfigured: true,
	IssueImportBlocker: true, IssueOperationIncomplete: true, IssueVMIDReused: true,
	IssuePrerequisitesMissing: true, IssueCleanupConflict: true, IssueOwnedOrphan: true,
}

// requireIssueCodes checks the wire contract: every prose issue has a stable,
// known code at the same position, and both survive JSON encoding.
func requireIssueCodes(t *testing.T, r Reconciliation) {
	t.Helper()
	for _, item := range r.Items {
		if len(item.Issues) != len(item.IssueDetails) {
			t.Fatalf("%s: %d issues but %d details", item.Key, len(item.Issues), len(item.IssueDetails))
		}
		for i, detail := range item.IssueDetails {
			if detail.Message != item.Issues[i] || !knownIssueCodes[detail.Code] {
				t.Fatalf("%s: issue %q has detail %+v", item.Key, item.Issues[i], detail)
			}
		}
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"issueDetails":null`) {
		t.Fatal("issueDetails must serialize as an array, not null")
	}
}

func itemCodes(t *testing.T, r Reconciliation, key string) []string {
	t.Helper()
	requireIssueCodes(t, r)
	for _, item := range r.Items {
		if item.Key == key {
			codes := []string{}
			for _, detail := range item.IssueDetails {
				codes = append(codes, detail.Code)
			}
			return codes
		}
	}
	t.Fatalf("missing item %s", key)
	return nil
}

func requireCodes(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("issue codes = %v, want %v", got, want)
	}
}

func TestIssueCodesAcrossReconciliationStates(t *testing.T) {
	t.Run("not configured", func(t *testing.T) {
		s, _, _ := demoService(t)
		requireCodes(t, itemCodes(t, s.Reconcile(context.Background()), "main:604"), IssueNotConfigured)
	})
	t.Run("unowned host profile mismatch", func(t *testing.T) {
		s, p, _ := demoService(t)
		p.inventory = func(i Inventory) Inventory {
			for n := range i.Containers {
				if i.Containers[n].VMID == 604 {
					i.Containers[n].MAC = "02:00:00:00:00:01"
				}
			}
			return i
		}
		requireCodes(t, itemCodes(t, s.Reconcile(context.Background()), "main:604"), IssueHostProfileMismatch)
	})
	t.Run("owned host profile mismatch", func(t *testing.T) {
		s, p, _ := demoService(t)
		ensureDemo(t, s)
		p.inventory = func(i Inventory) Inventory {
			for n := range i.Containers {
				if i.Containers[n].VMID == 604 {
					i.Containers[n].MAC = "02:00:00:00:00:01"
				}
			}
			return i
		}
		codes := itemCodes(t, s.Reconcile(context.Background()), "main:604")
		found := false
		for _, code := range codes {
			found = found || code == IssueHostProfileMismatch
		}
		if !found {
			t.Fatalf("owned profile mismatch lost its code: %v", codes)
		}
	})
	t.Run("owned orphan and read failure", func(t *testing.T) {
		s, p, _ := demoService(t)
		ensureDemo(t, s)
		p.inventory = withoutDemoCT
		requireCodes(t, itemCodes(t, s.Reconcile(context.Background()), "main:604"), IssueOwnedOrphan)
		p.readFail = true
		requireCodes(t, itemCodes(t, s.Reconcile(context.Background()), "main:604"), IssueReadIncomplete)
	})
	t.Run("incomplete inventory", func(t *testing.T) {
		s, p, _ := demoService(t)
		p.inventory = func(i Inventory) Inventory { i.Complete = false; return i }
		requireCodes(t, itemCodes(t, s.Reconcile(context.Background()), "main:604"), IssueReadIncomplete)
	})
	t.Run("unfinished operation", func(t *testing.T) {
		s, p, _ := demoService(t)
		p.applyFail = true
		plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = s.ApplyPlan(context.Background(), plan.ID)
		p.applyFail = false
		codes := itemCodes(t, s.Reconcile(context.Background()), "main:604")
		if len(codes) == 0 || codes[len(codes)-1] != IssueOperationIncomplete {
			t.Fatalf("unfinished operation is not coded last: %v", codes)
		}
	})
	t.Run("importable existing configuration", func(t *testing.T) {
		s, _, _ := hikyoService(t)
		requireCodes(t, itemCodes(t, s.Reconcile(context.Background()), "main:609"), IssueUnownedConflict)
	})
	t.Run("blocked import carries per-blocker codes", func(t *testing.T) {
		s, _, store := hikyoService(t)
		mutateHikyo(t, store, func(_ *State, rows *[]Resource) {
			kept := []Resource{}
			for _, r := range *rows {
				if r.ID != hikyoHandle {
					kept = append(kept, r)
				}
			}
			*rows = kept
		})
		codes := itemCodes(t, s.Reconcile(context.Background()), "main:609")
		blockers := 0
		for _, code := range codes {
			if code == IssueImportBlocker {
				blockers++
			}
		}
		if blockers == 0 || codes[0] != IssueUnownedConflict {
			t.Fatalf("blocked import lost its codes: %v", codes)
		}
	})
}
