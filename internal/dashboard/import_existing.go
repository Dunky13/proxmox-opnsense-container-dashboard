package dashboard

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// RouteLayout records per-app routing separately from an external parent. A nil
// layout retains the legacy dashboard-created standalone resource behavior.
type RouteLayout struct {
	Kind   string         `json:"kind"`
	Parent *OwnedResource `json:"parent,omitempty"`
}

type ExistingResource struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	Summary string `json:"summary"`
}

type importCandidate struct {
	Route     *RouteConfig
	Layout    *RouteLayout
	Resources []Resource
	Context   []Resource
	Existing  []ExistingResource
	Issues    []string
	Subnet    *SubnetReference
	Shared    *AccessListReference
}

// Explicit allowlists prevent arbitrary getter fields, headers, auth values and
// descriptions from being reflected in diagnostics or review effects.
var observationFields = map[string][]string{
	"reservation": {"hostname", "ip_address", "hw_address", "subnet"},
	"domain":      {"FromDomain", "enabled", "accesslist", "DisableTls", "FromPort", "DnsChallenge", "CustomCertificate"},
	"subdomain":   {"FromDomain", "enabled", "reverse", "accesslist"},
	"handle":      {"enabled", "reverse", "subdomain", "HandleType", "HandleDirective", "ToDomain", "ToPort", "HttpTls", "HttpTlsInsecureSkipVerify", "accesslist"},
	"acl":         {"clientIps", "accesslistInvert", "RequestMatcher", "HttpResponseCode"},
}

func safeObservation(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	if len(value) > 512 {
		return value[:512] + "…"
	}
	return value
}
func resourceObservations(r Resource) []FieldChange {
	out := []FieldChange{}
	for _, field := range observationFields[r.Kind] {
		if value, ok := r.Fields[field]; ok {
			value = safeObservation(effectValue(field, value))
			out = append(out, FieldChange{Field: effectField(field), Before: value, After: value})
		}
	}
	return out
}
func existingResource(r Resource, relation string) ExistingResource {
	parts := []string{relation}
	for _, f := range resourceObservations(r) {
		parts = append(parts, f.Field+"="+f.Before)
	}
	name := r.Fields["FromDomain"]
	if r.Kind == "reservation" {
		name = r.Fields["hostname"]
	}
	if r.Kind == "handle" {
		name = r.Fields["ToDomain"] + ":" + r.Fields["ToPort"]
	}
	return ExistingResource{Kind: r.Kind, ID: r.ID, Name: safeObservation(name), Summary: strings.Join(parts, "; ")}
}
func domainMatches(value, domain string) bool {
	for _, token := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		if strings.EqualFold(strings.TrimSuffix(token, "."), strings.TrimSuffix(domain, ".")) {
			return true
		}
	}
	return false
}
func singleDomain(value, domain string) bool {
	return strings.EqualFold(strings.TrimSuffix(strings.TrimSpace(value), "."), strings.TrimSuffix(domain, "."))
}
func reservationMatches(r Resource, desired map[string]string) (bool, bool) {
	ip := r.Fields["ip_address"] == desired["ip"]
	mac := strings.EqualFold(r.Fields["hw_address"], desired["mac"])
	return ip || mac, ip && mac
}
func resourceOwned(st State, routerID string, r Resource) bool {
	for _, own := range st.Owned {
		if own.OPNsenseID != routerID {
			continue
		}
		for _, ref := range own.Resources {
			if ref.Kind == r.Kind && ref.ID == r.ID {
				return true
			}
		}
	}
	return false
}
func checkedSnapshot(snap ProviderSnapshot) error {
	seen := map[string]bool{}
	for _, r := range snap.Resources {
		k := r.Kind + ":" + r.ID
		if seen[r.ID] {
			return conflict("duplicate provider resource identity: " + k)
		}
		seen[r.ID] = true
		if r.Kind != "caddyGeneral" && !uuidPattern.MatchString(r.ID) {
			return conflict("invalid provider resource identity for " + r.Kind)
		}
	}
	return nil
}
func observedRoute(handle Resource, public bool) (RouteConfig, error) {
	port, err := strconv.Atoi(handle.Fields["ToPort"])
	scheme := "http"
	if handle.Fields["HttpTls"] == "1" {
		scheme = "https"
	} else if handle.Fields["HttpTls"] != "0" {
		return RouteConfig{}, conflict("handle " + handle.ID + " has unsupported upstream TLS scheme")
	}
	route := RouteConfig{Scheme: scheme, Port: port, Public: public}
	if err != nil || !validRoute(route) {
		return route, conflict("handle " + handle.ID + " has unsupported upstream port")
	}
	return route, nil
}
func unsupportedRoute(r Resource) error {
	if r.Fields["enabled"] != "1" {
		return conflict(r.Kind + " " + r.ID + " is disabled; reviewed import requires an enabled route")
	}
	for _, field := range []string{"basicauth", "ClientAuthMode", "ClientAuthTrustPool", "AcmePassthrough"} {
		if r.Fields[field] != "" {
			return conflict(r.Kind + " " + r.ID + " uses unsupported " + field + "; exposure cannot be represented safely")
		}
	}
	if r.Kind == "handle" {
		if r.Fields["HandleType"] != "handle" || r.Fields["HandleDirective"] != "reverse_proxy" {
			return conflict("handle " + r.ID + " is not a single ordinary reverse_proxy handle")
		}
		for _, field := range []string{"HandlePath", "ToPath", "accesslist", "HttpTlsServerName", "HttpTlsTrustedCaCerts"} {
			if r.Fields[field] != "" {
				return conflict("handle " + r.ID + " uses unsupported " + field + "; import would not preserve managed route semantics")
			}
		}
		for _, field := range []string{"HttpTlsInsecureSkipVerify", "ForwardAuth"} {
			if r.Fields[field] != "" && r.Fields[field] != "0" {
				return conflict("handle " + r.ID + " uses unsupported " + field)
			}
		}
	}
	return nil
}
func parentContext(st State, o OPNsense, snap ProviderSnapshot, id string) (Resource, error) {
	if !uuidPattern.MatchString(id) {
		return Resource{}, conflict("child route must reference exactly one valid parent UUID")
	}
	parent, exists := snap.Find(id, "domain")
	if !exists {
		return parent, conflict("child route parent " + id + " is missing")
	}
	if resourceOwned(st, o.ID, parent) {
		return parent, conflict("child route parent " + id + " is dashboard-owned; shared parents must remain external")
	}
	if err := unsupportedRoute(parent); err != nil {
		return parent, err
	}
	acl, observed := parent.Fields["accesslist"]
	if !observed || acl != "" {
		return parent, conflict("parent " + id + " access policy must be observed and empty; inherited or layered policy is unsupported")
	}
	return parent, nil
}

func inspectImport(st State, inv Inventory, h Host, o OPNsense, c Container, snap ProviderSnapshot) importCandidate {
	out := importCandidate{Resources: []Resource{}, Context: []Resource{}, Existing: []ExistingResource{}, Issues: []string{}}
	issue := func(err error) {
		if err != nil {
			out.Issues = append(out.Issues, err.Error())
		}
	}
	if err := checkedSnapshot(snap); err != nil {
		issue(err)
		return out
	}
	desired, err := desiredFields(o, c, h, false)
	if err != nil {
		issue(err)
		return out
	}
	reservations, sites := []Resource{}, []Resource{}
	for _, r := range snap.Resources {
		if r.Kind == "reservation" {
			match, exact := reservationMatches(r, desired)
			if match {
				relation := "collision: only IP or MAC matches"
				if exact {
					relation = "exact IP and MAC match; unowned"
				}
				out.Existing = append(out.Existing, existingResource(r, relation))
				reservations = append(reservations, r)
			}
		}
		if (r.Kind == "domain" || r.Kind == "subdomain") && domainMatches(r.Fields["FromDomain"], desired["domain"]) {
			out.Existing = append(out.Existing, existingResource(r, "matching app domain; unowned"))
			sites = append(sites, r)
		}
	}
	// Diagnostics include every matching candidate even when eligibility fails.
	handles := []Resource{}
	for _, r := range snap.Resources {
		if r.Kind != "handle" {
			continue
		}
		for _, site := range sites {
			related := (site.Kind == "subdomain" && r.Fields["subdomain"] == site.ID) || (site.Kind == "domain" && r.Fields["reverse"] == site.ID)
			if related {
				handles = append(handles, r)
				out.Existing = append(out.Existing, existingResource(r, "related app handle"))
				break
			}
		}
	}
	if len(out.Existing) == 0 {
		return out
	}
	if _, owned := st.Owned[c.Key]; owned {
		issue(conflict("container already has ownership; import is unavailable"))
	}
	if routerBlocked(st, o.ID) {
		issue(conflict("OPNsense has unfinished operations"))
	}
	if !snap.Ready {
		issue(conflict("OPNsense prerequisites are missing"))
	}
	_, err = ensureContainer(inv, h, c.VMID)
	issue(err)
	issue(targetConflicts(inv, c, desired))
	out.Subnet, err = resolveSubnet(snap, h, c)
	issue(err)
	if len(reservations) != 1 {
		issue(conflict(fmt.Sprintf("expected one exact reservation; found %d IP/MAC candidates", len(reservations))))
	} else {
		r := reservations[0]
		_, exact := reservationMatches(r, desired)
		if !exact {
			issue(conflict("reservation " + r.ID + " collides with the intended IP or MAC; both must match exactly"))
		}
		if out.Subnet != nil && r.Fields["subnet"] != out.Subnet.ID {
			issue(conflict("reservation " + r.ID + " is not in the uniquely resolved KEA subnet " + out.Subnet.ID))
		}
		out.Resources = append(out.Resources, r)
	}
	if len(sites) != 1 {
		issue(conflict(fmt.Sprintf("expected one standalone domain or child domain for %s; found %d", desired["domain"], len(sites))))
	}
	if len(handles) != 1 {
		issue(conflict(fmt.Sprintf("expected one unambiguous related handle; found %d", len(handles))))
	}
	if len(sites) == 1 {
		site := sites[0]
		out.Resources = append(out.Resources, site)
		out.Layout = &RouteLayout{Kind: site.Kind}
		if !singleDomain(site.Fields["FromDomain"], desired["domain"]) {
			issue(conflict(site.Kind + " " + site.ID + " has multiple domain names; shared routes cannot be imported"))
		}
		issue(unsupportedRoute(site))
		reverse, subdomain := site.ID, ""
		if site.Kind == "subdomain" {
			reverse, subdomain = site.Fields["reverse"], site.ID
			parent, err := parentContext(st, o, snap, reverse)
			issue(err)
			if parent.ID != "" {
				out.Context = append(out.Context, parent)
				out.Existing = append(out.Existing, existingResource(parent, "shared parent context; retained external, never imported"))
				out.Layout.Parent = &OwnedResource{ID: parent.ID, Kind: "domain", Fingerprint: parent.Fingerprint()}
			}
		} else {
			for _, r := range snap.Resources {
				if r.Kind == "subdomain" && r.Fields["reverse"] == site.ID {
					issue(conflict("domain " + site.ID + " is a shared parent of child " + r.ID + "; it cannot be imported"))
				}
			}
		}
		if len(handles) == 1 {
			handle := handles[0]
			out.Resources = append(out.Resources, handle)
			if handle.Fields["reverse"] != reverse || handle.Fields["subdomain"] != subdomain {
				issue(conflict("handle " + handle.ID + " has inconsistent reverse/subdomain relationships"))
			}
			if handle.Fields["ToDomain"] != desired["ip"] {
				issue(conflict("handle " + handle.ID + " targets " + safeObservation(handle.Fields["ToDomain"]) + " instead of the intended IPv4 " + desired["ip"]))
			}
			issue(unsupportedRoute(handle))
			aclID, observed := site.Fields["accesslist"]
			if !observed {
				issue(conflict(site.Kind + " " + site.ID + " access policy was not observed"))
			}
			route, err := observedRoute(handle, aclID == "")
			issue(err)
			if err == nil {
				out.Route = &route
			}
			if aclID != "" {
				if aclID != o.PrivateAccessListID {
					issue(conflict("existing private ACL " + aclID + " must equal the configured external private policy; import does not change policy"))
				} else {
					acl, err := selectedAccessList(o, snap)
					issue(err)
					if err == nil {
						issue(sharedListNotOwned(st, o.ID, aclID))
						issue(sharedContextNotOwned(st, o, snap, false))
						out.Shared = referenceAccessList(acl, snap)
						out.Context = append(out.Context, acl)
					}
				}
			}
		}
	}
	candidateOwn := Ownership{Resources: []OwnedResource{}}
	for _, r := range out.Resources {
		if resourceOwned(st, o.ID, r) {
			issue(conflict(r.Kind + " " + r.ID + " already belongs to a dashboard ownership record"))
		}
		candidateOwn.Resources = append(candidateOwn.Resources, OwnedResource{ID: r.ID, Kind: r.Kind, Fingerprint: r.Fingerprint()})
	}
	issue(cleanupReferences(candidateOwn, snap))
	// Exposure is withheld if policy ancestry or route shape was unsupported.
	// Diagnostics must not label an unknown effective private route as public.
	if len(out.Issues) != 0 {
		out.Route = nil
	}
	sort.Slice(out.Existing, func(i, j int) bool {
		return out.Existing[i].Kind+out.Existing[i].ID < out.Existing[j].Kind+out.Existing[j].ID
	})
	return out
}
func (c importCandidate) eligible() bool {
	return len(c.Issues) == 0 && c.Route != nil && c.Layout != nil && c.Subnet != nil && len(c.Resources) == 3
}
func importRefs(rows []Resource) []OwnedResource {
	refs := []OwnedResource{}
	for _, r := range rows {
		ref := OwnedResource{ID: r.ID, Kind: r.Kind, Fingerprint: r.Fingerprint()}
		if r.Kind == "reservation" {
			ref.SubnetID = r.Fields["subnet"]
		}
		refs = append(refs, ref)
	}
	return refs
}
func (s *Service) importPlan(st State, req PlanRequest, h Host, o OPNsense, inv Inventory, snap ProviderSnapshot) (Plan, error) {
	if req.UpstreamPort != nil || req.UpstreamScheme != nil {
		return Plan{}, bad("import preserves the observed route and does not accept upstream overrides")
	}
	c, err := ensureContainer(inv, h, req.VMID)
	if err != nil {
		return Plan{}, err
	}
	candidate := inspectImport(st, inv, h, o, c, snap)
	if !candidate.eligible() {
		reason := "no matching existing resources"
		if len(candidate.Issues) != 0 {
			reason = strings.Join(candidate.Issues, "; ")
		}
		return Plan{}, conflict("reviewed import unavailable: " + reason)
	}
	effects := []PlanEffect{}
	for _, r := range candidate.Resources {
		effects = append(effects, PlanEffect{Kind: r.Kind, Action: "adopt", ResourceID: r.ID, Name: "Import ownership of existing " + r.Kind, Changes: resourceObservations(r)})
	}
	for _, r := range candidate.Context {
		effects = append(effects, PlanEffect{Kind: r.Kind, Action: "retain", ResourceID: r.ID, Name: "Retain external context; excluded from ownership and cleanup", Changes: resourceObservations(r)})
	}
	_, intendedIP, _ := profileCheck(c, h)
	notes := []string{
		"Metadata-only reviewed import: NO provider Create, Set, Delete or service application calls. All UUIDs, current fields, advanced settings and exposure remain unchanged.",
		"Ownership transfers only for the exact reservation, app domain or child domain, and related handle. Future reviewed maintenance may update these records, and reviewed orphan cleanup may delete them.",
		"Shared parent, siblings, selected ACLs, trusted proxy lists, headers, certificates and global DNS/TLS configuration remain externally managed and unchanged.",
		"Public state remains unchanged: " + strconv.FormatBool(candidate.Route.Public) + ". The observed app upstream is preserved: " + candidate.Route.Scheme + "://" + intendedIP + ":" + itoa(candidate.Route.Port) + ".",
	}
	for _, r := range candidate.Resources {
		if r.Kind == "reservation" && r.Fields["hostname"] != c.Name {
			notes = append(notes, "Existing reservation hostname "+safeObservation(r.Fields["hostname"])+" is preserved during import. It differs from "+c.Name+"; a subsequent standard reviewed ensure plan can align it.")
		}
	}
	if candidate.Shared != nil && candidate.Shared.ClientIPContext != nil {
		notes = append(notes, candidate.Shared.ClientIPContext.Summary)
	}
	now := time.Now().UTC()
	p := Plan{ID: newID(), Action: "import", Key: c.Key, HostID: h.ID, VMID: c.VMID, OPNsenseID: o.ID, Identity: c.Identity, Public: candidate.Route.Public, DesiredRoute: *candidate.Route, Effects: effects, ApplyNotes: notes, RouteLayout: candidate.Layout, ImportResources: importRefs(candidate.Resources), ResolvedSubnet: candidate.Subnet, SharedAccessList: candidate.Shared, OwnershipHash: fingerprint(st.Owned), ConfigHash: fingerprint(st.Config), Revision: st.Config.Revision, InventoryHash: inv.StableHash(), ProviderHash: fingerprint(snap), CreatedAt: now, ExpiresAt: now.Add(5 * time.Minute)}
	err = s.savePlan(p, now)
	return p, err
}
func (s *Service) savePlan(p Plan, now time.Time) error {
	return s.Store.Update(func(st *State) error {
		for id, old := range st.Plans {
			if old.ExpiresAt.Before(now) {
				delete(st.Plans, id)
			}
		}
		if len(st.Plans) >= 200 {
			return conflict("too many pending plans")
		}
		st.Plans[p.ID] = p
		return nil
	})
}
func (s *Service) applyImport(ctx context.Context, st State, p Plan, h Host, o OPNsense, inv Inventory, snap ProviderSnapshot) (Operation, error) {
	c, err := ensureContainer(inv, h, p.VMID)
	if err != nil {
		return Operation{}, err
	}
	if c.Identity != p.Identity {
		return Operation{}, conflict("container identity changed")
	}
	candidate := inspectImport(st, inv, h, o, c, snap)
	if !candidate.eligible() {
		return Operation{}, conflict("reviewed import eligibility changed: " + strings.Join(candidate.Issues, "; "))
	}
	if candidate.Route == nil || *candidate.Route != p.DesiredRoute || p.Public != candidate.Route.Public || fingerprint(candidate.Layout) != fingerprint(p.RouteLayout) || fingerprint(importRefs(candidate.Resources)) != fingerprint(p.ImportResources) || fingerprint(candidate.Subnet) != fingerprint(p.ResolvedSubnet) || fingerprint(candidate.Shared) != fingerprint(p.SharedAccessList) {
		return Operation{}, conflict("reviewed import bindings changed; create a fresh plan")
	}
	if err = ctx.Err(); err != nil {
		return Operation{}, err
	}
	now := time.Now().UTC()
	own := Ownership{Key: c.Key, HostID: h.ID, VMID: c.VMID, OPNsenseID: o.ID, Identity: c.Identity, Public: p.Public, DesiredRoute: p.DesiredRoute, Resources: importRefs(candidate.Resources), ResolvedSubnet: candidate.Subnet, SharedAccessList: candidate.Shared, RouteLayout: candidate.Layout, ResourceTemplates: clone(candidate.Resources)}
	op := Operation{ID: newID(), PlanID: p.ID, Key: c.Key, OPNsenseID: o.ID, Action: "import", Status: "succeeded", Step: "complete", CreatedAt: now, UpdatedAt: now, ContainerFingerprint: c.Fingerprint, DesiredRoute: &p.DesiredRoute, ResolvedSubnet: candidate.Subnet, SharedAccessList: candidate.Shared, Resources: own.Resources}
	// One durable commit includes ownership, plan consumption and terminal success.
	// A lost response cannot run the import again. Store persistence uncertainty
	// poisons subsequent mutations, including when rename succeeded but fsync failed.
	err = s.Store.Update(func(next *State) error {
		current, exists := next.Plans[p.ID]
		if !exists || fingerprint(current) != fingerprint(p) || fingerprint(next.Owned) != p.OwnershipHash || next.Config.Revision != p.Revision || fingerprint(next.Config) != p.ConfigHash || routerBlocked(*next, o.ID) {
			return conflict("import plan or ownership changed")
		}
		if time.Now().After(p.ExpiresAt) {
			return conflict("plan expired during review verification")
		}
		next.Owned[c.Key] = own
		delete(next.Plans, p.ID)
		next.Operations[op.ID] = op
		return nil
	})
	if err != nil {
		return Operation{}, err
	}
	return op, nil
}
