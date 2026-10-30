package dashboard

import (
	"context"
	"sort"
)

type ReconcileItem struct {
	CleanupSelectable  bool               `json:"cleanupSelectable,omitempty"`
	CanAddReservation  bool               `json:"canAddReservation,omitempty"`
	ReservationBlocked string             `json:"reservationBlocked,omitempty"`
	ManagedResources   []ExistingResource `json:"managedResources,omitempty"`
	Drift              []ResourceDrift    `json:"drift,omitempty"`
	CanAcceptChanges   bool               `json:"canAcceptChanges,omitempty"`
	AcceptBlocked      string             `json:"acceptBlocked,omitempty"`
	ImportAvailable    bool               `json:"importAvailable,omitempty"`
	ImportIssues       []string           `json:"importIssues,omitempty"`
	ExistingRoute      *RouteConfig       `json:"existingRoute,omitempty"`
	ExistingResources  []ExistingResource `json:"existingResources,omitempty"`
	ResolvedSubnet     *SubnetReference   `json:"resolvedSubnet,omitempty"`
	PrivateAccessList  *AccessList        `json:"privateAccessList,omitempty"`
	Key                string             `json:"key"`
	State              string             `json:"state"`
	Issues             []string           `json:"issues"`
	Owned              bool               `json:"owned"`
	DesiredRoute       *RouteConfig       `json:"desiredRoute,omitempty"`
	RouteUnavailable   bool               `json:"routeUnavailable,omitempty"`
}
type Reconciliation struct {
	ProtectedCandidates []ProtectedCandidate `json:"protectedCandidates,omitempty"`
	Inventory           Inventory            `json:"inventory"`
	Items               []ReconcileItem      `json:"items"`
	Operations          []Operation          `json:"operations"`
}

func (s *Service) Reconcile(ctx context.Context) Reconciliation {
	st := s.Store.Read()
	inv := s.Providers.Inventory(ctx, st.Config)
	return reconcile(st, inv, s.observeRouters(ctx, st.Config))
}

type routerObservations struct {
	Snapshots map[string]ProviderSnapshot
	Failures  map[string]bool
}

func (s *Service) observeRouters(ctx context.Context, cfg Config) routerObservations {
	snapshots := map[string]ProviderSnapshot{}
	failures := map[string]bool{}
	byRouter := map[string]ProviderSnapshot{}
	routerFailures := map[string]bool{}
	for _, h := range cfg.Hosts {
		o := routerForHost(cfg, h)
		if _, read := byRouter[o.ID]; !read && !routerFailures[o.ID] {
			snap, err := s.Providers.Snapshot(ctx, o)
			if err != nil {
				routerFailures[o.ID] = true
			} else {
				byRouter[o.ID] = snap
			}
		}
		failures[h.ID] = routerFailures[o.ID] || !byRouter[o.ID].Ready
		snapshots[h.ID] = byRouter[o.ID]
	}
	return routerObservations{snapshots, failures}
}

func reconcile(st State, inv Inventory, routers routerObservations) Reconciliation {
	out := Reconciliation{Inventory: decorateInventory(inv, st), Items: []ReconcileItem{}, Operations: operations(st), ProtectedCandidates: protectedCandidates(st, inv, routers)}
	snapshots, failures := routers.Snapshots, routers.Failures
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
					a = describePolicyAccessList(r, snapshots[h.ID])
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
		if owned && inv.Complete && !failures[h.ID] {
			item.Drift = ownedDrift(own, snapshots[h.ID])
			if len(item.Drift) > 0 {
				_, _, err := reviewOwnedChanges(st, inv, h, o, own, snapshots[h.ID])
				item.CanAcceptChanges = err == nil && !routerBlocked(st, o.ID)
				if err != nil {
					item.AcceptBlocked = err.Error()
				}
			}
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
			_, e := desiredFields(o, c, h, own.Public)
			if e != nil {
				item.State = "mismatch"
				item.Issues = append(item.Issues, "container does not match configured host profile")
			} else {
				if err := verifyRouteLayout(st, o, own, snapshots[h.ID]); err != nil {
					item.State = "mismatch"
					item.Issues = append(item.Issues, err.Error())
				}
				if own.RouteLayout != nil {
					if err := cleanupReferences(own, snapshots[h.ID]); err != nil {
						item.State = "conflict"
						item.Issues = append(item.Issues, err.Error())
					}
				}
				acl, _ := ownedByKind(own, "acl")
				site, _ := ownedByKind(own, routeKind(own))
				specs, err := ownedRouteSpecs(o, h, c, own, snapshots[h.ID], own.Public, acl.ID, site.ID)
				if err != nil {
					item.State = "mismatch"
					item.Issues = append(item.Issues, err.Error())
				}
				if err := verifySharedAccessList(o, snapshots[h.ID], own.SharedAccessList, own.Public); err != nil {
					item.State = "mismatch"
					item.Issues = append(item.Issues, err.Error())
				}
				for kind, fields := range specs {
					ref, _ := ownedByKind(own, kind)
					r, exists := snapshots[h.ID].Find(ref.ID, kind)
					if !exists {
						item.State = "mismatch"
						item.Issues = append(item.Issues, "missing owned "+kind)
						continue
					}
					for field, value := range fields {
						if r.Fields[field] != value {
							item.State = "mismatch"
							item.Issues = append(item.Issues, kind+" differs from desired configuration")
							break
						}
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
				item.Issues = append(item.Issues, e.Error())
			} else {
				item.Issues = append(item.Issues, "dashboard-owned reservation and proxy are missing")
			}
		}

		if !owned && inv.Complete && !failures[h.ID] {
			candidate := inspectImport(st, inv, h, o, c, snapshots[h.ID])
			item.ExistingResources = candidate.Existing
			item.ExistingRoute = candidate.Route
			item.ImportIssues = candidate.Issues
			item.ImportAvailable = candidate.eligible()
			if len(candidate.Existing) > 0 {
				item.State = "conflict"
				item.Issues = append(item.Issues, candidate.Issues...)
			}
		}
		if inv.Complete && !failures[h.ID] && snapshots[h.ID].Ready {
			_, _, _, _, err := reservationTarget(st, h, o, c.VMID, inv, snapshots[h.ID])
			item.CanAddReservation = err == nil
			if err != nil {
				item.ReservationBlocked = err.Error()
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
		if !failures[own.HostID] {
			for _, ref := range own.Resources {
				r, exists := snapshots[own.HostID].Find(ref.ID, ref.Kind)
				if exists {
					item.ManagedResources = append(item.ManagedResources, existingResource(r, "owned cleanup candidate"))
				} else {
					item.ManagedResources = append(item.ManagedResources, ExistingResource{ID: ref.ID, Kind: ref.Kind, Name: "Already absent; forget selected ownership only"})
				}
			}
			item.Drift = ownedDrift(own, snapshots[own.HostID])
		}
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
		} else if !snapshots[own.HostID].Ready {
			item.Issues = append(item.Issues, "provider prerequisites unavailable")
		} else if e = cleanupDeletionReferences(own, snapshots[own.HostID]); e != nil {
			item.State = "conflict"
			item.Issues = append(item.Issues, "unrelated configuration references owned resources")
		} else if e = cleanupTargetConflicts(inv, own, snapshots[own.HostID]); e != nil {
			item.State = "conflict"
			item.Issues = append(item.Issues, e.Error())
		} else {
			item.State = "ownedOrphan"
			item.Issues = append(item.Issues, "fully observed inventory has no matching VMID; cleanup requires explicit plan")
		}
		if inv.Complete && !failures[own.HostID] && snapshots[own.HostID].Ready && cleanupSafe(inv, own) == nil {
			_, err := existingOwnedResources(own, snapshots[own.HostID])
			item.CleanupSelectable = err == nil
		}
		out.Items = append(out.Items, item)
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].Key < out.Items[j].Key })
	return out
}
