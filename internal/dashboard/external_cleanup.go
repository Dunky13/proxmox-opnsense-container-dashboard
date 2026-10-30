package dashboard

import (
	"context"
	"sort"
	"strings"
	"time"
)

// External cleanup is deletion authority for one reviewed selection, never
// ownership. Only fingerprints and allowlisted observations reach the API.
type ExternalAddress struct {
	HostID string `json:"hostId"`
	VMID   int    `json:"vmid"`
	IP     string `json:"ip"`
	MAC    string `json:"mac"`
}
type ExternalCleanupReview struct {
	ForeignContextHash      string          `json:"foreignContextHash,omitempty"`
	Address                 ExternalAddress `json:"address"`
	Targets                 []OwnedResource `json:"targets"`
	ContextHash             string          `json:"contextHash"`
	RecoveryID              string          `json:"recoveryId,omitempty"`
	ExternalUseAcknowledged bool            `json:"externalUseAcknowledged"`
}

// Baseline includes private, complete getter evidence. Keep it off all API
// responses, including Activity. It survives missing rows and process restart.
type ExternalCleanupJournal struct {
	Review        ExternalCleanupReview `json:"review"`
	Baseline      ProviderSnapshot      `json:"baseline"`
	ConfigHash    string                `json:"configHash"`
	PolicyHash    string                `json:"policyHash,omitempty"`
	OwnershipHash string                `json:"ownershipHash"`
	Deleted       map[string]bool       `json:"deleted"`
	CaddyApplied  bool                  `json:"caddyApplied"`
	KEAApplied    bool                  `json:"keaApplied"`
	PlanIDs       []string              `json:"planIds"`
	InventoryHash string                `json:"inventoryHash"`
	ExpiresAt     time.Time             `json:"expiresAt"`
}

func externalInventory(cfg Config, inv Inventory) error {
	if err := inventoryError(inv); err != nil {
		return err
	}
	if inv.ObservedAt.IsZero() || time.Since(inv.ObservedAt) > 30*time.Second || inv.ObservedAt.After(time.Now().Add(time.Second)) {
		return conflict("fresh complete cluster occupancy is required")
	}
	for _, h := range cfg.Hosts {
		count := 0
		for _, hi := range inv.Hosts {
			if hi.HostID == h.ID {
				count++
				if !hi.Complete || hi.Error != "" {
					return conflict("complete occupancy for every configured cluster is required")
				}
			}
		}
		if count != 1 {
			return conflict("every configured cluster must have one complete occupancy observation")
		}
	}
	return nil
}
func externalAddress(cfg Config, o OPNsense, ip, mac string) (ExternalAddress, error) {
	matches := []ExternalAddress{}
	for _, h := range cfg.Hosts {
		if routerForHost(cfg, h).ID != o.ID {
			continue
		}
		profiles := append([]AddressProfile{{h.VMIDMin, h.VMIDMax, h.Network, h.MACPrefix}}, h.AddressProfiles...)
		for _, profile := range profiles {
			for id := profile.VMIDMin; id <= profile.VMIDMax; id++ {
				m, a, err := DeriveAddress(h, id)
				if err == nil && a == ip && (mac == "" || strings.EqualFold(mac, m)) {
					matches = append(matches, ExternalAddress{h.ID, id, a, m})
				}
			}
		}
	}
	if len(matches) != 1 {
		return ExternalAddress{}, conflict("address must uniquely match one configured linked host profile and VMID")
	}
	return matches[0], nil
}
func externalAbsent(st State, inv Inventory, a ExternalAddress) error {
	if err := externalInventory(st.Config, inv); err != nil {
		return err
	}
	if _, owned := st.Owned[key(a.HostID, a.VMID)]; owned {
		return conflict("dashboard-owned apps must use their normal guarded cleanup path")
	}
	own := Ownership{HostID: a.HostID, VMID: a.VMID}
	if err := cleanupSafe(inv, own); err != nil {
		return err
	}
	for _, c := range inv.Containers {
		if c.IP == a.IP || strings.EqualFold(c.MAC, a.MAC) {
			return conflict("an observed container uses this profile IP or MAC")
		}
	}
	return nil
}
func ordinaryExternalRoute(r Resource) error {
	if err := unsupportedRoute(r); err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, f := range observationFields[r.Kind] {
		allowed[f] = true
	}
	for _, f := range []string{"description", "basicauth", "ClientAuthMode", "ClientAuthTrustPool", "AcmePassthrough", "HandlePath", "ToPath", "HttpTlsServerName", "HttpTlsTrustedCaCerts", "ForwardAuth"} {
		allowed[f] = true
	}
	for field, value := range r.Fields {
		if !allowed[field] && value != "" {
			return conflict(r.Kind + " has unrecognized advanced route field " + safeObservation(field) + "; inspection required")
		}
	}
	for _, field := range []string{"enabled", "accesslist"} {
		if _, ok := r.Fields[field]; !ok {
			return conflict(r.Kind + " " + field + " policy was not observed")
		}
	}
	if r.Fields["accesslist"] != "" {
		return conflict("shared or layered access policy remains protected")
	}
	if r.Kind == "handle" {
		for _, f := range []string{"HandleType", "HandleDirective", "ToDomain", "ToPort", "HttpTls", "HttpTlsInsecureSkipVerify"} {
			if _, ok := r.Fields[f]; !ok {
				return conflict("handle upstream context was not fully observed: " + f)
			}
		}
		if _, err := observedRoute(r, true); err != nil {
			return err
		}
	}
	return nil
}
func exactAppDomain(o OPNsense, r Resource) bool {
	name := strings.ToLower(strings.TrimSuffix(r.Fields["FromDomain"], "."))
	base := strings.ToLower(o.DomainBase)
	label := strings.TrimSuffix(name, "."+base)
	return hostname.MatchString(label) && name == label+"."+base && validDomain(name)
}

// Return the whole ordinary app routing group. An external shared/wildcard
// parent is context only: never a target, nor evidence of an absent upstream.
func externalRouteGroup(st State, o OPNsense, snap ProviderSnapshot, r Resource) (ExternalAddress, []Resource, error) {
	site := r
	if r.Kind == "handle" {
		id, kind := r.Fields["subdomain"], "subdomain"
		if id == "" {
			id, kind = r.Fields["reverse"], "domain"
		}
		var ok bool
		site, ok = snap.Find(id, kind)
		if !ok {
			return ExternalAddress{}, nil, conflict("handle app domain context is missing")
		}
	}
	if (site.Kind != "domain" && site.Kind != "subdomain") || !exactAppDomain(o, site) {
		return ExternalAddress{}, nil, conflict("only an exact app domain label under the configured domain base is eligible; wildcard/shared parents stay protected")
	}
	if resourceOwned(st, o.ID, site) {
		return ExternalAddress{}, nil, conflict("dashboard-owned domain must use normal guarded cleanup")
	}
	if err := ordinaryExternalRoute(site); err != nil {
		return ExternalAddress{}, nil, err
	}
	reverse, child := site.ID, ""
	if site.Kind == "subdomain" {
		parent, err := parentContext(st, o, snap, site.Fields["reverse"])
		if err != nil {
			return ExternalAddress{}, nil, err
		}
		if err = ordinaryExternalRoute(parent); err != nil {
			return ExternalAddress{}, nil, err
		}
		if !singleDomain(parent.Fields["FromDomain"], "*."+o.DomainBase) && !singleDomain(parent.Fields["FromDomain"], o.DomainBase) {
			return ExternalAddress{}, nil, conflict("child parent is outside configured domain context")
		}
		reverse, child = parent.ID, site.ID
	}
	general, ok := snap.Find("general", "caddyGeneral")
	if _, observed := general.Fields["accesslist"]; !ok || !observed {
		return ExternalAddress{}, nil, conflict("global proxy trust context was not fully observed")
	}
	group := []Resource{site}
	var address ExternalAddress
	for _, row := range snap.Resources {
		if row.Kind == "subdomain" && site.Kind == "domain" && row.Fields["reverse"] == site.ID {
			return address, nil, conflict("shared parent has child consumers and stays protected")
		}
		if (row.Kind == "domain" || row.Kind == "subdomain") && row.ID != site.ID && domainMatches(row.Fields["FromDomain"], site.Fields["FromDomain"]) {
			return address, nil, conflict("app domain has ambiguous sibling consumers")
		}
		if row.Kind != "handle" || !((site.Kind == "domain" && row.Fields["reverse"] == site.ID) || (site.Kind == "subdomain" && row.Fields["subdomain"] == site.ID)) {
			continue
		}
		if resourceOwned(st, o.ID, row) {
			return address, nil, conflict("dashboard-owned handle cannot enter external cleanup")
		}
		if row.Fields["reverse"] != reverse || row.Fields["subdomain"] != child {
			return address, nil, conflict("handle has ambiguous parent or child relations")
		}
		if err := ordinaryExternalRoute(row); err != nil {
			return address, nil, err
		}
		a, err := externalAddress(st.Config, o, row.Fields["ToDomain"], "")
		if err != nil {
			return address, nil, err
		}
		if address.HostID != "" && address != a {
			return address, nil, conflict("app upstreams do not share one unique profile-derived address")
		}
		address = a
		group = append(group, row)
	}
	if len(group) < 2 {
		return address, nil, conflict("all app upstream contexts must be observed; no ordinary handle found")
	}
	return address, group, nil
}
func externalResource(st State, inv Inventory, o OPNsense, snap ProviderSnapshot, r Resource) (ExternalAddress, []Resource, error) {
	if resourceOwned(st, o.ID, r) {
		return ExternalAddress{}, nil, conflict("dashboard-owned UUID must use normal guarded cleanup")
	}
	var a ExternalAddress
	var group []Resource
	var err error
	switch r.Kind {
	case "reservation":
		a, err = externalAddress(st.Config, o, r.Fields["ip_address"], r.Fields["hw_address"])
		if r.Fields["hw_address"] == "" {
			return a, nil, conflict("reservation MAC must exactly match the profile")
		}
		if err == nil {
			h, _ := st.Config.Host(a.HostID)
			subnet, e := resolveSubnet(snap, h, Container{VMID: a.VMID, Name: "cleanup", IP: a.IP, MAC: a.MAC})
			if e != nil {
				err = e
			} else if r.Fields["subnet"] != subnet.ID {
				err = conflict("selected KEA subnet must positively contain the intended profile IP")
			}
		}
		for _, row := range snap.Resources {
			if row.Kind == "reservation" && row.ID != r.ID && (row.Fields["ip_address"] == r.Fields["ip_address"] || strings.EqualFold(row.Fields["hw_address"], r.Fields["hw_address"])) {
				err = conflict("another reservation uses this IP or MAC")
			}
		}
		group = []Resource{r}
	case "domain", "subdomain", "handle":
		a, group, err = externalRouteGroup(st, o, snap, r)
	default:
		err = conflict("resource kind is protected from external cleanup")
	}
	if err == nil {
		err = externalAbsent(st, inv, a)
	}
	return a, group, err
}
func externalSelection(st State, inv Inventory, o OPNsense, snap ProviderSnapshot, refs []OwnedResource, h Host, vmid int) (ExternalAddress, []Resource, error) {
	if !snap.Ready {
		return ExternalAddress{}, nil, conflict("provider prerequisites unavailable")
	}
	if err := checkedSnapshot(snap); err != nil {
		return ExternalAddress{}, nil, err
	}
	if err := externalInventory(st.Config, inv); err != nil {
		return ExternalAddress{}, nil, err
	}
	if len(refs) == 0 {
		return ExternalAddress{}, nil, bad("select exact UUIDs, kinds and fingerprints for external cleanup")
	}
	selected := map[string]bool{}
	rows := []Resource{}
	var address ExternalAddress
	for _, ref := range refs {
		if selected[ref.ID] || !uuidPattern.MatchString(ref.ID) {
			return address, nil, bad("duplicate or invalid selected UUID")
		}
		selected[ref.ID] = true
		r, ok := snap.Find(ref.ID, ref.Kind)
		if !ok || ref.Fingerprint == "" || r.Fingerprint() != ref.Fingerprint {
			return address, nil, conflict("selected UUID, kind or fingerprint changed")
		}
		a, _, err := externalResource(st, inv, o, snap, r)
		if err != nil {
			return address, nil, err
		}
		if a.HostID != h.ID || a.VMID != vmid || (address.HostID != "" && a != address) {
			return address, nil, conflict("selection must match one unique absent profile VMID and address")
		}
		address = a
		rows = append(rows, r)
	}
	for _, r := range rows {
		_, group, err := externalResource(st, inv, o, snap, r)
		if err != nil {
			return address, nil, err
		}
		for _, related := range group {
			if !selected[related.ID] {
				return address, nil, conflict("select the app domain/child and every related eligible handle as one consistent group")
			}
		}
	}
	own := Ownership{Resources: importRefs(rows), ResourceTemplates: rows}
	if err := cleanupDeletionReferences(own, snap); err != nil {
		return address, nil, err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Kind+rows[i].ID < rows[j].Kind+rows[j].ID })
	return address, rows, nil
}
func externalRouterBlocked(st State, routerID, recoveryID string) bool {
	if recoveryID == "" {
		return routerBlocked(st, routerID)
	}
	op, ok := st.Operations[recoveryID]
	if !ok || op.Action != "externalCleanup" || op.OPNsenseID != routerID || op.Status == "succeeded" || op.Status == "running" {
		return true
	}
	copy := clone(st)
	delete(copy.Operations, recoveryID)
	return routerBlocked(copy, routerID)
}
func externalPlanBlocked(st State, routerID string, review *ExternalCleanupReview) bool {
	if review == nil {
		return routerBlocked(st, routerID)
	}
	return externalRouterBlocked(st, routerID, review.RecoveryID)
}

// Reconcile only the originally selected deletions against complete original
// evidence. Any new/changed retained record (including foreign refs) blocks.
func inspectExternalJournal(st State, inv Inventory, snap ProviderSnapshot, j ExternalCleanupJournal) (ExternalCleanupJournal, error) {
	if (j.PolicyHash != "" && j.PolicyHash != configPolicy(st.Config)) || (j.PolicyHash == "" && j.ConfigHash != fingerprint(st.Config)) || j.OwnershipHash != fingerprint(st.Owned) {
		return j, conflict("cleanup configuration or ownership changed; preserve journal for inspection")
	}
	if err := externalAbsent(st, inv, j.Review.Address); err != nil {
		return j, err
	}
	if !snap.Ready {
		return j, conflict("provider prerequisites unavailable")
	}
	if err := checkedSnapshot(snap); err != nil {
		return j, err
	}
	targets := Ownership{Resources: j.Review.Targets}
	for _, ref := range targets.Resources {
		if r, ok := j.Baseline.Find(ref.ID, ref.Kind); ok {
			targets.ResourceTemplates = append(targets.ResourceTemplates, r)
		}
	}
	if err := cleanupDeletionReferences(targets, snap); err != nil {
		return j, err
	}
	if err := cleanupReservationReferences(targets, snap, true); err != nil {
		return j, err
	}
	if j.Deleted == nil {
		j.Deleted = map[string]bool{}
	}
	expected := clone(j.Baseline)
	expected.Resources = nil
	selected := map[string]OwnedResource{}
	for _, ref := range j.Review.Targets {
		selected[ref.ID] = ref
	}
	for _, r := range j.Baseline.Resources {
		if ref, ok := selected[r.ID]; ok {
			current, present := snap.Find(ref.ID, ref.Kind)
			if !present {
				j.Deleted[r.ID] = true
				continue
			}
			if j.Deleted[r.ID] {
				return j, conflict("a deleted external cleanup UUID reappeared")
			}
			if current.Fingerprint() != ref.Fingerprint {
				return j, conflict("selected external UUID fingerprint changed")
			}
		}
		expected.Resources = append(expected.Resources, r)
	}
	if fingerprint(expected) != fingerprint(snap) {
		return j, conflict("provider context or foreign references changed since external cleanup review")
	}
	return j, nil
}
func (s *Service) externalCleanupPlan(ctx context.Context, st State, req PlanRequest, h Host, o OPNsense, inv Inventory, snap ProviderSnapshot) (Plan, error) {
	if !req.ExternalUseAcknowledged {
		return Plan{}, bad("administrator acknowledgement that these exact UUIDs are unused by external or unmanaged clients is required")
	}
	if req.Public || req.UpstreamPort != nil || req.UpstreamScheme != nil {
		return Plan{}, bad("external cleanup accepts no route or exposure changes")
	}
	var j ExternalCleanupJournal
	if req.RecoveryID != "" {
		var ok bool
		j, ok = st.ExternalCleanups[req.RecoveryID]
		if !ok || j.Review.Address.HostID != h.ID || j.Review.Address.VMID != req.VMID {
			return Plan{}, conflict("external cleanup journal does not match this request")
		}
		if len(req.SelectedResources) != len(j.Review.Targets) || fingerprint(req.SelectedResources) != fingerprint(j.Review.Targets) {
			return Plan{}, conflict("recovery must review the exact original UUIDs, kinds and fingerprints")
		}
		var err error
		j, err = inspectExternalJournal(st, inv, snap, j)
		if err != nil {
			return Plan{}, err
		}
	} else {
		a, rows, err := externalSelection(st, inv, o, snap, req.SelectedResources, h, req.VMID)
		if err != nil {
			return Plan{}, err
		}
		j = ExternalCleanupJournal{Review: ExternalCleanupReview{Address: a, Targets: importRefs(rows), ContextHash: fingerprint(snap), ExternalUseAcknowledged: true}, Baseline: clone(snap), ConfigHash: fingerprint(st.Config), PolicyHash: configPolicy(st.Config), OwnershipHash: fingerprint(st.Owned), Deleted: map[string]bool{}}
	}
	foreignHash, err := s.externalForeignContext(ctx, st.Config, o, j.Review)
	if err != nil {
		return Plan{}, err
	}
	if req.RecoveryID != "" && foreignHash != j.Review.ForeignContextHash {
		return Plan{}, conflict("other configured firewall context changed; preserve external cleanup evidence for inspection")
	}
	j.Review.ForeignContextHash = foreignHash
	review := clone(j.Review)
	review.RecoveryID = req.RecoveryID
	own := Ownership{Resources: review.Targets}
	effects, _, err := planEffects("cleanup", h, o, inv, own, snap, RouteConfig{}, "")
	if err != nil {
		return Plan{}, err
	}
	for i := range effects {
		if effects[i].Action == "retain" {
			effects[i].Name = "Already absent selected external " + effects[i].Kind + "; retain deletion evidence"
			if evidence, ok := j.Baseline.Find(effects[i].ResourceID, effects[i].Kind); ok {
				effects[i].Changes = resourceObservations(evidence)
				for n := range effects[i].Changes {
					effects[i].Changes[n].After = ""
				}
			}
		} else {
			effects[i].Name = "Delete selected unowned " + effects[i].Kind
		}
	}
	now := time.Now().UTC()
	p := Plan{ID: newID(), Action: "externalCleanup", Key: key(h.ID, req.VMID), HostID: h.ID, VMID: req.VMID, OPNsenseID: o.ID, ExternalCleanup: &review, Identity: fingerprint(review.Address), Effects: effects, Revision: st.Config.Revision, ConfigHash: fingerprint(st.Config), OwnershipHash: fingerprint(st.Owned), InventoryHash: inv.StableHash(), ProviderHash: fingerprint(snap), CreatedAt: now, ExpiresAt: now.Add(5 * time.Minute), ApplyNotes: []string{"Deletes only these exact unowned UUIDs. No ownership is adopted. Administrator attests that these UUIDs are unused by external or unmanaged clients.", "The profile VMID is absent across complete cluster occupancy. Fresh address, subnet, policy and reference guards run before each delete and service application.", "Caddy deletions are applied before any reservation is deleted or KEA applied. Shared parents, siblings, ACLs and trust settings are retained.", "A failed or lost response never triggers an automatic retry. Inspect Activity and prepare a new acknowledged recovery plan."}}
	return p, s.savePlan(p, now)
}
func (s *Service) applyExternalCleanup(ctx context.Context, st State, p Plan, h Host, o OPNsense, inv Inventory, snap ProviderSnapshot) (Operation, error) {
	if p.ExternalCleanup == nil || !p.ExternalCleanup.ExternalUseAcknowledged {
		return Operation{}, conflict("external acknowledgement binding is missing")
	}
	foreignHash, err := s.externalForeignContext(ctx, st.Config, o, *p.ExternalCleanup)
	if err != nil {
		return Operation{}, err
	}
	if foreignHash != p.ExternalCleanup.ForeignContextHash {
		return Operation{}, conflict("other configured firewall context changed since review")
	}
	req := PlanRequest{Action: "externalCleanup", HostID: h.ID, VMID: p.VMID, SelectedResources: p.ExternalCleanup.Targets, ExternalUseAcknowledged: true, RecoveryID: p.ExternalCleanup.RecoveryID}
	var j ExternalCleanupJournal
	if req.RecoveryID != "" {
		j = clone(st.ExternalCleanups[req.RecoveryID])
		var err error
		j, err = inspectExternalJournal(st, inv, snap, j)
		if err != nil {
			return Operation{}, err
		}
	} else {
		a, rows, err := externalSelection(st, inv, o, snap, req.SelectedResources, h, p.VMID)
		if err != nil {
			return Operation{}, err
		}
		review := ExternalCleanupReview{Address: a, Targets: importRefs(rows), ContextHash: fingerprint(snap), ExternalUseAcknowledged: true, ForeignContextHash: foreignHash}
		if fingerprint(review) != fingerprint(p.ExternalCleanup) {
			return Operation{}, conflict("external cleanup bindings changed")
		}
		j = ExternalCleanupJournal{Review: review, Baseline: clone(snap), ConfigHash: p.ConfigHash, PolicyHash: configPolicy(st.Config), OwnershipHash: p.OwnershipHash, Deleted: map[string]bool{}}
	}
	if fingerprint(j.Review.Address) != p.Identity || fingerprint(j.Review.Targets) != fingerprint(p.ExternalCleanup.Targets) {
		return Operation{}, conflict("external selection identity changed")
	}
	now := time.Now().UTC()
	op := Operation{ID: newID(), PlanID: p.ID, Key: p.Key, OPNsenseID: o.ID, Action: "externalCleanup", Status: "running", Step: "starting", CreatedAt: now, UpdatedAt: now, ExternalCleanup: p.ExternalCleanup, CleanupTargets: clone(j.Review.Targets)}
	if req.RecoveryID != "" {
		old := st.Operations[req.RecoveryID]
		op.ID = old.ID
		op.CreatedAt = old.CreatedAt
	}
	j.PlanIDs = append(j.PlanIDs, p.ID)
	j.InventoryHash, j.ExpiresAt = p.InventoryHash, p.ExpiresAt
	own := externalCleanupSelection(j)
	op.Resources = clone(own.Resources)
	if err := s.Store.Update(func(next *State) error {
		current, ok := next.Plans[p.ID]
		if !ok || fingerprint(current) != fingerprint(p) || externalRouterBlocked(*next, o.ID, req.RecoveryID) || fingerprint(next.Config) != p.ConfigHash || fingerprint(next.Owned) != p.OwnershipHash || time.Now().After(p.ExpiresAt) {
			return conflict("external cleanup plan changed or expired")
		}
		delete(next.Plans, p.ID)
		if next.ExternalCleanups == nil {
			next.ExternalCleanups = map[string]ExternalCleanupJournal{}
		}
		next.ExternalCleanups[op.ID] = j
		next.Operations[op.ID] = op
		return nil
	}); err != nil {
		return op, err
	}
	w := workflow{s: s, ctx: ctx, router: o, op: &op, own: &own, snap: snap, external: &j}
	if err := w.cleanup(); err != nil {
		op.Status = "partial"
		op.Error = "External cleanup incomplete. Inspect exact deletion evidence and create a fresh acknowledged recovery plan. No automatic retry was sent."
		if persistErr := w.record(op.Step, op.ManualRecovery); persistErr != nil {
			return op, persistErr
		}
		return op, err
	}
	op.Status = "succeeded"
	op.Error = ""
	return op, w.record("complete", false)
}
func externalCleanupSelection(j ExternalCleanupJournal) Ownership {
	a := j.Review.Address
	own := Ownership{Key: key(a.HostID, a.VMID), HostID: a.HostID, VMID: a.VMID, Identity: fingerprint(a), Resources: []OwnedResource{}}
	for _, ref := range j.Review.Targets {
		if !j.Deleted[ref.ID] {
			own.Resources = append(own.Resources, ref)
		}
		if r, ok := j.Baseline.Find(ref.ID, ref.Kind); ok {
			own.ResourceTemplates = append(own.ResourceTemplates, r)
		}
	}
	return own
}

// Strict per-step guards cannot reconcile an unexpected disappearance. Only a
// new acknowledged recovery plan may interpret missing selected UUIDs.
func (w *workflow) externalGuard(inv Inventory, snap ProviderSnapshot) error {
	if w.external == nil {
		return nil
	}
	if time.Now().After(w.external.ExpiresAt) || inv.StableHash() != w.external.InventoryHash {
		return conflict("external cleanup review expired or cluster occupancy changed; create a fresh recovery plan")
	}
	foreignHash, err := w.s.externalForeignContext(w.ctx, w.s.Store.Read().Config, w.router, w.external.Review)
	if err != nil {
		return err
	}
	if foreignHash != w.external.Review.ForeignContextHash {
		return conflict("other configured firewall context changed during external cleanup")
	}
	inspected, err := inspectExternalJournal(w.s.Store.Read(), inv, snap, clone(*w.external))
	if err != nil {
		return err
	}
	if fingerprint(inspected.Deleted) != fingerprint(w.external.Deleted) {
		return conflict("selected UUID disappeared during cleanup; inspect Activity before recovery")
	}
	return nil
}

func (w *workflow) externalApplication(service string, selection Ownership) error {
	needed := false
	for _, ref := range w.external.Review.Targets {
		if (service == "kea" && ref.Kind == "reservation") || (service == "caddy" && ref.Kind != "reservation") {
			needed = true
		}
	}
	if !needed || (service == "caddy" && w.external.CaddyApplied) || (service == "kea" && w.external.KEAApplied) {
		return nil
	}
	if err := w.applyCleanupService(service, selection); err != nil {
		return err
	}
	if service == "caddy" {
		w.external.CaddyApplied = true
	} else {
		w.external.KEAApplied = true
	}
	return w.record("applied:"+service, false)
}

// Releasing an address depends on every configured proxy inventory, including
// firewalls with no enrolled host. These are read-only dependencies; retained
// handles there must be cleaned separately before releasing this reservation.
func (s *Service) externalForeignContext(ctx context.Context, cfg Config, router OPNsense, review ExternalCleanupReview) (string, error) {
	reservation := false
	for _, ref := range review.Targets {
		if ref.Kind == "reservation" {
			reservation = true
		}
	}
	if !reservation {
		return "", nil
	}
	return s.reservationForeignContext(ctx, cfg, router, []string{review.Address.IP})
}

func (s *Service) reservationForeignContext(ctx context.Context, cfg Config, router OPNsense, ips []string) (string, error) {
	if len(ips) == 0 {
		return "", nil
	}
	hashes := map[string]string{}
	for _, other := range cfg.OPNsenses {
		if other.ID == router.ID {
			continue
		}
		snap, err := s.Providers.Snapshot(ctx, other)
		if err != nil || !snap.Ready || checkedSnapshot(snap) != nil {
			return "", conflict("complete proxy inventory is required on configured firewall " + other.ID + " before releasing the reservation")
		}
		for _, r := range snap.Resources {
			for _, ip := range ips {
				if r.Kind == "handle" && strings.Contains(r.Fields["ToDomain"], ip) {
					return "", conflict("retained proxy handle " + r.ID + " on configured firewall " + other.ID + " still references the reservation IP; clean that proxy separately first")
				}
			}
		}
		hashes[other.ID] = fingerprint(snap)
	}
	return fingerprint(hashes), nil
}
