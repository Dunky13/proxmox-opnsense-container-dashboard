package dashboard

import (
	"context"
	"strings"
	"time"
)

// Reservation repair has no upstream or exposure input. It can add one missing
// reservation without configuring a proxy or inventing an application port.
func reservationFields(h Host, o OPNsense, c Container, own Ownership, snap ProviderSnapshot) (map[string]string, *SubnetReference, error) {
	subnet, err := resolveSubnet(snap, h, c)
	if err != nil {
		return nil, nil, err
	}
	d, err := desiredFields(o, c, h, false)
	if err != nil {
		return nil, nil, err
	}
	fields := map[string]string{"subnet": subnet.ID, "hw_address": d["mac"], "ip_address": d["ip"], "hostname": d["name"], "description": "container-dashboard " + c.Key}
	for _, template := range own.ResourceTemplates {
		if template.Kind != "reservation" {
			continue
		}
		fields = clone(template.Fields)
		for field := range fields {
			if strings.Contains(field, ".") {
				return nil, nil, conflict("nested advanced reservation settings require provider review")
			}
		}
		for k, v := range map[string]string{"subnet": subnet.ID, "hw_address": d["mac"], "ip_address": d["ip"], "hostname": d["name"]} {
			fields[k] = v
		}
	}
	return fields, subnet, nil
}

func reservationTarget(st State, h Host, o OPNsense, vmid int, inv Inventory, snap ProviderSnapshot) (Container, Ownership, map[string]string, *SubnetReference, error) {
	c, err := ensureContainer(inv, h, vmid)
	if err != nil {
		return c, Ownership{}, nil, nil, err
	}
	own, owned := st.Owned[c.Key]
	if owned && own.Identity != c.Identity {
		return c, own, nil, nil, conflict("VMID reuse or migration blocks repair")
	}
	if _, err = existingOwnedResources(own, snap); err != nil {
		return c, own, nil, nil, err
	}
	if ref, ok := ownedByKind(own, "reservation"); ok {
		if _, exists := snap.Find(ref.ID, "reservation"); exists {
			return c, own, nil, nil, conflict("owned reservation already exists")
		}
	}
	fields, subnet, err := reservationFields(h, o, c, own, snap)
	if err != nil {
		return c, own, nil, nil, err
	}
	desired, err := desiredFields(o, c, h, false)
	if err != nil {
		return c, own, nil, nil, err
	}
	if err = targetConflicts(inv, c, desired); err != nil {
		return c, own, nil, nil, err
	}
	if err = checkConflicts(snap, own, desired); err != nil {
		return c, own, nil, nil, err
	}
	if err = checkedSnapshot(snap); err != nil {
		return c, own, nil, nil, err
	}
	// An existing route may still refer to the reservation address. It is never
	// modified by this path, and its recorded dependency bindings must be intact.
	if err = verifyRouteLayout(st, o, own, snap); err != nil {
		return c, own, nil, nil, err
	}
	return c, own, fields, subnet, nil
}

func (s *Service) reservationPlan(st State, req PlanRequest, h Host, o OPNsense, inv Inventory, snap ProviderSnapshot) (Plan, error) {
	if req.Public || req.UpstreamScheme != nil || req.UpstreamPort != nil {
		return Plan{}, bad("reservation repair does not accept a proxy configuration")
	}
	c, own, fields, subnet, err := reservationTarget(st, h, o, req.VMID, inv, snap)
	if err != nil {
		return Plan{}, err
	}
	now := time.Now().UTC()
	effect := PlanEffect{Kind: "reservation", Action: "create", Name: "Add missing reservation only", Changes: []FieldChange{}}
	for _, k := range observationFields["reservation"] {
		effect.Changes = append(effect.Changes, FieldChange{effectField(k), "", safeObservation(fields[k])})
	}
	if ref, ok := ownedByKind(own, "reservation"); ok {
		effect.Changes = append(effect.Changes, FieldChange{"resourceId", ref.ID, "new:reservation"})
	}
	p := Plan{ID: newID(), Action: "reservation", Key: c.Key, HostID: h.ID, VMID: c.VMID, OPNsenseID: o.ID, DesiredRoute: own.DesiredRoute, Public: own.Public, ResolvedSubnet: subnet, Effects: []PlanEffect{effect}, ApplyNotes: []string{"Adds only the missing KEA reservation. No proxy, access policy or application port is configured.", "KEA is reconfigured after fresh verification. Existing advanced reservation fields are preserved when supported."}, Revision: st.Config.Revision, InventoryHash: inv.StableHash(), ProviderHash: fingerprint(snap), Identity: c.Identity, CreatedAt: now, ExpiresAt: now.Add(5 * time.Minute), ConfigHash: fingerprint(st.Config), OwnershipHash: fingerprint(st.Owned)}
	return p, s.savePlan(p, now)
}

func (s *Service) applyReservation(ctx context.Context, st State, p Plan, h Host, o OPNsense, inv Inventory, snap ProviderSnapshot) (Operation, error) {
	c, own, fields, subnet, err := reservationTarget(st, h, o, p.VMID, inv, snap)
	if err != nil {
		return Operation{}, err
	}
	if c.Identity != p.Identity || p.ResolvedSubnet == nil || *subnet != *p.ResolvedSubnet {
		return Operation{}, conflict("reservation target changed")
	}
	if own.Key == "" {
		own = Ownership{Key: c.Key, HostID: h.ID, VMID: c.VMID, OPNsenseID: o.ID, Identity: c.Identity, Resources: []OwnedResource{}}
	}
	// Forget just the selected proven-absent reservation UUID. Other missing
	// resources remain recorded for a separate proxy repair review.
	refs := []OwnedResource{}
	for _, ref := range own.Resources {
		if ref.Kind != "reservation" {
			refs = append(refs, ref)
		}
	}
	own.Resources = refs
	own.ResolvedSubnet = subnet
	now := time.Now().UTC()
	op := Operation{ID: newID(), PlanID: p.ID, Action: "reservation", Key: p.Key, OPNsenseID: o.ID, ContainerFingerprint: c.Fingerprint, ResolvedSubnet: subnet, Status: "running", Step: "starting", CreatedAt: now, UpdatedAt: now, Resources: own.Resources}
	if err = s.Store.Update(func(next *State) error {
		delete(next.Plans, p.ID)
		next.Operations[op.ID] = op
		next.Owned[p.Key] = own
		return nil
	}); err != nil {
		return op, err
	}
	work := workflow{s: s, ctx: ctx, router: o, op: &op, own: &own, snap: snap}
	// Fresh full reads immediately before the single write, separate from review.
	latest := s.Providers.Inventory(ctx, st.Config)
	current, e := s.Providers.Snapshot(ctx, o)
	if e != nil {
		err = e
	} else if latest.StableHash() != inv.StableHash() || fingerprint(current) != fingerprint(snap) {
		err = conflict("provider state changed before reservation creation")
	}
	if err == nil {
		_, err = work.upsert("reservation", fields)
	}
	if err == nil {
		err = work.record("reconfigure", false)
	}
	if err == nil {
		err = s.verifyReservationApplication(ctx, h, o, c, own, subnet)
	}
	if err == nil {
		err = s.Providers.Apply(ctx, o, "kea")
	}
	if err != nil {
		op.Status = "partial"
		op.Error = "reservation workflow incomplete; inspect Activity before recovery"
	} else {
		op.Status = "succeeded"
		op.Step = "complete"
		op.ManualRecovery = false
	}
	op.UpdatedAt = time.Now().UTC()
	persist := s.Store.Update(func(next *State) error { next.Operations[op.ID] = op; next.Owned[own.Key] = own; return nil })
	if persist != nil {
		return op, persist
	}
	return op, err
}

func (s *Service) verifyReservationApplication(ctx context.Context, h Host, o OPNsense, c Container, own Ownership, subnet *SubnetReference) error {
	inv := s.Providers.Inventory(ctx, s.Store.Read().Config)
	fresh, err := ensureContainer(inv, h, c.VMID)
	if err != nil {
		return err
	}
	if fresh.Identity != c.Identity || fresh.Fingerprint != c.Fingerprint {
		return conflict("container changed before KEA application")
	}
	snap, err := s.Providers.Snapshot(ctx, o)
	if err != nil {
		return err
	}
	if err = checkedSnapshot(snap); err != nil {
		return err
	}
	if _, err = existingOwnedResources(own, snap); err != nil {
		return err
	}
	ref, ok := ownedByKind(own, "reservation")
	r, exists := snap.Find(ref.ID, "reservation")
	if !ok || !exists || ref.Fingerprint != r.Fingerprint() {
		return conflict("reservation changed before KEA application")
	}
	return verifySubnet(snap, h, c, subnet)
}
