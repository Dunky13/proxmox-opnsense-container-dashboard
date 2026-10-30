package dashboard

import (
	"sort"
	"strconv"
	"strings"
)

func validRoute(r RouteConfig) bool {
	return (r.Scheme == "http" || r.Scheme == "https") && r.Port >= 1 && r.Port <= 65535
}
func observedOwnedRoute(own Ownership, snap ProviderSnapshot) (RouteConfig, error) {
	if validRoute(own.DesiredRoute) {
		return own.DesiredRoute, nil
	}
	ref, ok := ownedByKind(own, "handle")
	r, exists := snap.Find(ref.ID, "handle")
	if !ok || !exists || r.Fingerprint() != ref.Fingerprint {
		return RouteConfig{}, conflict("existing app upstream is unavailable; review explicit app scheme and port before repair")
	}
	port, err := strconv.Atoi(r.Fields["ToPort"])
	scheme := "http"
	if r.Fields["HttpTls"] == "1" {
		scheme = "https"
	} else if r.Fields["HttpTls"] != "0" {
		return RouteConfig{}, conflict("existing app upstream scheme is unsupported")
	}
	route := RouteConfig{Scheme: scheme, Port: port, Public: own.Public}
	if err != nil || !validRoute(route) {
		return RouteConfig{}, conflict("existing app upstream port is unsupported")
	}
	return route, nil
}
func resolveRoute(req PlanRequest, own Ownership, owned bool) (RouteConfig, error) {
	r := RouteConfig{Scheme: "http", Port: 80, Public: req.Public}
	if owned {
		r = own.DesiredRoute
		r.Public = req.Public
	}
	if req.Action == "cleanup" {
		if req.UpstreamScheme != nil || req.UpstreamPort != nil {
			return r, bad("cleanup does not accept route overrides")
		}
		// Cleanup never configures an upstream. Keep a valid response shape for
		// legacy records whose deleted handler cannot supply an app route.
		r = own.DesiredRoute
		if !validRoute(r) {
			r = RouteConfig{Scheme: "http", Port: 80, Public: own.Public}
		}
		return r, nil
	}
	if req.UpstreamScheme != nil {
		r.Scheme = *req.UpstreamScheme
	}
	if req.UpstreamPort != nil {
		r.Port = *req.UpstreamPort
	}
	if !validRoute(r) {
		return r, bad("route scheme must be http/https and port must be 1..65535")
	}
	return r, nil
}
func decorateInventory(inv Inventory, st State) Inventory {
	for i, c := range inv.Containers {
		if host, exists := st.Config.Host(c.HostID); exists {
			mac, ip, err := DeriveAddress(host, c.VMID)
			if err == nil {
				inv.Containers[i].ExpectedIP, inv.Containers[i].ExpectedMAC = ip, mac
			}
		}
		if own, ok := st.Owned[c.Key]; ok && validRoute(own.DesiredRoute) {
			r := own.DesiredRoute
			inv.Containers[i].DesiredRoute = &r
		}
	}
	return inv
}
func ownedByKind(own Ownership, kind string) (OwnedResource, bool) {
	for _, r := range own.Resources {
		if r.Kind == kind {
			return r, true
		}
	}
	return OwnedResource{}, false
}
func routeSpecs(o OPNsense, h Host, c Container, public bool, aclID, domainID string) (map[string]map[string]string, error) {
	d, e := desiredFields(o, c, h, public)
	if e != nil {
		return nil, e
	}
	description := "container-dashboard " + c.Key
	tls := "0"
	if o.UpstreamScheme == "https" {
		tls = "1"
	}
	if public {
		aclID = ""
	} else if o.PrivateAccessListID != "" {
		aclID = o.PrivateAccessListID
	}
	specs := map[string]map[string]string{
		"reservation": {"subnet": o.SubnetID, "hw_address": d["mac"], "ip_address": d["ip"], "hostname": d["name"], "description": description},
		"domain":      {"enabled": "1", "FromDomain": d["domain"], "DisableTls": "0", "accesslist": aclID, "description": description},
		"handle":      {"enabled": "1", "reverse": domainID, "HandleType": "handle", "HandleDirective": "reverse_proxy", "ToDomain": d["ip"], "ToPort": itoa(o.UpstreamPort), "HttpTls": tls, "HttpTlsInsecureSkipVerify": "0", "description": description},
	}
	if o.CertificateID != "" {
		specs["domain"]["CustomCertificate"] = o.CertificateID
	}
	if !public && o.PrivateAccessListID == "" {
		specs["acl"] = map[string]string{"accesslistName": "dashboard_" + h.ID + "_" + itoa(c.VMID), "clientIps": canonicalCIDRs(o.AllowlistCIDRs), "accesslistInvert": "0", "RequestMatcher": "remote_ip", "HttpResponseCode": "403", "description": description}
	}
	return specs, nil
}
func desiredResourcesMatch(o OPNsense, h Host, c Container, own Ownership, snap ProviderSnapshot, public bool) bool {
	if verifySharedAccessList(o, snap, own.SharedAccessList, public) != nil {
		return false
	}
	resolved, err := resolveSubnet(snap, h, c)
	if err != nil {
		return false
	}
	o.SubnetID = resolved.ID
	acl, _ := ownedByKind(own, "acl")
	domain, ok := ownedByKind(own, "domain")
	if !ok {
		return false
	}
	specs, e := routeSpecs(o, h, c, public, acl.ID, domain.ID)
	if e != nil {
		return false
	}
	for kind, fields := range specs {
		ref, ok := ownedByKind(own, kind)
		if !ok {
			return false
		}
		r, ok := snap.Find(ref.ID, kind)
		if !ok {
			return false
		}
		for k, v := range fields {
			if r.Fields[k] != v {
				return false
			}
		}
	}
	return true
}
func effectField(s string) string {
	names := map[string]string{"hw_address": "macAddress", "ip_address": "ipAddress", "subnet": "subnetId", "FromDomain": "domain", "DisableTls": "disableTls", "CustomCertificate": "certificateId", "accesslist": "accessListId", "accesslistName": "accessListName", "clientIps": "clientCidrs", "accesslistInvert": "inverted", "RequestMatcher": "requestMatcher", "HttpResponseCode": "denyStatus", "ToDomain": "upstreamAddress", "ToPort": "upstreamPort", "HttpTls": "upstreamScheme", "HttpTlsInsecureSkipVerify": "insecureSkipVerify", "reverse": "domainId", "HandleType": "handleType", "HandleDirective": "handleDirective"}
	if n, ok := names[s]; ok {
		return n
	}
	if s == "" {
		return ""
	}
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '_' || r == '.' || r == '-' })
	if len(parts) == 0 {
		return "field"
	}
	out := strings.ToLower(parts[0][:1]) + parts[0][1:]
	for _, p := range parts[1:] {
		if p != "" {
			out += strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return out
}
func effectValue(field, value string) string {
	if field == "HttpTls" {
		if value == "0" {
			return "http"
		}
		if value == "1" {
			return "https"
		}
	}
	return value
}
func planEffects(action string, h Host, o OPNsense, inv Inventory, own Ownership, snap ProviderSnapshot, route RouteConfig, targetKey string) ([]PlanEffect, []string, error) {
	effects := []PlanEffect{}
	notes := []string{"Changes apply only to the recorded dashboard-owned resource UUIDs.", "Service reconfiguration is required; upstream KEA application may briefly interrupt DHCP."}
	if action == "cleanup" {
		for _, kind := range []string{"handle", "domain", "acl", "reservation"} {
			for _, ref := range own.Resources {
				if ref.Kind != kind {
					continue
				}
				r, exists := snap.Find(ref.ID, kind)
				effect := PlanEffect{Kind: kind, Action: "delete", ResourceID: ref.ID, Name: "Delete owned " + kind, Changes: []FieldChange{}}
				if !exists {
					effect.Action = "retain"
					effect.Name = "Owned " + kind + " is already absent; forget its ownership record"
				}
				keys := []string{}
				for k := range r.Fields {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					effect.Changes = append(effect.Changes, FieldChange{effectField(k), effectValue(k, r.Fields[k]), ""})
				}
				effects = append(effects, effect)
			}
		}
		return effects, notes, nil
	}
	c, ok := inv.Find(targetKey)
	if !ok {
		return nil, nil, conflict("plan target container is missing")
	}
	o.UpstreamScheme = route.Scheme
	o.UpstreamPort = route.Port
	subnet, err := resolveSubnet(snap, h, c)
	if err != nil {
		return nil, nil, err
	}
	o.SubnetID = subnet.ID
	acl, aclExists := ownedByKind(own, "acl")
	domain, domainExists := ownedByKind(own, "domain")
	if aclExists {
		_, aclExists = snap.Find(acl.ID, "acl")
	}
	if domainExists {
		_, domainExists = snap.Find(domain.ID, "domain")
	}
	aclID, domainID := acl.ID, domain.ID
	if !aclExists {
		aclID = "new:acl"
	}
	if !domainExists {
		domainID = "new:domain"
	}
	specs, e := routeSpecs(o, h, c, route.Public, aclID, domainID)
	if e != nil {
		return nil, nil, e
	}
	changed := false
	ledgerNotes := []string{}
	for _, kind := range []string{"reservation", "acl", "domain", "handle"} {
		fields, needed := specs[kind]
		if !needed {
			if ref, ok := ownedByKind(own, kind); ok {
				effect := PlanEffect{Kind: kind, Action: "retain", ResourceID: ref.ID, Name: "Retain detached owned ACL for future private use", Changes: []FieldChange{}}
				if _, exists := snap.Find(ref.ID, kind); !exists {
					effect.Action = "update"
					effect.Name = "Forget already absent detached ACL ownership"
					effect.Changes = append(effect.Changes, FieldChange{"resourceId", ref.ID, ""})
					ledgerNotes = append(ledgerNotes, "The detached ACL is already absent. Only its ownership record is removed; no provider deletion is sent.")
				}
				effects = append(effects, effect)
			}
			continue
		}
		ref, recorded := ownedByKind(own, kind)
		r, exists := snap.Find(ref.ID, kind)
		before := map[string]string{}
		if exists {
			before = r.Fields
		}
		effect := PlanEffect{Kind: kind, Action: "retain", ResourceID: ref.ID, Name: "Owned " + kind, Changes: []FieldChange{}}
		if !exists {
			effect.Action = "create"
			effect.Name = "Create " + kind
			effect.ResourceID = ""
			if recorded {
				effect.Name = "Recreate missing owned " + kind
				effect.Changes = append(effect.Changes, FieldChange{"resourceId", ref.ID, "new:" + kind})
			}
		}
		keys := []string{}
		for k := range fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if before[k] != fields[k] {
				effect.Changes = append(effect.Changes, FieldChange{effectField(k), effectValue(k, before[k]), effectValue(k, fields[k])})
			}
		}
		if exists && len(effect.Changes) > 0 {
			effect.Action = "update"
		}
		if effect.Action != "retain" {
			changed = true
		}
		effects = append(effects, effect)
	}
	if changed {
		notes = append(notes, "The domain is staged disabled, its ACL is verified, then the domain is enabled before service application.")
		for i := range effects {
			if effects[i].Kind == "domain" && effects[i].Action == "retain" {
				effects[i].Action = "update"
				effects[i].Name = "Stage and verify owned domain before enabling"
			}
		}
	} else {
		notes = []string{"Recorded resources already match; no provider write or service application is required."}
	}
	if !route.Public && o.PrivateAccessListID != "" {
		shared, err := selectedAccessList(o, snap)
		if err != nil {
			return nil, nil, err
		}
		a := describePolicyAccessList(shared, snap)
		effects = append(effects, PlanEffect{Kind: "acl", Action: "retain", ResourceID: a.ID, Name: "Reuse shared access list: " + a.Name + " (unchanged, excluded from cleanup)", Changes: []FieldChange{{"clientCidrs", strings.Join(a.CIDRs, ","), strings.Join(a.CIDRs, ",")}, {"requestMatcher", a.Matcher, a.Matcher}}})
		if a.ClientIPContext != nil {
			notes = append(notes, a.ClientIPContext.Summary, "The reviewed client IP trust context is rechecked before every mutation and service application, including recovery. Global trust lists and headers remain external and unchanged.")
		}
		notes = append(notes, "The shared access list remains externally managed and unchanged. It is excluded from dashboard ownership and cleanup.")
	}
	notes = append(notes, "KEA subnet resolved from the intended IPv4: "+subnet.CIDR+" ("+subnet.ID+"). The existing subnet remains unchanged.")
	return effects, append(notes, ledgerNotes...), nil
}
