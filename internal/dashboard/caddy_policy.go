package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// Only selected header identities and types cross the provider boundary. Header
// values, replacements and unrelated definitions may contain credentials.
type ClientIPHeader struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}
type ClientIPContext struct {
	TrustedProxyListID  string           `json:"trustedProxyListId"`
	TrustedProxyCIDRs   []string         `json:"trustedProxyCidrs"`
	Headers             []ClientIPHeader `json:"headers"`
	HeaderOrder         string           `json:"headerOrder"`
	EnableLayer4        string           `json:"enableLayer4"`
	Layer4EmptyVerified bool             `json:"layer4EmptyVerified,omitempty"`
	ProxyProtocolAllow  string           `json:"proxyProtocolAllow"`
	Suitable            bool             `json:"suitable"`
	Reason              string           `json:"reason"`
	Summary             string           `json:"summary"`
}
type CaddyPolicySnapshot struct {
	Resources       []Resource
	ClientIPContext *ClientIPContext
}

func (p CaddyPolicySnapshot) snapshot() ProviderSnapshot {
	return ProviderSnapshot{Resources: p.Resources, ClientIPContext: p.ClientIPContext}
}
func unavailableClientIP(reason string) *ClientIPContext {
	return &ClientIPContext{TrustedProxyCIDRs: []string{}, Headers: []ClientIPHeader{}, Reason: reason}
}

var headerName = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

// Relation dictionaries expose a set, not the configured selection order. Never
// turn sorted UUIDs into a claim about Caddy's effective header precedence.
func policySelection(value any) (string, string, error) {
	if empty, ok := value.([]any); ok && len(empty) == 0 {
		return "", "configured", nil
	}
	if options, ok := value.(map[string]any); ok {
		for _, option := range options {
			entry, ok := option.(map[string]any)
			if !ok {
				return "", "", unavailable("malformed policy selection")
			}
			switch entry["selected"] {
			case true, false, float64(0), float64(1), "0", "1", "true", "false":
			default:
				return "", "", unavailable("incomplete policy selection")
			}
		}
	} else if _, ok := value.(string); !ok {
		return "", "", unavailable("malformed policy selection")
	}
	v, err := normalizeValue(value)
	order := "configured"
	if _, ok := value.(map[string]any); ok && strings.Contains(v, ",") {
		order = "notExposed"
	}
	return v, order, err
}
func (p *RealProviders) caddyGeneral(ctx context.Context, o OPNsense, rows []Resource) (Resource, *ClientIPContext, error) {
	var out struct {
		Caddy struct {
			General map[string]json.RawMessage `json:"general"`
		} `json:"caddy"`
	}
	if err := opnJSON(ctx, o, http.MethodGet, "caddy/general/get", nil, &out); err != nil {
		return Resource{}, unavailableClientIP("Caddy general policy could not be read."), err
	}
	general := Resource{ID: "general", Kind: "caddyGeneral", Fields: map[string]string{}}
	fields := map[string]any{}
	for _, name := range []string{"enabled", "accesslist", "ClientIpHeaders", "EnableLayer4", "ProxyProtocolAllow"} {
		raw, exists := out.Caddy.General[name]
		if !exists {
			continue
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return general, unavailableClientIP("Caddy policy metadata is malformed."), nil
		}
		fields[name] = value
		v, err := normalizeValue(value)
		if name == "accesslist" || name == "ClientIpHeaders" {
			v, _, err = policySelection(value)
		}
		if err == nil {
			general.Fields[name] = v
		}
	}
	if general.Fields["EnableLayer4"] == "1" {
		empty, err := p.emptyLayer4(ctx, o)
		fields["layer4EmptyVerified"] = err == nil && empty
	}
	context := resolveClientIPContext(general, fields, rows, func(id string) (string, error) {
		// A narrow decoder deliberately discards HeaderValue and every other field.
		var header struct {
			Header *struct {
				Type string `json:"HeaderType"`
			} `json:"header"`
		}
		if err := opnJSON(ctx, o, http.MethodGet, "caddy/reverse_proxy/get_header/"+id, nil, &header); err != nil {
			return "", err
		}
		if header.Header == nil {
			return "", unavailable("selected client IP header is missing")
		}
		return header.Header.Type, nil
	})
	return general, context, nil
}

// An enabled but unused Layer 4 feature only falls through to the HTTP app.
// Require a complete empty model inventory, and never infer emptiness on error.
func (p *RealProviders) emptyLayer4(ctx context.Context, o OPNsense) (bool, error) {
	var out struct {
		Total *int               `json:"total"`
		Rows  *[]json.RawMessage `json:"rows"`
	}
	if err := opnJSON(ctx, o, http.MethodGet, "caddy/reverse_proxy/search_layer4?current=1&rowCount=-1", nil, &out); err != nil {
		return false, err
	}
	if out.Total == nil || out.Rows == nil || *out.Total != len(*out.Rows) {
		return false, unavailable("Layer 4 inventory is incomplete")
	}
	return len(*out.Rows) == 0, nil
}
func resolveClientIPContext(general Resource, fields map[string]any, rows []Resource, getHeader func(string) (string, error)) *ClientIPContext {
	c := unavailableClientIP("")
	for _, name := range []string{"accesslist", "ClientIpHeaders"} {
		value, exists := fields[name]
		if !exists || value == nil {
			c.Reason = "Caddy client IP selections are incomplete."
			return c
		}
		if _, _, err := policySelection(value); err != nil {
			c.Reason = "Caddy client IP selections are malformed."
			return c
		}
	}
	trustedID := general.Fields["accesslist"]
	if trustedID != "" && !uuidPattern.MatchString(trustedID) {
		c.Reason = "The global trusted proxy selection is not a single valid identity."
		return c
	}
	c.TrustedProxyListID = trustedID
	if value, exists := fields["EnableLayer4"]; exists {
		switch value.(type) {
		case string, bool, float64:
		default:
			c.Reason = "Caddy Layer 4 policy metadata is malformed."
			return c
		}
		v, err := normalizeValue(value)
		if err != nil || (v != "" && v != "0" && v != "1") {
			c.Reason = "Caddy Layer 4 policy metadata is malformed."
			return c
		}
	}
	if value, exists := fields["ProxyProtocolAllow"]; exists {
		if _, _, err := policySelection(value); err != nil {
			c.Reason = "Caddy PROXY protocol policy metadata is malformed."
			return c
		}
	}
	c.EnableLayer4 = general.Fields["EnableLayer4"]
	if c.EnableLayer4 == "" {
		c.EnableLayer4 = "0"
	}
	c.ProxyProtocolAllow = general.Fields["ProxyProtocolAllow"]
	c.Layer4EmptyVerified, _ = fields["layer4EmptyVerified"].(bool)
	if c.EnableLayer4 != "0" && (!c.Layer4EmptyVerified || c.ProxyProtocolAllow != "") {
		c.Reason = "Layer 4 rules or PROXY protocol policy are configured or could not be verified empty; client_ip routes require a fresh verifiable context."
		return c
	}
	if c.TrustedProxyListID != "" {
		trust, ok := (ProviderSnapshot{Resources: rows}).Find(c.TrustedProxyListID, "acl")
		if !ok {
			c.Reason = "The global trusted proxy list is missing or unresolved."
			return c
		}
		// The template consumes clientIps only here, not this list's request matcher
		// or inversion. Do not reinterpret it as a domain request ACL.
		c.TrustedProxyCIDRs = strings.Split(canonicalCIDRs(strings.Split(trust.Fields["clientIps"], ",")), ",")
		if reason := restrictedCIDRs(c.TrustedProxyCIDRs); reason != "" {
			c.Reason = "Unsafe global trusted proxy CIDRs: " + reason
			return c
		}
	}
	ids, order, err := policySelection(fields["ClientIpHeaders"])
	if err != nil {
		c.Reason = "Selected client IP headers are malformed."
		return c
	}
	c.HeaderOrder = order
	seen := map[string]bool{}
	if ids != "" {
		for _, id := range strings.Split(ids, ",") {
			if !uuidPattern.MatchString(id) || seen[id] {
				c.Reason = "Selected client IP header identities are invalid."
				return c
			}
			seen[id] = true
			kind, err := getHeader(id)
			if err != nil || len(kind) > 1024 || !headerName.MatchString(kind) {
				c.Reason = "A selected client IP header type is missing, invalid or unreadable."
				return c
			}
			for _, prior := range c.Headers {
				if strings.EqualFold(prior.Type, kind) {
					c.Reason = "Duplicate selected client IP header types are ambiguous."
					return c
				}
			}
			c.Headers = append(c.Headers, ClientIPHeader{ID: id, Type: kind})
		}
	}
	c.Suitable = true
	if c.TrustedProxyListID == "" {
		c.Summary = "client_ip uses the remote address because no global trusted proxies are configured."
	} else {
		c.Summary = "client_ip preserves Caddy's configured trusted proxy and client header semantics."
	}
	if len(c.Headers) == 0 {
		c.Summary += " No client header override is selected; Caddy defaults to X-Forwarded-For."
	}
	if c.HeaderOrder == "notExposed" {
		c.Summary += " The API exposes selected headers without their configured precedence; effective header order is unverified."
	}
	if c.EnableLayer4 == "1" {
		c.Summary += " Layer 4 is enabled with a verified empty rule model and no PROXY protocol allowlist. Custom imports remain outside the API snapshot."
	}
	return c
}
func (p *RealProviders) CaddyPolicy(ctx context.Context, o OPNsense) (CaddyPolicySnapshot, error) {
	ctx, closeHTTP := withProviderHTTP(ctx)
	defer closeHTTP()
	rows, err := p.AccessLists(ctx, o)
	if err != nil {
		return CaddyPolicySnapshot{}, err
	}
	general, clientIP, _ := p.caddyGeneral(ctx, o, rows)
	if general.Kind != "" {
		rows = append(rows, general)
	}
	return CaddyPolicySnapshot{Resources: rows, ClientIPContext: clientIP}, nil
}
func (p *DemoProviders) CaddyPolicy(ctx context.Context, o OPNsense) (CaddyPolicySnapshot, error) {
	rows, err := p.AccessLists(ctx, o)
	if err != nil {
		return CaddyPolicySnapshot{}, err
	}
	all := p.Store.Read().DemoResources[o.ID]
	general, _ := (ProviderSnapshot{Resources: all}).Find("general", "caddyGeneral")
	fields := map[string]any{}
	for k, v := range general.Fields {
		fields[k] = v
	}
	if general.Fields["EnableLayer4"] == "1" {
		empty := true
		for _, r := range all {
			if r.Kind == "caddyLayer4" {
				empty = false
			}
		}
		fields["layer4EmptyVerified"] = empty
	}
	clientIP := resolveClientIPContext(general, fields, rows, func(id string) (string, error) {
		r, ok := (ProviderSnapshot{Resources: all}).Find(id, "caddyClientIPHeader")
		if !ok {
			return "", unavailable("header missing")
		}
		return r.Fields["HeaderType"], nil
	})
	if general.Kind != "" {
		rows = append(rows, general)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Kind+rows[i].ID < rows[j].Kind+rows[j].ID })
	return CaddyPolicySnapshot{Resources: rows, ClientIPContext: clientIP}, nil
}
