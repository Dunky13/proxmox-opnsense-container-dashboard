package dashboard

import (
	"context"
	"strings"
)

// Reservation authority is independent of any app domain sharing its address.
func reservationConflicts(snap ProviderSnapshot, own Ownership, desired map[string]string) error {
	for _, row := range snap.Resources {
		if row.Kind != "reservation" {
			continue
		}
		ref, owned := ownedByKind(own, "reservation")
		if owned && ref.ID == row.ID {
			continue
		}
		if row.Fields["ip_address"] == desired["ip"] || strings.EqualFold(row.Fields["hw_address"], desired["mac"]) {
			return conflict("another reservation already uses the intended IP or MAC")
		}
	}
	return nil
}

func (s *Service) cleanupForeignContext(ctx context.Context, cfg Config, router OPNsense, selection Ownership, snap ProviderSnapshot) (string, error) {
	ips := []string{}
	for _, ref := range selection.Resources {
		if ref.Kind != "reservation" {
			continue
		}
		r, exists := snap.Find(ref.ID, ref.Kind)
		if !exists {
			for _, template := range selection.ResourceTemplates {
				if template.ID == ref.ID && template.Kind == ref.Kind && template.Fingerprint() == ref.Fingerprint {
					r, exists = template, true
				}
			}
		}
		if !exists {
			continue
		} // No remaining row or verified deletion evidence to apply.
		if r.Fields["ip_address"] == "" {
			return "", conflict("reservation address unavailable for cross-firewall cleanup verification")
		}
		ips = append(ips, r.Fields["ip_address"])
	}
	return s.reservationForeignContext(ctx, cfg, router, ips)
}

func (w *workflow) cleanupGuard(inv Inventory, snap ProviderSnapshot, selection Ownership) error {
	if !snap.Ready {
		return conflict("provider prerequisites unavailable before cleanup")
	}
	if w.external != nil {
		return w.externalGuard(inv, snap)
	}
	hash, err := w.s.cleanupForeignContext(w.ctx, w.s.Store.Read().Config, w.router, selection, snap)
	if err != nil {
		return err
	}
	if w.op.CleanupForeignContextHash != "" && hash != w.op.CleanupForeignContextHash {
		return conflict("other configured firewall context changed during cleanup")
	}
	return nil
}
