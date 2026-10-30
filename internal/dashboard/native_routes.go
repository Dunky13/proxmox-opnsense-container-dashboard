package dashboard

import "strings"

func routeKind(own Ownership) string {
	if own.RouteLayout != nil {
		return own.RouteLayout.Kind
	}
	return "domain"
}
func routeResourceOrder() []string {
	return []string{"reservation", "acl", "domain", "subdomain", "handle"}
}
func cleanupResourceOrder() []string {
	return []string{"handle", "subdomain", "domain", "acl", "reservation"}
}

func refreshRouteLayout(st State, o OPNsense, own Ownership, snap ProviderSnapshot) (*RouteLayout, error) {
	if own.RouteLayout == nil {
		return nil, nil
	}
	layout := clone(own.RouteLayout)
	if layout.Kind != "domain" && layout.Kind != "subdomain" {
		return nil, conflict("unsupported recorded route layout")
	}
	if err := checkedSnapshot(snap); err != nil {
		return nil, err
	}
	if layout.Kind == "domain" {
		if layout.Parent != nil {
			return nil, conflict("standalone route cannot own or reference a shared parent")
		}
		return layout, nil
	}
	if layout.Parent == nil {
		return nil, conflict("native child route parent binding is missing")
	}
	parent, err := parentContext(st, o, snap, layout.Parent.ID)
	if err != nil {
		return nil, err
	}
	layout.Parent = &OwnedResource{ID: parent.ID, Kind: "domain", Fingerprint: parent.Fingerprint()}
	return layout, nil
}
func verifyRouteLayout(st State, o OPNsense, own Ownership, snap ProviderSnapshot) error {
	layout, err := refreshRouteLayout(st, o, own, snap)
	if err != nil {
		return err
	}
	if fingerprint(layout) != fingerprint(own.RouteLayout) {
		return conflict("external parent context changed since review; create a fresh plan")
	}
	return nil
}

// Imported resources keep their complete acknowledged fields. Updates patch
// only this app's managed network/route fields, preserving advanced settings.
// A proven-absent record is recreated from its durable template, including its
// native relations; a missing child never becomes a top-level domain.
func ownedRouteSpecs(o OPNsense, h Host, c Container, own Ownership, snap ProviderSnapshot, public bool, aclID, siteID string) (map[string]map[string]string, error) {
	specs, err := routeSpecs(o, h, c, public, aclID, siteID)
	if err != nil || own.RouteLayout == nil {
		return specs, err
	}
	kind := routeKind(own)
	if kind != "domain" && kind != "subdomain" {
		return nil, conflict("unsupported recorded route layout")
	}
	delete(specs["reservation"], "description")
	delete(specs["handle"], "description")
	delete(specs["handle"], "HandleType")
	delete(specs["handle"], "HandleDirective")
	delete(specs["handle"], "HttpTlsInsecureSkipVerify")
	specs["handle"]["subdomain"] = ""
	site := specs["domain"]
	delete(site, "description")
	delete(site, "DisableTls")
	delete(site, "CustomCertificate")
	if kind == "subdomain" {
		if own.RouteLayout.Parent == nil {
			return nil, conflict("native child route parent binding is missing")
		}
		parentID := own.RouteLayout.Parent.ID
		site["reverse"] = parentID
		specs["handle"]["reverse"] = parentID
		specs["handle"]["subdomain"] = siteID
		delete(specs, "domain")
		specs["subdomain"] = site
	}
	for resourceKind, fields := range specs {
		ref, _ := ownedByKind(own, resourceKind)
		if _, exists := snap.Find(ref.ID, resourceKind); exists {
			continue
		}
		if resourceKind == "acl" {
			continue
		}
		var template *Resource
		for i := range own.ResourceTemplates {
			if own.ResourceTemplates[i].Kind == resourceKind {
				template = &own.ResourceTemplates[i]
				break
			}
		}
		if template == nil {
			return nil, conflict("missing imported " + resourceKind + " has no durable repair template; inspect provider configuration")
		}
		restored := clone(template.Fields)
		for field := range restored {
			// The provider write DTO is flat. Do not flatten complex nested reservation
			// options into a different invented model during missing-resource repair.
			if strings.Contains(field, ".") {
				return nil, conflict("missing imported " + resourceKind + " has nested advanced settings; automatic recreation is unsupported")
			}
		}
		for field, value := range fields {
			restored[field] = value
		}
		specs[resourceKind] = restored
	}
	return specs, nil
}
