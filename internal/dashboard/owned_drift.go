package dashboard

import (
	"context"
	"sort"
	"time"
)

type ResourceDrift struct {
	ID      string        `json:"id"`
	Kind    string        `json:"kind"`
	Status  string        `json:"status"`
	Changes []FieldChange `json:"changes"`
	Detail  string        `json:"detail"`
}

func ownedDrift(own Ownership, snap ProviderSnapshot) []ResourceDrift {
	out := []ResourceDrift{}
	for _, ref := range own.Resources {
		now, exists := snap.Find(ref.ID, ref.Kind)
		if exists && now.Fingerprint() == ref.Fingerprint {
			continue
		}
		d := ResourceDrift{ID: ref.ID, Kind: ref.Kind, Status: "changed", Changes: []FieldChange{}, Detail: "Full resource fingerprint changed; unlisted fields remain private."}
		if !exists {
			d.Status = "missing"
			d.Detail = "Recorded UUID is absent. Review adding the missing resource; replacements are never adopted automatically."
		} else {
			for _, before := range own.ResourceTemplates {
				if before.ID != ref.ID || before.Kind != ref.Kind || before.Fingerprint() != ref.Fingerprint {
					continue
				}
				for _, field := range observationFields[ref.Kind] {
					if before.Fields[field] != now.Fields[field] {
						d.Changes = append(d.Changes, FieldChange{effectField(field), safeObservation(effectValue(field, before.Fields[field])), safeObservation(effectValue(field, now.Fields[field]))})
					}
				}
			}
		}
		out = append(out, d)
	}
	return out
}

// Acknowledge only an independently corrected reservation hostname. Every other
// byte, UUID, relation, upstream and exposure must remain acknowledged and safe.
// This intentionally narrow rebase cannot adopt foreign resources or auth state.
func reviewOwnedChanges(st State, inv Inventory, h Host, o OPNsense, own Ownership, snap ProviderSnapshot) (Ownership, []PlanEffect, error) {
	if err := checkedSnapshot(snap); err != nil {
		return own, nil, err
	}
	c, err := ensureContainer(inv, h, own.VMID)
	if err != nil {
		return own, nil, err
	}
	if own.Identity == "" || c.Identity != own.Identity {
		return own, nil, conflict("container identity changed")
	}
	desired, err := desiredFields(o, c, h, own.Public)
	if err != nil {
		return own, nil, err
	}
	if err = targetConflicts(inv, c, desired); err != nil {
		return own, nil, err
	}
	if err = checkConflicts(snap, own, desired); err != nil {
		return own, nil, err
	}
	if err = verifyRouteLayout(st, o, own, snap); err != nil {
		return own, nil, err
	}
	if err = cleanupReferences(own, snap); err != nil {
		return own, nil, err
	}
	if err = sharedContextNotOwned(st, o, snap, own.Public); err != nil {
		return own, nil, err
	}
	if err = verifySharedAccessList(o, snap, own.SharedAccessList, own.Public); err != nil {
		return own, nil, err
	}
	if err = verifySubnet(snap, h, c, own.ResolvedSubnet); err != nil {
		return own, nil, err
	}
	own = clone(own)
	effects := []PlanEffect{}
	for i, ref := range own.Resources {
		now, exists := snap.Find(ref.ID, ref.Kind)
		if !exists || ref.Fingerprint == "" {
			return own, nil, conflict("missing or uncertain UUID cannot be acknowledged")
		}
		if now.Fingerprint() == ref.Fingerprint {
			continue
		}
		matched := false
		for j, before := range own.ResourceTemplates {
			if before.Kind != ref.Kind || before.ID != ref.ID || before.Fingerprint() != ref.Fingerprint {
				continue
			}
			if ref.Kind != "reservation" || now.Fields["hostname"] != c.Name {
				return own, nil, conflict("only a reservation hostname corrected to the current container name can be accepted")
			}
			expected := clone(before)
			expected.Fields["hostname"] = now.Fields["hostname"]
			if expected.Fingerprint() != now.Fingerprint() {
				return own, nil, conflict("other provider fields changed; metadata acknowledgement is unsafe")
			}
			effects = append(effects, PlanEffect{Kind: ref.Kind, ResourceID: ref.ID, Action: "acknowledge", Name: "Accept observed reservation hostname in ownership only", Changes: []FieldChange{{"hostname", safeObservation(before.Fields["hostname"]), safeObservation(now.Fields["hostname"])}}})
			own.Resources[i].Fingerprint = now.Fingerprint()
			own.ResourceTemplates[j] = clone(now)
			matched = true
		}
		if !matched {
			return own, nil, conflict("verified private template is unavailable for this changed resource")
		}
	}
	if len(effects) == 0 {
		return own, nil, conflict("no compatible observed hostname change to accept")
	}
	o.UpstreamScheme, o.UpstreamPort = own.DesiredRoute.Scheme, own.DesiredRoute.Port
	if !desiredResourcesMatch(o, h, c, own, snap, own.Public) {
		return own, nil, conflict("observed network, upstream or exposure differs from recorded desired configuration")
	}
	sort.Slice(effects, func(i, j int) bool { return effects[i].ResourceID < effects[j].ResourceID })
	return own, effects, nil
}

func (s *Service) acceptPlan(st State, req PlanRequest, h Host, o OPNsense, inv Inventory, snap ProviderSnapshot) (Plan, error) {
	own, ok := st.Owned[key(h.ID, req.VMID)]
	if !ok {
		return Plan{}, conflict("no owned configuration")
	}
	if req.UpstreamScheme != nil || req.UpstreamPort != nil || req.Public != own.Public {
		return Plan{}, bad("accept does not permit changing route or exposure")
	}
	updated, effects, err := reviewOwnedChanges(st, inv, h, o, own, snap)
	if err != nil {
		return Plan{}, err
	}
	now := time.Now().UTC()
	p := Plan{ID: newID(), Action: "accept", Key: own.Key, HostID: h.ID, VMID: own.VMID, OPNsenseID: o.ID, Public: own.Public, DesiredRoute: updated.DesiredRoute, Effects: effects, ApplyNotes: []string{"Records only the reviewed hostname fingerprint and private repair template. No provider writes or service reconfiguration.", "Existing route intent, advanced fields, shared parent and policy remain unchanged."}, Revision: st.Config.Revision, InventoryHash: inv.StableHash(), ProviderHash: fingerprint(snap), Identity: own.Identity, CreatedAt: now, ExpiresAt: now.Add(5 * time.Minute), OwnershipHash: fingerprint(st.Owned), ConfigHash: fingerprint(st.Config)}
	return p, s.savePlan(p, now)
}

func (s *Service) applyAccept(ctx context.Context, st State, p Plan, h Host, o OPNsense, inv Inventory, snap ProviderSnapshot) (Operation, error) {
	own, _, err := reviewOwnedChanges(st, inv, h, o, st.Owned[p.Key], snap)
	if err != nil {
		return Operation{}, err
	}
	now := time.Now().UTC()
	op := Operation{ID: newID(), PlanID: p.ID, Key: p.Key, OPNsenseID: o.ID, Action: "accept", Status: "succeeded", Step: "complete", CreatedAt: now, UpdatedAt: now, Resources: own.Resources}
	err = s.Store.Update(func(next *State) error {
		delete(next.Plans, p.ID)
		next.Owned[p.Key] = own
		next.Operations[op.ID] = op
		return nil
	})
	return op, err
}
