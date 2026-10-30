package dashboard

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type reviewSnapshotProviders struct {
	*probeProviders
	observe func(OPNsense, ProviderSnapshot) ProviderSnapshot
	deleted func(Resource)
}

func (p *reviewSnapshotProviders) Delete(ctx context.Context, o OPNsense, r Resource) error {
	err := p.DemoProviders.Delete(ctx, o, r)
	if err == nil && p.deleted != nil {
		p.deleted(r)
	}
	return err
}

func (p *reviewSnapshotProviders) Snapshot(ctx context.Context, o OPNsense) (ProviderSnapshot, error) {
	snap, err := p.probeProviders.Snapshot(ctx, o)
	if err == nil && p.observe != nil {
		snap = p.observe(o, snap)
	}
	return snap, err
}

func addOwnedCleanupOtherRouter(t *testing.T, st *Store, reference bool) {
	t.Helper()
	if err := st.Update(func(next *State) error {
		o := next.Config.OPNsenses[0]
		o.ID = "other-firewall"
		next.Config.OPNsenses = append(next.Config.OPNsenses, o)
		for _, r := range next.DemoResources["demo-edge"] {
			if r.ID != hikyoReservation && r.ID != hikyoChild && r.ID != hikyoHandle {
				next.DemoResources[o.ID] = append(next.DemoResources[o.ID], clone(r))
			}
		}
		if reference {
			next.DemoResources[o.ID] = append(next.DemoResources[o.ID], Resource{ID: "c1111111-1111-4111-8111-111111111111", Kind: "handle", Fields: map[string]string{"ToDomain": "192.168.6.9"}})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReviewOwnedCleanupReadsOtherFirewall(t *testing.T) {
	s, p, st := hikyoService(t)
	importHikyo(t, s)
	removeHikyo(t, p)
	addOwnedCleanupOtherRouter(t, st, true)
	if _, err := s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 609}); err == nil {
		t.Fatal("owned reservation cleanup ignored a retained other-firewall proxy")
	}
}

func TestReviewOwnedCleanupRejectsReadinessLossBeforeDelete(t *testing.T) {
	s, p, st := hikyoService(t)
	importHikyo(t, s)
	removeHikyo(t, p)
	reads := 0
	s.Providers = &reviewSnapshotProviders{probeProviders: p, observe: func(_ OPNsense, snap ProviderSnapshot) ProviderSnapshot {
		reads++
		if reads >= 3 {
			snap.Ready = false
		}
		return snap
	}}
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 609})
	if err != nil {
		t.Fatal(err)
	}
	before := fingerprint(st.Read().DemoResources)
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err == nil {
		t.Fatal("cleanup ignored stopped provider prerequisites")
	}
	if before != fingerprint(st.Read().DemoResources) {
		t.Fatal("deleted a resource after provider readiness disappeared")
	}
}

func TestReviewOwnedCleanupOtherFirewallFreshGuards(t *testing.T) {
	for _, phase := range []string{"admission", "between-deletes", "application", "recovery"} {
		t.Run(phase, func(t *testing.T) {
			s, p, st := hikyoService(t)
			importHikyo(t, s)
			removeHikyo(t, p)
			addOwnedCleanupOtherRouter(t, st, false)
			reference := false
			providers := &reviewSnapshotProviders{probeProviders: p, observe: func(o OPNsense, snap ProviderSnapshot) ProviderSnapshot {
				if o.ID == "other-firewall" && reference {
					snap.Resources = append(snap.Resources, Resource{ID: "c1111111-1111-4111-8111-111111111111", Kind: "handle", Fields: map[string]string{"ToDomain": "192.168.6.9"}})
				}
				return snap
			}}
			s.Providers = providers
			plan, err := s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 609})
			if err != nil {
				t.Fatal(err)
			}
			if phase == "admission" {
				reference = true
			}
			providers.deleted = func(r Resource) {
				if (phase == "between-deletes" && r.Kind == "handle") || (phase == "application" && r.Kind == "subdomain") {
					reference = true
				}
			}
			if phase == "recovery" {
				p.applyFail = true
			}
			op, err := s.ApplyPlan(context.Background(), plan.ID)
			if err == nil {
				t.Fatal("cleanup ignored other-firewall reference", phase)
			}
			if phase == "recovery" {
				reference = true
				p.applyFail = false
				p.events = nil
				if _, err := s.Recover(context.Background(), op.ID); err == nil || !strings.Contains(err.Error(), "other-firewall") {
					t.Fatal("recovery did not reject retained foreign handle", err)
				}
				if len(p.events) != 0 {
					t.Fatal("recovery applied a service before rejecting the foreign reference", p.events)
				}
				if st.Read().Operations[op.ID].Status == "succeeded" {
					t.Fatal("failed recovery discarded partial evidence")
				}
				return // The row was staged absent before this new reference; KEA must remain unapplied.
			}
			for _, r := range st.Read().DemoResources["demo-edge"] {
				if r.ID == hikyoReservation {
					for _, event := range p.events {
						if event == "apply:kea" {
							t.Fatal("applied KEA despite reference", phase)
						}
					}
					return
				}
			}
			t.Fatal("released referenced reservation", phase)
		})
	}
}

func TestReviewReservationRecoveryRechecksCollisions(t *testing.T) {
	for _, collision := range []string{"container", "reservation", "readiness"} {
		t.Run(collision, func(t *testing.T) {
			s, p, st := demoService(t)
			plan, err := s.Plan(context.Background(), PlanRequest{Action: "reservation", HostID: "main", VMID: 604})
			if err != nil {
				t.Fatal(err)
			}
			p.applyFail = true
			op, err := s.ApplyPlan(context.Background(), plan.ID)
			if err == nil || op.Step != "reconfigure" {
				t.Fatal("expected recoverable KEA failure", op, err)
			}
			p.applyFail, p.events = false, nil
			switch collision {
			case "container":
				p.inventory = func(inv Inventory) Inventory {
					inv.Containers = append(inv.Containers, Container{Key: "main:612", HostID: "main", VMID: 612, IP: "192.168.6.4", MAC: "d0:cc:37:00:06:12"})
					return inv
				}
			case "reservation":
				if err := st.Update(func(next *State) error {
					next.DemoResources["demo-edge"] = append(next.DemoResources["demo-edge"], Resource{ID: "c2222222-2222-4222-8222-222222222222", Kind: "reservation", Fields: map[string]string{"ip_address": "192.168.6.4"}})
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			case "readiness":
				s.Providers = &reviewSnapshotProviders{probeProviders: p, observe: func(_ OPNsense, snap ProviderSnapshot) ProviderSnapshot { snap.Ready = false; return snap }}
			}
			if _, err := s.Recover(context.Background(), op.ID); err == nil {
				t.Fatal("recovery ignored fresh", collision)
			}
			if len(p.events) != 0 {
				t.Fatal("applied KEA before rejecting collision", p.events)
			}
		})
	}
}

func TestReviewReservationCreationRetainsUnownedProxy(t *testing.T) {
	s, p, st := hikyoService(t)
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
		t.Fatal("an unowned proxy blocked independent reservation creation", err)
	}
	if _, err := s.ApplyPlan(context.Background(), plan.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := p.Snapshot(context.Background(), st.Read().Config.OPNsenses[0])
	for _, r := range before {
		now, ok := after.Find(r.ID, r.Kind)
		if !ok || now.Fingerprint() != r.Fingerprint() {
			t.Fatal("unselected manual proxy/context changed")
		}
	}
}

func TestReviewOwnedOrphanAllowsIndependentSubsetReview(t *testing.T) {
	s, p, st := hikyoService(t)
	importHikyo(t, s)
	removeHikyo(t, p)
	mutateHikyo(t, st, func(_ *State, rows *[]Resource) {
		*rows = append(*rows, Resource{ID: "c3333333-3333-4333-8333-333333333333", Kind: "handle", Fields: map[string]string{"ToDomain": "192.168.6.9"}})
	})
	for _, item := range s.Reconcile(context.Background()).Items {
		if item.Key == "main:609" {
			raw, _ := json.Marshal(item)
			if !strings.Contains(string(raw), `"cleanupSelectable":true`) {
				t.Fatal("whole-app dependency hid verified independent proxy selection", string(raw))
			}
			return
		}
	}
	t.Fatal("orphan absent")
}

func TestReviewExternalRecoveryAllowsCredentialRepair(t *testing.T) {
	s, f, _ := externalHTTPService(t)
	plan := httpExternalPlan(t, s, hikyoReservation, hikyoChild, hikyoHandle)
	f.failService = "caddy"
	op, err := s.ApplyPlan(context.Background(), plan.ID)
	if err == nil {
		t.Fatal("expected partial cleanup")
	}
	state := s.Store.Read()
	edit := ConfigEdit{Revision: state.Config.Revision}
	for _, h := range state.Config.Hosts {
		edit.Hosts = append(edit.Hosts, HostEdit{Host: h})
	}
	secret := "repaired-secret"
	for _, o := range state.Config.OPNsenses {
		edit.OPNsenses = append(edit.OPNsenses, OPNsenseEdit{OPNsense: o, Secret: &secret})
	}
	if _, err := s.UpdateConfig(edit); err != nil {
		t.Fatal(err)
	}
	f.failService = ""
	recovery, err := s.Plan(context.Background(), recoveryRequest(op))
	if err != nil {
		t.Fatal("credential repair stranded recovery", err)
	}
	if _, err := s.ApplyPlan(context.Background(), recovery.ID); err != nil {
		t.Fatal(err)
	}
}
