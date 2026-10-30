package dashboard

import (
	"context"
	"sort"
)

type ReconcileItem struct {
	ResolvedSubnet    *SubnetReference `json:"resolvedSubnet,omitempty"`
	PrivateAccessList *AccessList      `json:"privateAccessList,omitempty"`
	Key               string           `json:"key"`
	State             string           `json:"state"`
	Issues            []string         `json:"issues"`
	Owned             bool             `json:"owned"`
	DesiredRoute      *RouteConfig     `json:"desiredRoute,omitempty"`
	RouteUnavailable  bool             `json:"routeUnavailable,omitempty"`
}
type Reconciliation struct {
	Inventory  Inventory       `json:"inventory"`
	Items      []ReconcileItem `json:"items"`
	Operations []Operation     `json:"operations"`
}

func (s *Service) Reconcile(ctx context.Context) Reconciliation {
	st := s.Store.Read()
	inv := s.Providers.Inventory(ctx, st.Config)
	out := Reconciliation{decorateInventory(inv, st), []ReconcileItem{}, s.Operations()}
	snapshots := map[string]ProviderSnapshot{}
	failures := map[string]bool{}
	for _, h := range st.Config.Hosts {
		o := routerForHost(st.Config, h)
		snap, e := s.Providers.Snapshot(ctx, o)
		if e != nil {
			failures[h.ID] = true
		} else {
			snapshots[h.ID] = snap
		}
	}
	seen := map[string]bool{}
	for _, c := range inv.Containers {
		seen[c.Key] = true
		own, owned := st.Owned[c.Key]
		h, _ := st.Config.Host(c.HostID)
		o := routerForHost(st.Config, h)
		item := ReconcileItem{Key: c.Key, State: "missing", Issues: []string{}, Owned: owned}
		if !failures[h.ID] {
			subnet, err := resolveSubnet(snapshots[h.ID], h, c)
			if err == nil {
				item.ResolvedSubnet = subnet
				o.SubnetID = subnet.ID
			}
		}
		if o.PrivateAccessListID != "" {
			a := AccessList{ID: o.PrivateAccessListID, CIDRs: []string{}, Reason: "Definition unavailable; refresh provider state. Private plans require fresh verification."}
			if !failures[h.ID] {
				if r, exists := snapshots[h.ID].Find(o.PrivateAccessListID, "acl"); exists {
					a = describeAccessList(r)
				} else {
					a.Reason = "Selected shared access list is missing."
				}
			}
			item.PrivateAccessList = &a
		}
		if owned {
			if !validRoute(own.DesiredRoute) && !failures[h.ID] {
				if migrated, err := observedOwnedRoute(own, snapshots[h.ID]); err == nil {
					own.DesiredRoute = migrated
				}
			}
			route := own.DesiredRoute
			if validRoute(route) {
				item.DesiredRoute = &route
			} else {
				item.RouteUnavailable = true
			}
			for i, target := range out.Inventory.Containers {
				if target.Key == c.Key && validRoute(own.DesiredRoute) {
					out.Inventory.Containers[i].DesiredRoute = &route
				}
			}
			o.UpstreamScheme = route.Scheme
			o.UpstreamPort = route.Port
		}
		if !inv.Complete || failures[h.ID] {
			item.State = "unknown"
			item.Issues = append(item.Issues, "complete inventory and provider reads are required")
		} else if owned && item.RouteUnavailable {
			item.State = "mismatch"
			item.Issues = append(item.Issues, "existing app upstream is unavailable; explicitly choose this app's scheme and port before repair")
		} else if owned {
			item.State = "healthy"
			if item.ResolvedSubnet == nil {
				item.State = "mismatch"
				item.Issues = append(item.Issues, "intended IPv4 must match exactly one configured KEA subnet")
			} else if own.ResolvedSubnet != nil && *own.ResolvedSubnet != *item.ResolvedSubnet {
				item.State = "mismatch"
				item.Issues = append(item.Issues, "resolved KEA subnet changed since the recorded route")
			}
			if own.Identity != c.Identity || c.Identity == "" {
				item.State = "conflict"
				item.Issues = append(item.Issues, "container identity changed or unavailable")
			}
			if e := checkOwned(own, snapshots[h.ID]); e != nil {
				item.State = "mismatch"
				item.Issues = append(item.Issues, "owned resources changed or are missing")
			}
			d, e := desiredFields(o, c, h, own.Public)
			if e != nil {
				item.State = "mismatch"
				item.Issues = append(item.Issues, "container does not match configured host profile")
			} else {
				tls := "0"
				if o.UpstreamScheme == "https" {
					tls = "1"
				}
				aclID, domainID := "", ""
				for _, ref := range own.Resources {
					if ref.Kind == "acl" {
						aclID = ref.ID
					}
					if ref.Kind == "domain" {
						domainID = ref.ID
					}
				}
				if own.Public {
					aclID = ""
				} else if o.PrivateAccessListID != "" {
					aclID = o.PrivateAccessListID
					if e := verifySharedAccessList(o, snapshots[h.ID], own.SharedAccessList, false); e != nil {
						item.State = "mismatch"
						item.Issues = append(item.Issues, "shared private policy changed, is missing or is unsuitable; review a fresh plan")
					}
				}
				expected := map[string]map[string]string{"reservation": {"ip_address": d["ip"], "hw_address": d["mac"], "hostname": d["name"], "subnet": o.SubnetID}, "domain": {"FromDomain": d["domain"], "enabled": "1", "CustomCertificate": o.CertificateID, "accesslist": aclID}, "handle": {"ToDomain": d["ip"], "ToPort": itoa(o.UpstreamPort), "enabled": "1", "reverse": domainID, "HttpTls": tls, "HttpTlsInsecureSkipVerify": "0"}}
				if o.CertificateID == "" {
					delete(expected["domain"], "CustomCertificate")
				}
				if !own.Public && o.PrivateAccessListID == "" {
					expected["acl"] = map[string]string{"clientIps": canonicalCIDRs(o.AllowlistCIDRs), "accesslistInvert": "0", "RequestMatcher": "remote_ip"}
				}
				for kind, fields := range expected {
					found := false
					for _, ref := range own.Resources {
						if ref.Kind != kind {
							continue
						}
						r, ok := snapshots[h.ID].Find(ref.ID, kind)
						if !ok {
							continue
						}
						found = true
						for k, v := range fields {
							if r.Fields[k] != v {
								item.State = "mismatch"
								item.Issues = append(item.Issues, kind+" differs from desired configuration")
								break
							}
						}
					}
					if !found {
						item.State = "mismatch"
						item.Issues = append(item.Issues, "missing owned "+kind)
					}
				}
			}
		} else {
			d, e := desiredFields(o, c, h, false)
			if e != nil {
				item.State = "conflict"
				item.Issues = append(item.Issues, "container does not match host profile")
			} else if e = checkConflicts(snapshots[h.ID], own, d); e != nil {
				item.State = "conflict"
				item.Issues = append(item.Issues, "unowned reservation or domain conflicts")
			} else {
				item.Issues = append(item.Issues, "dashboard-owned reservation and proxy are missing")
			}
		}
		for _, op := range out.Operations {
			if op.Key == c.Key && op.Status != "succeeded" {
				item.State = "mismatch"
				item.Issues = append(item.Issues, "provider operation is incomplete; recovery required")
			}
		}
		out.Items = append(out.Items, item)
	}
	for k, own := range st.Owned {
		if seen[k] {
			continue
		}
		item := ReconcileItem{Key: k, State: "unknown", Issues: []string{}, Owned: true}
		if validRoute(own.DesiredRoute) {
			route := own.DesiredRoute
			item.DesiredRoute = &route
		}
		if !inv.Complete || failures[own.HostID] {
			item.Issues = append(item.Issues, "inventory/provider failure prevents orphan classification")
		} else if e := cleanupSafe(inv, own); e != nil {
			item.State = "conflict"
			item.Issues = append(item.Issues, "VMID exists elsewhere; possible migration or reuse")
		} else if _, e = existingOwnedResources(own, snapshots[own.HostID]); e != nil {
			item.State = "mismatch"
			item.Issues = append(item.Issues, "owned provider resources changed")
		} else if e = cleanupReferences(own, snapshots[own.HostID]); e != nil {
			item.State = "conflict"
			item.Issues = append(item.Issues, "unrelated configuration references owned resources")
		} else {
			item.State = "ownedOrphan"
			item.Issues = append(item.Issues, "fully observed inventory has no matching VMID; cleanup requires explicit plan")
		}
		out.Items = append(out.Items, item)
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].Key < out.Items[j].Key })
	return out
}
