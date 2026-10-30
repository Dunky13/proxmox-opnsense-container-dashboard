package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func externalRequest(t *testing.T, s *Service, ids []string, acknowledged bool) PlanRequest {
	t.Helper()
	st := s.Store.Read()
	refs := []OwnedResource{}
	for _, id := range ids {
		for _, r := range st.DemoResources["demo-edge"] {
			if r.ID == id {
				refs = append(refs, OwnedResource{ID: id, Kind: r.Kind, Fingerprint: r.Fingerprint()})
			}
		}
	}
	raw, _ := json.Marshal(map[string]any{"action": "externalCleanup", "hostId": "main", "vmid": 609, "externalUseAcknowledged": acknowledged, "selectedResources": refs})
	var req PlanRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	return req
}

func TestExternalCleanupReviewedDeletionWithoutOwnership(t *testing.T) {
	s, p, st := hikyoService(t)
	removeHikyo(t, p)
	auth, err := NewAuth(context.Background(), AuthConfig{Demo: true})
	if err != nil {
		t.Fatal(err)
	}
	api := NewAPI(s, auth, nil)
	req := externalRequest(t, s, []string{hikyoReservation, hikyoChild, hikyoHandle}, true)
	raw, _ := json.Marshal(req)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/plans", strings.NewReader(string(raw)))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != 201 {
		t.Fatalf("eligible manual external cleanup rejected: %d %s", w.Code, w.Body.String())
	}
	var plan Plan
	if err = json.Unmarshal(w.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Effects) != 3 {
		t.Fatal(plan.Effects)
	}
	op, err := s.ApplyPlan(context.Background(), plan.ID)
	if err != nil || op.Status != "succeeded" {
		t.Fatal(op, err)
	}
	if len(st.Read().Owned) != 0 {
		t.Fatal("external cleanup silently adopted ownership")
	}
	for _, r := range st.Read().DemoResources["demo-edge"] {
		if r.ID == hikyoReservation || r.ID == hikyoChild || r.ID == hikyoHandle {
			t.Fatal("selected resource retained", r.ID)
		}
	}
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err == nil {
		t.Fatal("consumed plan replayed")
	}
}

func TestExternalCleanupEligibilityAndSelectionProtection(t *testing.T) {
	for _, name := range []string{"no-ack", "empty", "kind", "fingerprint", "duplicate", "owned", "wildcard", "inconsistent-group", "retained-handle", "ambiguous-profile", "wrong-mac", "wrong-subnet", "overlapping-subnet", "unknown-auth", "secret-auth", "shared-acl", "advanced-route", "external-upstream", "multiple-upstreams", "sibling-reference", "foreign-reference-field", "retained-advanced-upstream", "incomplete-host", "stale-inventory", "observed-address-reuse"} {
		t.Run(name, func(t *testing.T) {
			s, p, st := hikyoService(t)
			removeHikyo(t, p)
			ids := []string{hikyoReservation, hikyoChild, hikyoHandle}
			if name == "wildcard" {
				ids = append(ids, hikyoParent)
			}
			if name == "inconsistent-group" {
				ids = []string{hikyoHandle}
			}
			if name == "retained-handle" || name == "retained-advanced-upstream" {
				ids = []string{hikyoReservation}
			}
			mutateHikyo(t, st, func(next *State, rows *[]Resource) {
				switch name {
				case "owned":
					next.Owned["main:609"] = Ownership{Key: "main:609", OPNsenseID: "demo-edge", Resources: []OwnedResource{{ID: hikyoReservation, Kind: "reservation"}}}
				case "ambiguous-profile":
					h := next.Config.Hosts[0]
					h.ID = "duplicate-host"
					next.Config.Hosts = append(next.Config.Hosts, h)
				case "wrong-mac":
					changeHikyo(*rows, hikyoReservation, "hw_address", "d0:cc:37:00:06:10")
				case "wrong-subnet":
					changeHikyo(*rows, hikyoReservation, "subnet", "a2222222-2222-4222-8222-222222222007")
				case "overlapping-subnet":
					*rows = append(*rows, Resource{ID: "c1111111-1111-4111-8111-111111111111", Kind: "subnet", Fields: map[string]string{"subnet": "192.168.6.0/25"}})
				case "unknown-auth":
					for i := range *rows {
						if (*rows)[i].ID == hikyoChild {
							delete((*rows)[i].Fields, "ClientAuthMode")
						}
					}
				case "secret-auth":
					changeHikyo(*rows, hikyoChild, "basicauth", "PRIVATE_AUTH_SENTINEL")
				case "shared-acl":
					changeHikyo(*rows, hikyoChild, "accesslist", next.Config.OPNsenses[0].PrivateAccessListID)
				case "advanced-route":
					changeHikyo(*rows, hikyoHandle, "UnknownAuth", "PRIVATE_AUTH_SENTINEL")
				case "external-upstream":
					changeHikyo(*rows, hikyoHandle, "ToDomain", "192.168.3.240")
				case "multiple-upstreams":
					changeHikyo(*rows, hikyoHandle, "ToDomain", "192.168.6.9,192.168.6.10")
				case "sibling-reference":
					*rows = append(*rows, Resource{ID: "c1111111-1111-4111-8111-111111111111", Kind: "subdomain", Fields: map[string]string{"reverse": hikyoChild}})
				case "foreign-reference-field":
					*rows = append(*rows, Resource{ID: "c1111111-1111-4111-8111-111111111111", Kind: "handle", Fields: map[string]string{"ForeignDependency": hikyoChild}})
				case "retained-advanced-upstream":
					changeHikyo(*rows, hikyoHandle, "ToDomain", "http://192.168.6.9:8080,192.168.6.10")
				}
			})
			if name == "incomplete-host" || name == "stale-inventory" || name == "observed-address-reuse" {
				old := p.inventory
				p.inventory = func(inv Inventory) Inventory {
					inv = old(inv)
					switch name {
					case "incomplete-host":
						inv.Hosts[0].Complete = false
					case "stale-inventory":
						inv.ObservedAt = inv.ObservedAt.Add(-time.Minute)
					case "observed-address-reuse":
						inv.Containers = append(inv.Containers, Container{VMID: 699, IP: "192.168.6.9", MAC: "other"})
					}
					return inv
				}
			}
			req := externalRequest(t, s, ids, name != "no-ack")
			switch name {
			case "empty":
				req.SelectedResources = nil
			case "kind":
				req.SelectedResources[0].Kind = "handle"
			case "fingerprint":
				req.SelectedResources[0].Fingerprint = "stale"
			case "duplicate":
				req.SelectedResources = append(req.SelectedResources, req.SelectedResources[0])
			}
			if _, err := s.Plan(context.Background(), req); err == nil {
				t.Fatal("unsafe external cleanup accepted", name)
			}
			if len(p.events) != 0 || len(st.Read().Operations) != 0 {
				t.Fatal("failed plan wrote")
			}
			raw, _ := json.Marshal(s.Reconcile(context.Background()).ProtectedCandidates)
			if strings.Contains(string(raw), "PRIVATE_AUTH_SENTINEL") {
				t.Fatal("private auth reflected")
			}
		})
	}
}
