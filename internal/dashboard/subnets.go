package dashboard

import (
	"context"
	"net/netip"
	"sort"
)

type SubnetReference struct {
	ID          string `json:"id"`
	CIDR        string `json:"cidr"`
	Fingerprint string `json:"fingerprint"`
}
type SubnetInfo struct {
	ID          string `json:"id"`
	CIDR        string `json:"cidr"`
	Description string `json:"description"`
}
type SubnetDiscovery struct {
	OPNsenseID string       `json:"opnsenseId"`
	Revision   int          `json:"revision"`
	Subnets    []SubnetInfo `json:"subnets"`
}

func (p *RealProviders) Subnets(ctx context.Context, o OPNsense) ([]Resource, error) {
	return p.listResources(ctx, o, "subnet")
}
func (p *DemoProviders) Subnets(ctx context.Context, o OPNsense) ([]Resource, error) {
	rows := []Resource{}
	for _, r := range p.Store.Read().DemoResources[o.ID] {
		if r.Kind == "subnet" {
			rows = append(rows, r)
		}
	}
	return rows, nil
}
func (s *Service) Subnets(ctx context.Context, id string) (SubnetDiscovery, error) {
	st := s.Store.Read()
	o, ok := st.Config.Router(id)
	if !ok {
		return SubnetDiscovery{}, &Fault{"notFound", "OPNsense configuration not found", 404}
	}
	rows, err := s.Providers.Subnets(ctx, o)
	if err != nil {
		return SubnetDiscovery{}, unavailable("KEA subnet discovery failed; verify saved connection and DHCPv4 API access")
	}
	out := SubnetDiscovery{OPNsenseID: id, Revision: st.Config.Revision, Subnets: []SubnetInfo{}}
	for _, r := range rows {
		if _, err := subnetPrefix(r); err != nil {
			return SubnetDiscovery{}, err
		}
		out.Subnets = append(out.Subnets, SubnetInfo{r.ID, r.Fields["subnet"], r.Fields["description"]})
	}
	sort.Slice(out.Subnets, func(i, j int) bool {
		return out.Subnets[i].CIDR+out.Subnets[i].ID < out.Subnets[j].CIDR+out.Subnets[j].ID
	})
	return out, nil
}
func subnetPrefix(r Resource) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(r.Fields["subnet"])
	if !uuidPattern.MatchString(r.ID) || err != nil || !p.Addr().Is4() || p != p.Masked() {
		return netip.Prefix{}, unavailable("KEA returned an invalid subnet definition")
	}
	return p, nil
}

// Count every containing subnet, rather than taking the first or longest
// match. Ambiguity is an operator topology issue, never a routing guess.
func resolveSubnet(snap ProviderSnapshot, h Host, c Container) (*SubnetReference, error) {
	_, ip, err := profileCheck(c, h)
	if err != nil {
		return nil, err
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil || !addr.Is4() {
		return nil, conflict("intended IPv4 is unavailable")
	}
	var match *SubnetReference
	seen := map[string]bool{}
	for _, r := range snap.Resources {
		if r.Kind != "subnet" {
			continue
		}
		prefix, err := subnetPrefix(r)
		if err != nil {
			return nil, err
		}
		if seen[r.ID] {
			return nil, unavailable("duplicate KEA subnet identity")
		}
		seen[r.ID] = true
		if !prefix.Contains(addr) {
			continue
		}
		if match != nil {
			return nil, conflict("multiple KEA subnets contain the intended IPv4; resolve overlapping topology before applying")
		}
		match = &SubnetReference{r.ID, prefix.String(), r.Fingerprint()}
	}
	if match == nil {
		return nil, conflict("no configured KEA subnet contains the intended IPv4")
	}
	return match, nil
}
func verifySubnet(snap ProviderSnapshot, h Host, c Container, ref *SubnetReference) error {
	match, err := resolveSubnet(snap, h, c)
	if err != nil {
		return err
	}
	if ref == nil || *match != *ref {
		return conflict("resolved KEA subnet changed since review; create a fresh plan")
	}
	return nil
}
