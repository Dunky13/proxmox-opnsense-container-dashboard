package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

type endpoint struct{ Controller, Command, Wrapper string }

func resourceEndpoint(kind string) (endpoint, error) {
	switch kind {
	case "reservation":
		return endpoint{"kea/dhcpv4", "reservation", "reservation"}, nil
	case "subnet":
		return endpoint{"kea/dhcpv4", "subnet", "subnet4"}, nil
	case "acl":
		return endpoint{"caddy/reverse_proxy", "access_list", "accesslist"}, nil
	case "domain":
		return endpoint{"caddy/reverse_proxy", "reverse_proxy", "reverse"}, nil
	case "handle":
		return endpoint{"caddy/reverse_proxy", "handle", "handle"}, nil
	case "subdomain":
		return endpoint{"caddy/reverse_proxy", "subdomain", "subdomain"}, nil
	}
	return endpoint{}, bad("unsupported resource kind")
}
func opnJSON(ctx context.Context, o OPNsense, method, path string, body, out any) error {
	if o.APIKey == "" || o.APISecret == "" {
		return unavailable("OPNsense API credentials are not configured")
	}
	e := providerJSON(ctx, o.BaseURL, o.CAPEM, method, "/api/"+path, body, func(r *http.Request) { r.SetBasicAuth(o.APIKey, o.APISecret) }, out)
	if e != nil {
		if strings.HasPrefix(path, "caddy/") {
			return unavailable("os-caddy API: " + e.Error() + "; verify plugin installation and API access")
		}
		return unavailable("KEA API: " + e.Error() + "; verify DHCPv4 configuration and API access")
	}
	return nil
}

// getBase exposes option/relation fields as dictionaries of {value, selected}.
// Normalize selected keys instead of comparing translated search labels.
func normalizeValue(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case json.Number:
		return x.String(), nil
	case float64:
		return fmt.Sprint(x), nil
	case bool:
		if x {
			return "1", nil
		}
		return "0", nil
	case nil:
		return "", nil
	case []any:
		// PHP serializes consecutive numeric option keys as a JSON array. Keep
		// those keys as their indexes; translated labels are never stored values.
		options := make(map[string]any, len(x))
		for index, entry := range x {
			option, ok := entry.(map[string]any)
			if !ok {
				return "", unavailable("unsupported provider field shape")
			}
			switch option["selected"] {
			case true, false, float64(0), float64(1), "0", "1", "true", "false":
			default:
				return "", unavailable("unsupported provider option selection")
			}
			options[strconv.Itoa(index)] = option
		}
		return normalizeValue(options)
	case map[string]any:
		keys := []string{}
		for k, entry := range x {
			m, ok := entry.(map[string]any)
			if !ok {
				return "", unavailable("unsupported provider field shape")
			}
			selected := m["selected"] == true || m["selected"] == float64(1) || m["selected"] == "1" || m["selected"] == "true"
			if selected {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		return strings.Join(keys, ","), nil
	}
	return "", unavailable("unsupported provider field shape")
}
func flattenFields(prefix string, m map[string]any, out map[string]string) error {
	for k, v := range m {
		if nested, ok := v.(map[string]any); ok {
			selection := len(nested) == 0
			for _, entry := range nested {
				if option, ok := entry.(map[string]any); ok {
					if _, ok := option["selected"]; ok {
						selection = true
						break
					}
				}
			}
			if !selection {
				if e := flattenFields(prefix+k+".", nested, out); e != nil {
					return e
				}
				continue
			}
		}
		s, e := normalizeValue(v)
		if e != nil {
			return e
		}
		out[prefix+k] = s
	}
	return nil
}
func (p *RealProviders) getResource(ctx context.Context, o OPNsense, kind, id string) (Resource, error) {
	ep, e := resourceEndpoint(kind)
	if e != nil {
		return Resource{}, e
	}
	if !uuidPattern.MatchString(id) {
		return Resource{}, unavailable("provider returned invalid resource UUID")
	}
	var env map[string]map[string]any
	if e = opnJSON(ctx, o, "GET", ep.Controller+"/get_"+ep.Command+"/"+id, nil, &env); e != nil {
		return Resource{}, e
	}
	fields, ok := env[ep.Wrapper]
	if !ok || len(fields) == 0 {
		return Resource{}, unavailable("provider resource disappeared or is malformed")
	}
	r := Resource{ID: id, Kind: kind, Fields: map[string]string{}}
	if e = flattenFields("", fields, r.Fields); e != nil {
		return Resource{}, e
	}
	if value, ok := r.Fields["clientIps"]; ok {
		r.Fields["clientIps"] = canonicalCIDRs(strings.Split(value, ","))
	}
	return r, nil
}
func (p *RealProviders) listResources(ctx context.Context, o OPNsense, kind string) ([]Resource, error) {
	ctx, closeHTTP := withProviderHTTP(ctx)
	defer closeHTTP()
	ep, _ := resourceEndpoint(kind)
	var grid struct {
		Total *int `json:"total"`
		Rows  []struct {
			UUID string `json:"uuid"`
		} `json:"rows"`
	}
	// Query parameters work on the documented GET/POST search endpoints and avoid
	// assumptions about how a plugin binds JSON pagination to its grid request.
	if e := opnJSON(ctx, o, "GET", ep.Controller+"/search_"+ep.Command+"?current=1&rowCount=-1", nil, &grid); e != nil {
		return nil, e
	}
	if grid.Total == nil || grid.Rows == nil || *grid.Total != len(grid.Rows) || len(grid.Rows) > 10000 {
		return nil, unavailable("provider resource inventory is incomplete")
	}
	seen := map[string]bool{}
	for _, row := range grid.Rows {
		if !uuidPattern.MatchString(row.UUID) || seen[row.UUID] {
			return nil, unavailable("invalid or duplicate provider resource identity")
		}
		seen[row.UUID] = true
	}
	return readProviderRows(ctx, len(grid.Rows), func(ctx context.Context, index int) (Resource, error) {
		return p.getResource(ctx, o, kind, grid.Rows[index].UUID)
	})
}
func (p *RealProviders) Snapshot(ctx context.Context, o OPNsense) (ProviderSnapshot, error) {
	ctx, closeHTTP := withProviderHTTP(ctx)
	defer closeHTTP()
	snap := ProviderSnapshot{Resources: []Resource{}, Issues: []string{}}
	for _, kind := range []string{"reservation", "acl", "domain", "handle", "subdomain", "subnet"} {
		rows, e := p.listResources(ctx, o, kind)
		if e != nil {
			return snap, e
		}
		snap.Resources = append(snap.Resources, rows...)
	}
	general, clientIP, err := p.caddyGeneral(ctx, o, snap.Resources)
	if err != nil {
		return snap, err
	}
	if general.Fields["enabled"] != "1" {
		snap.Issues = append(snap.Issues, "Caddy must already be enabled in global settings")
	}
	snap.ClientIPContext = clientIP
	snap.Resources = append(snap.Resources, general)

	var kea struct {
		DHCPv4 struct {
			General map[string]any `json:"general"`
		} `json:"dhcpv4"`
	}
	if e := opnJSON(ctx, o, "GET", "kea/dhcpv4/get", nil, &kea); e != nil {
		return snap, e
	}
	keaEnabled, e := normalizeValue(kea.DHCPv4.General["enabled"])
	if e != nil || keaEnabled != "1" {
		snap.Issues = append(snap.Issues, "KEA DHCPv4 must already be enabled")
	}

	// Legacy informational field only. Resolution always uses the complete
	// discovered topology and each target's intended IPv4, never this hint.
	if subnet, ok := snap.Find(o.SubnetID, "subnet"); ok {
		snap.Subnet = subnet.Fields["subnet"]
	}
	for _, service := range []string{"kea", "caddy"} {
		var status struct {
			Status string `json:"status"`
		}
		if e := opnJSON(ctx, o, "GET", service+"/service/status", nil, &status); e != nil {
			return snap, e
		}
		if status.Status != "running" {
			snap.Issues = append(snap.Issues, service+" service must already be running")
		}
	}
	if o.CertificateID != "" {
		// Verify that the certificate is a currently offered model value. Never install
		// certificates or change global DNS/TLS settings on the operator's behalf.
		var domain struct {
			Reverse map[string]any `json:"reverse"`
		}
		if e := opnJSON(ctx, o, "GET", "caddy/reverse_proxy/get_reverse_proxy", nil, &domain); e != nil {
			return snap, e
		}
		offered := false
		if options, ok := domain.Reverse["CustomCertificate"].(map[string]any); ok {
			_, offered = options[o.CertificateID]
		}
		if o.CertificateID != "" && !offered {
			snap.Issues = append(snap.Issues, "selected certificate is not available to os-caddy")
		}
	}
	snap.Ready = len(snap.Issues) == 0
	sort.Slice(snap.Resources, func(a, b int) bool {
		a1, b1 := snap.Resources[a], snap.Resources[b]
		return a1.Kind+a1.ID < b1.Kind+b1.ID
	})
	return snap, nil
}
func (p *RealProviders) Create(ctx context.Context, o OPNsense, kind string, fields map[string]string) (Resource, error) {
	ctx, closeHTTP := withProviderHTTP(ctx)
	defer closeHTTP()
	if kind == "subnet" {
		return Resource{}, bad("subnet writes are not supported")
	}
	ep, e := resourceEndpoint(kind)
	if e != nil {
		return Resource{}, e
	}
	var out struct {
		Result string `json:"result"`
		UUID   string `json:"uuid"`
	}
	if e = opnJSON(ctx, o, "POST", ep.Controller+"/add_"+ep.Command, map[string]any{ep.Wrapper: fields}, &out); e != nil {
		return Resource{}, e
	}
	if out.Result != "saved" || !uuidPattern.MatchString(out.UUID) {
		return Resource{}, unavailable("provider did not confirm resource creation")
	}
	r, err := p.getResource(ctx, o, kind, out.UUID)
	if err != nil {
		return Resource{ID: out.UUID, Kind: kind}, err
	}
	return r, nil
}
func (p *RealProviders) Set(ctx context.Context, o OPNsense, old Resource, fields map[string]string) (Resource, error) {
	ctx, closeHTTP := withProviderHTTP(ctx)
	defer closeHTTP()
	if old.Kind == "subnet" {
		return Resource{}, bad("subnet writes are not supported")
	}
	ep, e := resourceEndpoint(old.Kind)
	if e != nil {
		return Resource{}, e
	}
	fresh, e := p.getResource(ctx, o, old.Kind, old.ID)
	if e != nil {
		return Resource{}, e
	}
	if fresh.Fingerprint() != old.Fingerprint() {
		return Resource{}, conflict("provider resource changed before update")
	}
	var out struct {
		Result string `json:"result"`
	}
	if e = opnJSON(ctx, o, "POST", ep.Controller+"/set_"+ep.Command+"/"+old.ID, map[string]any{ep.Wrapper: fields}, &out); e != nil {
		return Resource{}, e
	}
	if out.Result != "saved" {
		return Resource{}, unavailable("provider did not confirm resource update")
	}
	r, err := p.getResource(ctx, o, old.Kind, old.ID)
	if err != nil {
		return Resource{ID: old.ID, Kind: old.Kind}, err
	}
	return r, nil
}
func (p *RealProviders) Delete(ctx context.Context, o OPNsense, old Resource) error {
	ctx, closeHTTP := withProviderHTTP(ctx)
	defer closeHTTP()
	if old.Kind == "subnet" {
		return bad("subnet writes are not supported")
	}
	ep, e := resourceEndpoint(old.Kind)
	if e != nil {
		return e
	}
	fresh, e := p.getResource(ctx, o, old.Kind, old.ID)
	if e != nil {
		return e
	}
	if fresh.Fingerprint() != old.Fingerprint() {
		return conflict("provider resource changed before deletion")
	}
	var out struct {
		Result string `json:"result"`
	}
	if e = opnJSON(ctx, o, "POST", ep.Controller+"/del_"+ep.Command+"/"+old.ID, map[string]any{}, &out); e != nil {
		return e
	}
	if out.Result != "deleted" {
		return unavailable("provider did not confirm resource deletion")
	}
	return nil
}
func (p *RealProviders) Apply(ctx context.Context, o OPNsense, service string) error {
	if service != "kea" && service != "caddy" {
		return bad("unsupported service")
	}
	var out struct {
		Status string `json:"status"`
	}
	if e := opnJSON(ctx, o, "POST", service+"/service/reconfigure", map[string]any{}, &out); e != nil {
		return e
	}
	if out.Status != "ok" {
		return unavailable(service + " reconfigure failed; operator recovery required")
	}
	return nil
}
