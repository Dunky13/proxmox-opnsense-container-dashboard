package dashboard

import (
	"context"
	"sort"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

type Service struct {
	Store      *Store
	Providers  Providers
	mutate     sync.Mutex
	jobsMu     sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
	workers    sync.WaitGroup
	slots      chan struct{}
	JobTimeout time.Duration
}

func NewService(store *Store, p Providers) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{Store: store, Providers: p, ctx: ctx, cancel: cancel, slots: make(chan struct{}, 2), JobTimeout: 30 * time.Minute}
}
func (s *Service) Close()               { s.cancel(); s.workers.Wait() }
func newID() string                     { return oauth2.GenerateVerifier() }
func (s *Service) Settings() ConfigView { return viewConfig(s.Store.Read().Config) }
func (s *Service) UpdateConfig(edit ConfigEdit) (ConfigView, error) {
	s.mutate.Lock()
	defer s.mutate.Unlock()
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	state := s.Store.Read()
	if edit.Revision != state.Config.Revision {
		return ConfigView{}, conflict("settings revision changed")
	}
	next := Config{Revision: state.Config.Revision + 1, Hosts: []Host{}, OPNsenses: []OPNsense{}}
	for _, v := range edit.Hosts {
		h := v.Host
		if old, ok := state.Config.Host(h.ID); ok {
			h.TokenSecret = old.TokenSecret
		}
		if v.Secret != nil {
			h.TokenSecret = *v.Secret
		}
		next.Hosts = append(next.Hosts, h)
	}
	for _, v := range edit.OPNsenses {
		o := v.OPNsense
		if old, ok := state.Config.Router(o.ID); ok {
			o.APISecret = old.APISecret
			o.APIKey = old.APIKey
		}
		if v.Secret != nil {
			o.APISecret = *v.Secret
		}
		if v.APIKeyValue != nil {
			o.APIKey = *v.APIKeyValue
		}
		next.OPNsenses = append(next.OPNsenses, o)
	}
	if e := ValidateConfig(next); e != nil {
		return ConfigView{}, e
	}
	for _, o := range next.OPNsenses {
		if err := sharedListNotOwned(state, o.ID, o.PrivateAccessListID); err != nil {
			return ConfigView{}, err
		}
	}
	for _, owned := range state.Owned {
		old, _ := state.Config.Host(owned.HostID)
		h, ok := next.Host(owned.HostID)
		if !ok || h.Node != old.Node || h.BaseURL != old.BaseURL || h.OPNsenseID != old.OPNsenseID {
			return ConfigView{}, conflict("host identity is protected while ownership exists")
		}
		a, _ := state.Config.Router(owned.OPNsenseID)
		b, ok := next.Router(owned.OPNsenseID)
		if !ok || a.BaseURL != b.BaseURL {
			return ConfigView{}, conflict("provider identity is protected while ownership exists")
		}
	}
	for _, op := range state.Operations {
		if op.Status != "succeeded" {
			if configPolicy(next) != configPolicy(state.Config) {
				return ConfigView{}, conflict("recover unfinished operations before changing settings")
			}
		}
	}
	for _, j := range state.Jobs {
		if jobBlocks(j) {
			return ConfigView{}, conflict("finish or acknowledge update jobs before changing settings")
		}
	}
	e := s.Store.Update(func(st *State) error { st.Config = next; st.Plans = map[string]Plan{}; return nil })
	return viewConfig(next), e
}
func (s *Service) Inventory(ctx context.Context) Inventory {
	st := s.Store.Read()
	return decorateInventory(s.Providers.Inventory(ctx, st.Config), st)
}
func (s *Service) TestHost(ctx context.Context, id string) (HostInventory, error) {
	cfg := s.Store.Read().Config
	h, ok := cfg.Host(id)
	if !ok {
		return HostInventory{}, &Fault{"notFound", "host not found", 404}
	}
	i := s.Providers.Inventory(ctx, Config{Hosts: []Host{h}})
	if len(i.Hosts) != 1 {
		return HostInventory{}, unavailable("host inventory is missing")
	}
	return i.Hosts[0], nil
}
func (s *Service) TestRouter(ctx context.Context, id string) (ProviderSnapshot, error) {
	o, ok := s.Store.Read().Config.Router(id)
	if !ok {
		return ProviderSnapshot{}, &Fault{"notFound", "OPNsense not found", 404}
	}
	return s.Providers.Snapshot(ctx, o)
}
func routerBlocked(st State, id string) bool {
	for _, op := range st.Operations {
		if op.OPNsenseID == id && op.Status != "succeeded" {
			return true
		}
	}
	return false
}
func checkOwned(own Ownership, snap ProviderSnapshot) error {
	for _, r := range own.Resources {
		now, ok := snap.Find(r.ID, r.Kind)
		if !ok || now.Fingerprint() != r.Fingerprint {
			return conflict("owned provider resource is missing or changed")
		}
	}
	return nil
}

// A fresh plan may recreate a resource proven absent by a complete snapshot.
// Existing resources must still match their last acknowledged fingerprints.
// Dropping absent UUIDs never adopts a replacement with matching fields.
func existingOwnedResources(own Ownership, snap ProviderSnapshot) (Ownership, error) {
	refs := own.Resources
	own.Resources = []OwnedResource{}
	for _, ref := range refs {
		if ref.Fingerprint == "" {
			return own, conflict("owned resource has an uncertain fingerprint; inspect provider state")
		}
		resource, exists := snap.Find(ref.ID, ref.Kind)
		if !exists {
			continue
		}
		if resource.Fingerprint() != ref.Fingerprint {
			return own, conflict("owned provider resource changed")
		}
		if ref.Kind == "reservation" && ref.SubnetID == "" {
			ref.SubnetID = resource.Fields["subnet"]
		}
		own.Resources = append(own.Resources, ref)
	}
	return own, nil
}
func cleanupSafe(i Inventory, own Ownership) error {
	if e := inventoryError(i); e != nil {
		return e
	}
	for _, h := range i.Hosts {
		for _, g := range h.Guests {
			if g.VMID == own.VMID {
				return conflict("VMID still exists in observed clusters; stopped, reused or migrated guests block cleanup")
			}
		}
	}
	return nil
}
func desiredFields(o OPNsense, c Container, h Host, public bool) (map[string]string, error) {
	mac, ip, e := profileCheck(c, h)
	if e != nil {
		return nil, e
	}
	domain := c.Name + "." + o.DomainBase
	if !validDomain(domain) {
		return nil, conflict("derived proxy domain exceeds DNS limits")
	}
	return map[string]string{"mac": mac, "ip": ip, "name": c.Name, "domain": domain}, nil
}
func checkConflicts(snap ProviderSnapshot, own Ownership, desired map[string]string) error {
	isOwned := func(r Resource) bool {
		for _, x := range own.Resources {
			if x.ID == r.ID && x.Kind == r.Kind {
				return true
			}
		}
		return false
	}
	for _, r := range snap.Resources {
		if isOwned(r) {
			continue
		}
		if r.Kind == "reservation" && (r.Fields["ip_address"] == desired["ip"] || r.Fields["hw_address"] == desired["mac"]) {
			return conflict("an unrelated DHCP reservation already uses the IP or MAC")
		}
		if r.Kind == "domain" && r.Fields["FromDomain"] == desired["domain"] {
			return conflict("an unrelated Caddy domain already exists")
		}
	}
	return nil
}
func (s *Service) Plan(ctx context.Context, req PlanRequest) (Plan, error) {
	s.mutate.Lock()
	defer s.mutate.Unlock()
	st := s.Store.Read()
	h, ok := st.Config.Host(req.HostID)
	if !ok {
		return Plan{}, bad("unknown host")
	}
	o := routerForHost(st.Config, h)
	if routerBlocked(st, o.ID) {
		return Plan{}, conflict("OPNsense has unfinished operations")
	}
	if req.Action != "ensure" && req.Action != "cleanup" {
		return Plan{}, bad("action must be ensure or cleanup")
	}
	if req.VMID < 100 || req.VMID > 999999999 {
		return Plan{}, bad("invalid VMID")
	}
	inv := s.Providers.Inventory(ctx, st.Config)
	if e := inventoryError(inv); e != nil {
		return Plan{}, e
	}
	snap, e := s.Providers.Snapshot(ctx, o)
	if e != nil {
		return Plan{}, e
	}
	if !snap.Ready {
		return Plan{}, conflict("OPNsense prerequisites are missing; run connection preflight")
	}
	own, owned := st.Owned[key(h.ID, req.VMID)]
	if owned && req.Action == "ensure" && !validRoute(own.DesiredRoute) {
		migrated, err := observedOwnedRoute(own, snap)
		if err != nil {
			if req.UpstreamScheme == nil || req.UpstreamPort == nil {
				return Plan{}, err
			}
			migrated = RouteConfig{Scheme: *req.UpstreamScheme, Port: *req.UpstreamPort, Public: own.Public}
		}
		own.DesiredRoute = migrated
	}
	route, e := resolveRoute(req, own, owned)
	if e != nil {
		return Plan{}, e
	}
	identity := ""
	if req.Action == "cleanup" {
		if !owned || len(own.Resources) == 0 {
			return Plan{}, conflict("no dashboard-owned resources")
		}
		if own.Identity == "" {
			return Plan{}, conflict("ownership creation identity is missing")
		}
		if e = cleanupSafe(inv, own); e != nil {
			return Plan{}, e
		}
		if _, e = existingOwnedResources(own, snap); e != nil {
			return Plan{}, e
		}
		if e = cleanupReferences(own, snap); e != nil {
			return Plan{}, e
		}
		identity = own.Identity
	} else {
		c, e := ensureContainer(inv, h, req.VMID)
		if e != nil {
			return Plan{}, e
		}
		identity = c.Identity
		if owned && own.Identity != identity {
			return Plan{}, conflict("VMID has been reused or moved")
		}
		if !snap.Ready {
			return Plan{}, conflict("OPNsense prerequisites are missing; run connection preflight")
		}
		if !req.Public && o.PrivateAccessListID == "" && len(o.AllowlistCIDRs) == 0 {
			return Plan{}, conflict("private routes require configured allowlist CIDRs")
		}
		resolved, err := resolveSubnet(snap, h, c)
		if err != nil {
			return Plan{}, err
		}
		o.SubnetID = resolved.ID
		desired, e := desiredFields(o, c, h, req.Public)
		if e != nil {
			return Plan{}, e
		}
		if e = targetConflicts(inv, c, desired); e != nil {
			return Plan{}, e
		}
		if e = checkConflicts(snap, own, desired); e != nil {
			return Plan{}, e
		}
		if owned {
			existing, err := existingOwnedResources(own, snap)
			if err != nil {
				return Plan{}, err
			}
			if len(existing.Resources) != len(own.Resources) {
				if e = cleanupReferences(own, snap); e != nil {
					return Plan{}, e
				}
			}
			if e = checkOwned(existing, snap); e != nil {
				return Plan{}, e
			}
		}
	}
	now := time.Now().UTC()
	effects, notes, e := planEffects(req.Action, h, o, inv, own, snap, route, key(h.ID, req.VMID))
	if e != nil {
		return Plan{}, e
	}
	p := Plan{ID: newID(), Action: req.Action, Key: key(h.ID, req.VMID), HostID: h.ID, VMID: req.VMID, OPNsenseID: o.ID, Public: route.Public, DesiredRoute: route, Effects: effects, ApplyNotes: notes, Revision: st.Config.Revision, InventoryHash: inv.StableHash(), ProviderHash: fingerprint(snap), Identity: identity, CreatedAt: now, ExpiresAt: now.Add(5 * time.Minute)}
	if req.Action == "ensure" {
		c, _ := inv.Find(p.Key)
		p.ResolvedSubnet, e = resolveSubnet(snap, h, c)
		if e != nil {
			return Plan{}, e
		}
	}
	if req.Action == "ensure" && !route.Public && o.PrivateAccessListID != "" {
		if err := sharedListNotOwned(st, o.ID, o.PrivateAccessListID); err != nil {
			return Plan{}, err
		}
		shared, err := selectedAccessList(o, snap)
		if err != nil {
			return Plan{}, err
		}
		p.SharedAccessList = referenceAccessList(shared, snap)
		if err := sharedContextNotOwned(st, o, snap, false); err != nil {
			return Plan{}, err
		}
	}
	e = s.Store.Update(func(st *State) error {
		for id, p := range st.Plans {
			if p.ExpiresAt.Before(now) {
				delete(st.Plans, id)
			}
		}
		if len(st.Plans) >= 200 {
			return conflict("too many pending plans")
		}
		st.Plans[p.ID] = p
		return nil
	})
	return p, e
}
func cleanupReferences(own Ownership, snap ProviderSnapshot) error {
	ids := map[string]bool{}
	for _, r := range own.Resources {
		ids[r.ID] = true
	}
	for _, r := range snap.Resources {
		if ids[r.ID] {
			continue
		}
		for _, k := range []string{"reverse", "accesslist", "subdomain"} {
			if ids[r.Fields[k]] {
				return conflict("an unrelated resource references owned configuration")
			}
		}
	}
	return nil
}
func (s *Service) ApplyPlan(ctx context.Context, id string) (Operation, error) {
	if !s.mutate.TryLock() {
		return Operation{}, conflict("another mutation is active")
	}
	defer s.mutate.Unlock()
	st := s.Store.Read()
	plan, ok := st.Plans[id]
	if !ok {
		return Operation{}, &Fault{"notFound", "plan not found", 404}
	}
	if time.Now().After(plan.ExpiresAt) || plan.Revision != st.Config.Revision {
		return Operation{}, conflict("plan expired or settings changed")
	}
	if routerBlocked(st, plan.OPNsenseID) {
		return Operation{}, conflict("OPNsense has unfinished operations")
	}
	h, ok := st.Config.Host(plan.HostID)
	if !ok {
		return Operation{}, conflict("host removed")
	}
	o := routerForHost(st.Config, h)
	inv := s.Providers.Inventory(ctx, st.Config)
	if e := inventoryError(inv); e != nil {
		return Operation{}, e
	}
	if inv.StableHash() != plan.InventoryHash {
		return Operation{}, conflict("inventory changed; create a fresh plan")
	}
	snap, e := s.Providers.Snapshot(ctx, o)
	if e != nil {
		return Operation{}, e
	}
	if fingerprint(snap) != plan.ProviderHash {
		return Operation{}, conflict("provider resources changed; create a fresh plan")
	}
	if plan.Action == "ensure" {
		if e = verifySharedAccessList(o, snap, plan.SharedAccessList, plan.Public); e != nil {
			return Operation{}, e
		}
		c, _ := inv.Find(plan.Key)
		if e = verifySubnet(snap, h, c, plan.ResolvedSubnet); e != nil {
			return Operation{}, e
		}
	}
	own, owned := st.Owned[plan.Key]
	if owned {
		existing, err := existingOwnedResources(own, snap)
		if err != nil {
			return Operation{}, err
		}
		if len(existing.Resources) != len(own.Resources) {
			if e = cleanupReferences(own, snap); e != nil {
				return Operation{}, e
			}
		}
		own = existing
	}
	if plan.Action == "cleanup" {
		if !owned {
			return Operation{}, conflict("ownership is missing")
		}
		if e = cleanupSafe(inv, own); e != nil {
			return Operation{}, e
		}
		if e = checkOwned(own, snap); e != nil {
			return Operation{}, e
		}
		if e = cleanupReferences(own, snap); e != nil {
			return Operation{}, e
		}
	} else {
		c, e := ensureContainer(inv, h, plan.VMID)
		if e != nil {
			return Operation{}, e
		}
		if c.Identity != plan.Identity {
			return Operation{}, conflict("container identity changed")
		}
		if owned && own.Identity != c.Identity {
			return Operation{}, conflict("VMID reuse detected")
		}
	}
	now := time.Now().UTC()
	op := Operation{ID: newID(), PlanID: plan.ID, Key: plan.Key, OPNsenseID: o.ID, Action: plan.Action, Status: "running", Step: "starting", CreatedAt: now, UpdatedAt: now, Resources: []OwnedResource{}}
	if plan.Action == "ensure" {
		container, _ := inv.Find(plan.Key)
		op.ContainerFingerprint = container.Fingerprint
	}
	if !owned {
		own = Ownership{Key: plan.Key, HostID: h.ID, VMID: plan.VMID, OPNsenseID: o.ID, Identity: plan.Identity, Public: plan.Public, Resources: []OwnedResource{}}
	}
	if plan.Action == "ensure" {
		if !validRoute(plan.DesiredRoute) {
			return Operation{}, conflict("plan predates route configuration; create a new plan")
		}
		own.Public = plan.DesiredRoute.Public
		own.SharedAccessList = plan.SharedAccessList
		op.SharedAccessList = plan.SharedAccessList
		own.ResolvedSubnet = plan.ResolvedSubnet
		op.ResolvedSubnet = plan.ResolvedSubnet
		op.DesiredRoute = &plan.DesiredRoute
		own.DesiredRoute = plan.DesiredRoute
		o.UpstreamScheme = plan.DesiredRoute.Scheme
		o.UpstreamPort = plan.DesiredRoute.Port
		o.SubnetID = plan.ResolvedSubnet.ID
	}
	op.Resources = append([]OwnedResource{}, own.Resources...)
	if e = s.Store.Update(func(st *State) error {
		delete(st.Plans, id)
		st.Operations[op.ID] = op
		st.Owned[own.Key] = own
		return nil
	}); e != nil {
		return op, e
	}
	work := &workflow{s: s, ctx: ctx, router: o, op: &op, own: &own, snap: snap}
	if plan.Action == "ensure" {
		c, _ := inv.Find(plan.Key)
		e = work.ensure(h, c, plan.Public)
	} else {
		e = work.cleanup()
	}
	if e != nil {
		op.Status = "partial"
		op.Error = "provider workflow incomplete; inspect recorded step before recovery"
		op.UpdatedAt = time.Now().UTC()
		if persistErr := s.Store.Update(func(st *State) error { st.Operations[op.ID] = op; return nil }); persistErr != nil {
			return op, persistErr
		}
		return op, e
	}
	op.Status = "succeeded"
	op.Step = "complete"
	op.Error = ""
	op.ManualRecovery = false
	op.UpdatedAt = time.Now().UTC()
	e = s.Store.Update(func(st *State) error {
		st.Operations[op.ID] = op
		if plan.Action == "cleanup" {
			delete(st.Owned, own.Key)
		} else {
			own.Public = plan.Public
			st.Owned[own.Key] = own
		}
		return nil
	})
	return op, e
}

type workflow struct {
	host      Host
	container Container
	s         *Service
	ctx       context.Context
	router    OPNsense
	op        *Operation
	own       *Ownership
	snap      ProviderSnapshot
}

func (w *workflow) record(step string, uncertain bool) error {
	w.op.Step = step
	w.op.ManualRecovery = uncertain
	w.op.Resources = append([]OwnedResource{}, w.own.Resources...)
	w.op.UpdatedAt = time.Now().UTC()
	return w.s.Store.Update(func(st *State) error { st.Operations[w.op.ID] = *w.op; st.Owned[w.own.Key] = *w.own; return nil })
}
func (w *workflow) upsert(kind string, fields map[string]string) (Resource, error) {
	var old Resource
	exists := false
	for _, r := range w.own.Resources {
		if r.Kind == kind {
			old, exists = w.snap.Find(r.ID, kind)
			if !exists || old.Fingerprint() != r.Fingerprint {
				return old, conflict("owned resource changed")
			}
		}
	}
	if exists {
		same := true
		for k, v := range fields {
			if old.Fields[k] != v {
				same = false
				break
			}
		}
		if same {
			return old, nil
		}
		if kind == "acl" {
			policy, err := w.s.Providers.CaddyPolicy(w.ctx, w.router)
			if err != nil {
				return Resource{}, err
			}
			general, ok := policy.snapshot().Find("general", "caddyGeneral")
			trustedID, observed := general.Fields["accesslist"]
			if !ok || !observed || trustedID == old.ID {
				return Resource{}, conflict("global trusted proxy policy must remain externally managed and unchanged")
			}
		}
	}
	if e := w.checkEnsureDependencies(); e != nil {
		return Resource{}, e
	}
	if e := w.record("write:"+kind, true); e != nil {
		return Resource{}, e
	}
	var r Resource
	var e error
	if exists {
		r, e = w.s.Providers.Set(w.ctx, w.router, old, fields)
	} else {
		r, e = w.s.Providers.Create(w.ctx, w.router, kind, fields)
	}
	if uuidPattern.MatchString(r.ID) {
		w.op.Resources = append(w.op.Resources, OwnedResource{ID: r.ID, Kind: kind})
	}
	if e != nil {
		return r, e
	}
	for k, v := range fields {
		if r.Fields[k] != v {
			return r, unavailable("provider did not retain requested resource fields")
		}
	}
	ref := OwnedResource{ID: r.ID, Kind: kind, Fingerprint: r.Fingerprint()}
	if kind == "reservation" {
		ref.SubnetID = r.Fields["subnet"]
	}
	replaced := false
	for i, x := range w.own.Resources {
		if x.Kind == kind {
			w.own.Resources[i] = ref
			replaced = true
		}
	}
	if !replaced {
		w.own.Resources = append(w.own.Resources, ref)
	}
	found := false
	for i, x := range w.snap.Resources {
		if x.Kind == kind && x.ID == r.ID {
			w.snap.Resources[i] = r
			found = true
		}
	}
	if !found {
		w.snap.Resources = append(w.snap.Resources, r)
	}
	return r, w.record("recorded:"+kind, true)
}
func (w *workflow) ensure(h Host, c Container, public bool) error {
	w.host, w.container = h, c

	if desiredResourcesMatch(w.router, h, c, *w.own, w.snap, public) {
		return nil
	}
	specs, e := routeSpecs(w.router, h, c, public, "", "")
	if e != nil {
		return e
	}
	if _, e = w.upsert("reservation", specs["reservation"]); e != nil {
		return e
	}
	aclID := ""
	if !public {
		if w.router.PrivateAccessListID != "" {
			if e = verifySharedAccessList(w.router, w.snap, w.op.SharedAccessList, false); e != nil {
				return e
			}
			aclID = w.router.PrivateAccessListID
		} else {
			acl, err := w.upsert("acl", specs["acl"])
			if err != nil {
				return err
			}
			aclID = acl.ID
		}
	}
	specs["domain"]["enabled"] = "0"
	specs["domain"]["accesslist"] = aclID
	domain, e := w.upsert("domain", specs["domain"])
	if e != nil {
		return e
	}
	specs["handle"]["reverse"] = domain.ID
	if _, e = w.upsert("handle", specs["handle"]); e != nil {
		return e
	}
	// Re-read the ACL and domain immediately before enabling; never substitute a
	// public route when the private ACL cannot be verified.
	snap, e := w.s.Providers.Snapshot(w.ctx, w.router)
	if e != nil {
		return e
	}
	if e = checkOwned(*w.own, snap); e != nil {
		return e
	}
	fresh, ok := snap.Find(domain.ID, "domain")
	if !ok || fresh.Fields["accesslist"] != aclID {
		return conflict("domain ACL attachment is not verified")
	}
	if !public {
		if e = verifySharedAccessList(w.router, snap, w.op.SharedAccessList, false); e != nil {
			return e
		}
		acl, ok := snap.Find(aclID, "acl")
		if !ok || (w.router.PrivateAccessListID == "" && (acl.Fields["clientIps"] != canonicalCIDRs(w.router.AllowlistCIDRs) || acl.Fields["accesslistInvert"] != "0" || acl.Fields["RequestMatcher"] != "remote_ip")) {
			return conflict("private ACL is not verified")
		}
	}
	cfg := w.s.Store.Read().Config
	latest := w.s.Providers.Inventory(w.ctx, cfg)
	freshCT, e := ensureContainer(latest, h, c.VMID)
	if e != nil {
		return e
	}
	if freshCT.Identity != c.Identity || freshCT.Fingerprint != c.Fingerprint {
		return conflict("container changed during provider preparation; route remains disabled")
	}
	w.snap = snap
	if e = w.checkEnsureDependencies(); e != nil {
		return e
	}
	if _, e = w.upsert("domain", map[string]string{"enabled": "1"}); e != nil {
		return e
	}
	if e = w.record("reconfigure", false); e != nil {
		return e
	}
	for _, service := range serviceApplicationOrder("ensure") {
		if err := w.checkEnsureDependencies(); err != nil {
			return err
		}
		if e = w.s.Providers.Apply(w.ctx, w.router, service); e != nil {
			return e
		}
	}
	return nil
}
func (w *workflow) checkShared(public bool) error {
	if public || w.router.PrivateAccessListID == "" {
		return nil
	}
	policy, err := w.s.Providers.CaddyPolicy(w.ctx, w.router)
	if err != nil {
		return unavailable("shared access list could not be verified before application")
	}
	if err := sharedContextNotOwned(w.s.Store.Read(), w.router, policy.snapshot(), false); err != nil {
		return err
	}
	return verifySharedAccessList(w.router, policy.snapshot(), w.op.SharedAccessList, false)
}
func (w *workflow) checkEnsureDependencies() error {
	if w.op.Action != "ensure" {
		return nil
	}
	cfg := w.s.Store.Read().Config
	inv := w.s.Providers.Inventory(w.ctx, cfg)
	c, err := ensureContainer(inv, w.host, w.container.VMID)
	if err != nil {
		return err
	}
	if c.Identity != w.container.Identity || c.Fingerprint != w.container.Fingerprint {
		return conflict("container changed during provider preparation")
	}
	rows, err := w.s.Providers.Subnets(w.ctx, w.router)
	if err != nil {
		return unavailable("KEA subnet topology could not be verified before application")
	}
	if err = verifySubnet(ProviderSnapshot{Resources: rows}, w.host, c, w.op.ResolvedSubnet); err != nil {
		return err
	}
	return w.checkShared(w.own.Public)
}
func (w *workflow) cleanup() error {
	for _, kind := range []string{"handle", "domain", "acl", "reservation"} {
		for _, ref := range append([]OwnedResource{}, w.own.Resources...) {
			if ref.Kind != kind {
				continue
			}
			if kind == "acl" && ref.ID == w.router.PrivateAccessListID {
				return conflict("selected shared access list cannot be deleted")
			}

			latestInventory := w.s.Providers.Inventory(w.ctx, w.s.Store.Read().Config)
			if e := cleanupSafe(latestInventory, *w.own); e != nil {
				return e
			}
			latest, e := w.s.Providers.Snapshot(w.ctx, w.router)
			if e != nil {
				return e
			}
			if e = checkOwned(*w.own, latest); e != nil {
				return e
			}
			if e = cleanupReferences(*w.own, latest); e != nil {
				return e
			}
			w.snap = latest
			r, ok := w.snap.Find(ref.ID, kind)
			if !ok || r.Fingerprint() != ref.Fingerprint {
				return conflict("owned resource changed")
			}
			if e := w.record("delete:"+kind, true); e != nil {
				return e
			}
			if e := w.s.Providers.Delete(w.ctx, w.router, r); e != nil {
				return e
			}
			for i, x := range w.own.Resources {
				if x.ID == ref.ID && x.Kind == kind {
					w.own.Resources = append(w.own.Resources[:i], w.own.Resources[i+1:]...)
					break
				}
			}
			if e := w.record("deleted:"+kind, true); e != nil {
				return e
			}
		}
	}
	if e := w.record("reconfigure", false); e != nil {
		return e
	}
	for _, service := range serviceApplicationOrder("cleanup") {
		if e := w.s.Providers.Apply(w.ctx, w.router, service); e != nil {
			return e
		}
	}
	return nil
}
func serviceApplicationOrder(action string) [2]string {
	if action == "cleanup" {
		return [2]string{"caddy", "kea"}
	}
	return [2]string{"kea", "caddy"}
}

func (s *Service) Recover(ctx context.Context, id string) (Operation, error) {
	s.mutate.Lock()
	defer s.mutate.Unlock()
	st := s.Store.Read()
	op, ok := st.Operations[id]
	if !ok {
		return op, &Fault{"notFound", "operation not found", 404}
	}
	if op.Status == "succeeded" {
		return op, conflict("operation already completed")
	}
	if op.ManualRecovery || op.Step != "reconfigure" {
		return op, conflict("write outcome or workflow is incomplete; manual provider inspection is required")
	}
	own, ok := st.Owned[op.Key]
	if !ok {
		return op, conflict("ownership record missing")
	}
	h, hostOK := st.Config.Host(own.HostID)
	if !hostOK {
		return op, conflict("owned host is missing")
	}
	o := routerForHost(st.Config, h)
	for _, service := range serviceApplicationOrder(op.Action) {
		inv := s.Providers.Inventory(ctx, st.Config)
		if e := inventoryError(inv); e != nil {
			return op, e
		}
		var desired map[string]string
		if op.Action == "cleanup" {
			if e := cleanupSafe(inv, own); e != nil {
				return op, e
			}
		} else {
			c, e := ensureContainer(inv, h, own.VMID)
			if e != nil {
				return op, e
			}
			if c.Identity != own.Identity || op.ContainerFingerprint == "" || c.Fingerprint != op.ContainerFingerprint {
				return op, conflict("container changed since the operation; inspect provider state before recovery")
			}
			route := own.DesiredRoute
			if !validRoute(route) {
				return op, conflict("app upstream is unavailable; inspect the legacy operation before recovery")
			}
			o.UpstreamScheme, o.UpstreamPort = route.Scheme, route.Port
			desired, e = desiredFields(o, c, h, own.Public)
			if e != nil {
				return op, e
			}
			if e = targetConflicts(inv, c, desired); e != nil {
				return op, e
			}
		}
		snap, e := s.Providers.Snapshot(ctx, o)
		if e != nil {
			return op, e
		}
		if !snap.Ready {
			return op, conflict("OPNsense prerequisites must be restored before recovery")
		}
		if e = checkOwned(own, snap); e != nil {
			return op, e
		}
		if op.Action == "ensure" {
			if e = sharedContextNotOwned(st, o, snap, own.Public); e != nil {
				return op, e
			}
			if e = verifySharedAccessList(o, snap, op.SharedAccessList, own.Public); e != nil {
				return op, e
			}
			c, _ := inv.Find(op.Key)
			if e = verifySubnet(snap, h, c, op.ResolvedSubnet); e != nil {
				return op, e
			}
			if op.DesiredRoute == nil || *op.DesiredRoute != own.DesiredRoute {
				return op, conflict("app route changed since the operation")
			}
			o.SubnetID = op.ResolvedSubnet.ID
			if e = checkConflicts(snap, own, desired); e != nil {
				return op, e
			}
			if !desiredResourcesMatch(o, h, c, own, snap, own.Public) {
				return op, conflict("recorded route differs from current desired configuration")
			}
		} else if e = cleanupReferences(own, snap); e != nil {
			return op, e
		}
		if e = s.Providers.Apply(ctx, o, service); e != nil {
			return op, e
		}
	}
	op.Status = "succeeded"
	op.Step = "complete"
	op.Error = ""
	op.UpdatedAt = time.Now().UTC()
	e := s.Store.Update(func(st *State) error {
		st.Operations[id] = op
		if op.Action == "cleanup" {
			delete(st.Owned, op.Key)
		}
		return nil
	})
	return op, e
}
func (s *Service) Operations() []Operation {
	out := []Operation{}
	for _, op := range s.Store.Read().Operations {
		out = append(out, op)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func configPolicy(c Config) string {
	c = clone(c)
	c.Revision = 0
	for i := range c.Hosts {
		c.Hosts[i].TokenSecret = ""
		c.Hosts[i].TokenID = ""
		c.Hosts[i].CAPEM = ""
	}
	for i := range c.OPNsenses {
		c.OPNsenses[i].APIKey = ""
		c.OPNsenses[i].APISecret = ""
		c.OPNsenses[i].CAPEM = ""
	}
	return fingerprint(c)
}
