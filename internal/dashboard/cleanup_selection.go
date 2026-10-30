package dashboard

import "strings"

func cleanupReservationReferences(own Ownership, snap ProviderSnapshot, includeAbsent bool) error {
	selected := map[string]bool{}
	for _, ref := range own.Resources {
		selected[ref.ID] = true
	}
	for _, ref := range own.Resources {
		if ref.Kind != "reservation" {
			continue
		}
		reservation, exists := snap.Find(ref.ID, ref.Kind)
		if !exists && includeAbsent {
			for _, template := range own.ResourceTemplates {
				if template.ID == ref.ID && template.Kind == ref.Kind && template.Fingerprint() == ref.Fingerprint {
					reservation = template
					exists = true
				}
			}
		}
		if !exists || reservation.Fields["ip_address"] == "" {
			continue
		}
		for _, r := range snap.Resources {
			if r.Kind == "handle" && !selected[r.ID] && strings.Contains(r.Fields["ToDomain"], reservation.Fields["ip_address"]) {
				return conflict("proxy handle " + r.ID + " still uses the selected reservation address; include its owned proxy resources or review the foreign dependency")
			}
		}
	}
	return nil
}

func verifyCleanedTargets(remaining Ownership, targets []OwnedResource, snap ProviderSnapshot) error {
	for _, target := range targets {
		retained := false
		for _, ref := range remaining.Resources {
			if ref.ID == target.ID && ref.Kind == target.Kind {
				retained = true
			}
		}
		if !retained {
			if _, exists := snap.Find(target.ID, target.Kind); exists {
				return conflict("a deleted cleanup UUID reappeared; inspect provider state before application")
			}
		}
	}
	return nil
}

func cleanupTargetConflicts(inv Inventory, own Ownership, snap ProviderSnapshot) error {
	for _, ref := range own.Resources {
		r, exists := snap.Find(ref.ID, ref.Kind)
		if !exists {
			for _, template := range own.ResourceTemplates {
				if template.ID == ref.ID && template.Kind == ref.Kind {
					r = template
				}
			}
		}
		for _, c := range inv.Containers {
			if (c.IP != "" && (c.IP == r.Fields["ip_address"] || c.IP == r.Fields["ToDomain"])) || (c.MAC != "" && strings.EqualFold(c.MAC, r.Fields["hw_address"])) {
				return conflict("another observed container uses a selected resource address; cleanup is protected")
			}
		}
	}
	return nil
}

// A selection never grants ownership. Dependency checks treat retained owned
// resources just like foreign consumers, so a live proxy cannot lose its parent.
func selectedCleanup(own Ownership, ids []string) (Ownership, error) {
	if len(ids) == 0 {
		return own, nil
	}
	selected := clone(own)
	selected.Resources = []OwnedResource{}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return own, bad("duplicate selected resource")
		}
		seen[id] = true
		found := false
		for _, ref := range own.Resources {
			if ref.ID == id {
				selected.Resources = append(selected.Resources, ref)
				found = true
			}
		}
		if !found {
			return own, conflict("selected UUID is not owned by this app")
		}
	}
	return selected, nil
}
