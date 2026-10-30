package dashboard

import (
	"context"
	"net/netip"
	"sort"
	"strings"
)

// A reference binds external policy without recording dashboard ownership.
type AccessListReference struct {
	ID                         string           `json:"id"`
	Fingerprint                string           `json:"fingerprint"`
	ClientIPContextFingerprint string           `json:"clientIpContextFingerprint,omitempty"`
	ClientIPContext            *ClientIPContext `json:"clientIpContext,omitempty"`
}
type AccessList struct {
	ClientIPContext *ClientIPContext `json:"clientIpContext,omitempty"`
	ID              string           `json:"id"`
	Name            string           `json:"name"`
	Description     string           `json:"description"`
	CIDRs           []string         `json:"cidrs"`
	Matcher         string           `json:"matcher"`
	Inversion       string           `json:"inversion"`
	Suitable        bool             `json:"suitable"`
	Reason          string           `json:"reason"`
}
type AccessListDiscovery struct {
	OPNsenseID  string       `json:"opnsenseId"`
	Revision    int          `json:"revision"`
	AccessLists []AccessList `json:"accessLists"`
}

func describeAccessList(r Resource) AccessList {
	a := AccessList{ID: r.ID, Name: r.Fields["accesslistName"], Description: r.Fields["description"], CIDRs: []string{}, Matcher: r.Fields["RequestMatcher"], Inversion: r.Fields["accesslistInvert"]}
	if value := canonicalCIDRs(strings.Split(r.Fields["clientIps"], ",")); value != "" {
		a.CIDRs = strings.Split(value, ",")
	}
	switch {
	case a.Inversion != "0":
		a.Reason = "Inversion must be disabled."
	case a.Matcher != "remote_ip" && a.Matcher != "client_ip":
		a.Reason = "Only native remote_ip and client_ip matchers are supported for private routes."
	case len(a.CIDRs) == 0:
		a.Reason = "An explicit restricted CIDR list is required."
	default:
		a.Reason = restrictedCIDRs(a.CIDRs)
	}
	a.Suitable = a.Reason == ""

	return a
}

func restrictedCIDRs(values []string) string {
	if len(values) == 0 {
		return "An explicit restricted CIDR list is required."
	}
	for _, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err != nil || prefix.Bits() == 0 || prefix != prefix.Masked() || prefix.Addr().Is4In6() {
			return "Only explicit restricted canonical CIDRs are supported; empty or unrestricted ranges are unsafe."
		}
	}
	if unrestrictedCIDRs(values) {
		return "The combined CIDRs allow an entire address family. Use restricted networks."
	}
	return ""
}
func describePolicyAccessList(r Resource, snap ProviderSnapshot) AccessList {
	a := describeAccessList(r)
	if a.Matcher == "client_ip" && a.Suitable {
		a.ClientIPContext = snap.ClientIPContext
		if a.ClientIPContext == nil || !a.ClientIPContext.Suitable {
			a.Suitable = false
			a.Reason = "Caddy client IP trust context is unavailable."
			if a.ClientIPContext != nil {
				a.Reason = a.ClientIPContext.Reason
			}
		}
	}
	return a
}
func referenceAccessList(r Resource, snap ProviderSnapshot) *AccessListReference {
	ref := &AccessListReference{ID: r.ID, Fingerprint: r.Fingerprint()}
	if r.Fields["RequestMatcher"] == "client_ip" {
		ref.ClientIPContext = snap.ClientIPContext
		ref.ClientIPContextFingerprint = fingerprint(snap.ClientIPContext)
	}
	return ref
}

// Adjacent prefixes can collectively allow all clients even without an
// explicit /0. Collapse complete sibling pairs before assessing restriction.
func unrestrictedCIDRs(values []string) bool {
	prefixes := map[netip.Prefix]bool{}
	for _, value := range values {
		if p, err := netip.ParsePrefix(value); err == nil {
			prefixes[p] = true
		}
	}
	for bits := 128; bits > 0; bits-- {
		parents := map[netip.Prefix]int{}
		for p := range prefixes {
			if p.Bits() == bits {
				parents[netip.PrefixFrom(p.Addr(), bits-1).Masked()]++
			}
		}
		for parent, count := range parents {
			if count == 2 {
				prefixes[parent] = true
			}
		}
	}
	for p := range prefixes {
		if p.Bits() == 0 {
			return true
		}
	}
	return false
}
func selectedAccessList(o OPNsense, snap ProviderSnapshot) (Resource, error) {
	r, ok := snap.Find(o.PrivateAccessListID, "acl")
	if !ok {
		return r, conflict("selected shared access list is missing")
	}
	a := describePolicyAccessList(r, snap)
	if !a.Suitable {
		return r, conflict("selected shared access list is unsuitable: " + a.Reason)
	}
	return r, nil
}
func verifySharedAccessList(o OPNsense, snap ProviderSnapshot, ref *AccessListReference, public bool) error {
	if public {
		return nil
	}
	if o.PrivateAccessListID == "" {
		if ref != nil {
			return conflict("shared access list selection changed")
		}
		if len(o.AllowlistCIDRs) == 0 {
			return conflict("private routes require configured allowlist CIDRs")
		}
		return nil
	}
	r, err := selectedAccessList(o, snap)
	if err != nil {
		return err
	}
	if ref == nil || ref.ID != r.ID || ref.Fingerprint != r.Fingerprint() {
		return conflict("shared access list changed since review; create a fresh plan")
	}
	if r.Fields["RequestMatcher"] == "client_ip" && (ref.ClientIPContextFingerprint == "" || ref.ClientIPContext == nil || ref.ClientIPContextFingerprint != fingerprint(ref.ClientIPContext) || ref.ClientIPContextFingerprint != fingerprint(snap.ClientIPContext)) {
		return conflict("Caddy client IP trust context changed or was not reviewed; create a fresh plan")
	}
	return nil
}
func (p *RealProviders) AccessLists(ctx context.Context, o OPNsense) ([]Resource, error) {
	return p.listResources(ctx, o, "acl")
}
func (p *DemoProviders) AccessLists(ctx context.Context, o OPNsense) ([]Resource, error) {
	rows := []Resource{}
	for _, r := range p.Store.Read().DemoResources[o.ID] {
		if r.Kind == "acl" {
			rows = append(rows, r)
		}
	}
	return rows, nil
}
func (s *Service) AccessLists(ctx context.Context, id string) (AccessListDiscovery, error) {
	st := s.Store.Read()
	o, ok := st.Config.Router(id)
	if !ok {
		return AccessListDiscovery{}, &Fault{"notFound", "OPNsense configuration not found", 404}
	}
	policy, err := s.Providers.CaddyPolicy(ctx, o)
	if err != nil {
		return AccessListDiscovery{}, unavailable("Caddy access list discovery failed; verify saved address, CA trust, credentials and os-caddy API access")
	}
	out := AccessListDiscovery{OPNsenseID: id, Revision: st.Config.Revision, AccessLists: []AccessList{}}
	for _, r := range policy.Resources {
		if r.Kind != "acl" {
			continue
		}
		a := describePolicyAccessList(r, policy.snapshot())
		if a.ClientIPContext != nil && a.ClientIPContext.TrustedProxyListID != "" {
			if err := sharedListNotOwned(st, id, a.ClientIPContext.TrustedProxyListID); err != nil {
				a.Suitable = false
				a.Reason = "Global trusted proxy list is dashboard-owned; its external stability cannot be verified."
			}
		}
		// An owned generated ACL must never be promoted into shared policy: its
		// original container could otherwise edit/delete it during later cleanup.
		for _, own := range st.Owned {
			if own.OPNsenseID != id {
				continue
			}
			for _, ref := range own.Resources {
				if ref.Kind == "acl" && ref.ID == r.ID {
					a.Suitable = false
					a.Reason = "Dashboard-owned list; use an externally managed shared list instead."
				}
			}
		}
		out.AccessLists = append(out.AccessLists, a)
	}
	sort.Slice(out.AccessLists, func(i, j int) bool { a, b := out.AccessLists[i], out.AccessLists[j]; return a.Name+a.ID < b.Name+b.ID })
	return out, nil
}

func sharedContextNotOwned(st State, o OPNsense, snap ProviderSnapshot, public bool) error {
	if public || o.PrivateAccessListID == "" {
		return nil
	}
	r, ok := snap.Find(o.PrivateAccessListID, "acl")
	if ok && r.Fields["RequestMatcher"] == "client_ip" && snap.ClientIPContext != nil && snap.ClientIPContext.TrustedProxyListID != "" {
		return sharedListNotOwned(st, o.ID, snap.ClientIPContext.TrustedProxyListID)
	}
	return nil
}
func sharedListNotOwned(st State, routerID, id string) error {
	for _, own := range st.Owned {
		if own.OPNsenseID != routerID {
			continue
		}
		for _, ref := range own.Resources {
			if ref.Kind == "acl" && ref.ID == id {
				return conflict("selected shared access list is dashboard-owned; select an external list")
			}
		}
	}
	return nil
}
