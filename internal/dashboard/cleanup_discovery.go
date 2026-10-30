package dashboard

import "sort"

type ProtectedCandidate struct {
	OPNsenseID   string           `json:"opnsenseId"`
	Resource     ExistingResource `json:"resource"`
	Observations []FieldChange    `json:"observations"`
	Reason       string           `json:"reason"`
	Eligible     bool             `json:"eligible"`
	HostID       string           `json:"hostId,omitempty"`
	VMID         int              `json:"vmid,omitempty"`
	Fingerprint  string           `json:"fingerprint"`
}

// Discovery grants no authority. Planning independently verifies exact selected
// fingerprints, complete occupancy, profile mapping and all retained consumers.
func protectedCandidates(st State, inv Inventory, routers routerObservations) []ProtectedCandidate {
	out := []ProtectedCandidate{}
	seen := map[string]bool{}
	if externalInventory(st.Config, inv) != nil {
		return out
	}
	for _, h := range st.Config.Hosts {
		o := routerForHost(st.Config, h)
		if seen[o.ID] || routers.Failures[h.ID] {
			continue
		}
		seen[o.ID] = true
		snap := routers.Snapshots[h.ID]
		if !snap.Ready || checkedSnapshot(snap) != nil {
			continue
		}
		for _, r := range snap.Resources {
			if resourceOwned(st, o.ID, r) {
				continue
			}
			if r.Kind != "reservation" && r.Kind != "domain" && r.Kind != "subdomain" && r.Kind != "handle" {
				continue
			}
			a, _, err := externalResource(st, inv, o, snap, r)
			// Present guests are handled by their configuration inspector, not orphan
			// cleanup. Keep unknown/unconfigured scopes visible with a precise blocker.
			if a.HostID != "" && externalAbsent(st, inv, a) != nil {
				continue
			}
			c := ProtectedCandidate{OPNsenseID: o.ID, Resource: existingResource(r, "unowned record"), Observations: resourceObservations(r), HostID: a.HostID, VMID: a.VMID, Fingerprint: r.Fingerprint()}
			if err != nil {
				c.Reason = "Protected: " + err.Error()
			} else if routerBlocked(st, o.ID) {
				c.Reason = "Protected: inspect the incomplete operation or update job in Activity before a new cleanup."
			} else {
				c.Eligible = true
				c.Reason = "Eligible for explicit reviewed deletion after administrator acknowledgement that these exact UUIDs are unused by external or unmanaged clients. Select an app domain/child with all related handles. Retained references block reservation deletion."
			}
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].OPNsenseID+out[i].Resource.Kind+out[i].Resource.ID < out[j].OPNsenseID+out[j].Resource.Kind+out[j].Resource.ID
	})
	return out
}
