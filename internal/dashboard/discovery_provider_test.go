package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fixtureRealProvider struct {
	*RealProviders
	demo *DemoProviders
}

func (p *fixtureRealProvider) Inventory(ctx context.Context, cfg Config) Inventory {
	return p.demo.Inventory(ctx, cfg)
}

func TestRealDiscoveredRouteUsesGetterRelationsAndAutomaticHTTPS(t *testing.T) {
	rows := []Resource{
		{ID: vlan6Subnet, Kind: "subnet", Fields: map[string]string{"subnet": "192.168.6.0/23", "subnet_id": "6", "description": "Discovered DHCP network"}},
		{ID: sharedDemoACL, Kind: "acl", Fields: map[string]string{"accesslistName": "Shared VPN", "description": "Manual policy", "clientIps": "10.8.0.0/24,fd00:1234::/64", "RequestMatcher": "remote_ip", "accesslistInvert": "0", "HttpResponseCode": "403"}},
	}
	serial := 0
	failApply := false
	writes := []string{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, secret, ok := r.BasicAuth()
		if !ok || user != "fixture-key" || secret != "fixture-secret" {
			t.Error("wrong credentials")
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/")
		switch path {
		case "caddy/general/get":
			writeJSON(w, 200, map[string]any{"caddy": map[string]any{"general": map[string]any{"enabled": "1", "accesslist": map[string]any{sharedDemoACL: map[string]any{"value": "VPN", "selected": 0}}}}})
			return
		case "kea/dhcpv4/get":
			writeJSON(w, 200, map[string]any{"dhcpv4": map[string]any{"general": map[string]any{"enabled": "1"}}})
			return
		case "caddy/service/status", "kea/service/status":
			writeJSON(w, 200, map[string]string{"status": "running"})
			return
		case "caddy/service/reconfigure", "kea/service/reconfigure":
			if r.Method != "POST" {
				t.Error("wrong apply method")
			}
			writes = append(writes, path)
			if failApply {
				writeJSON(w, 200, map[string]string{"status": "failed"})
			} else {
				writeJSON(w, 200, map[string]string{"status": "ok"})
			}
			return
		}
		for _, kind := range []string{"subnet", "acl", "reservation", "domain", "handle", "subdomain"} {
			ep, _ := resourceEndpoint(kind)
			base := ep.Controller + "/"
			if path == base+"search_"+ep.Command {
				if r.Method != "GET" || r.URL.Query().Get("rowCount") != "-1" {
					t.Error("incomplete search")
				}
				found := []map[string]string{}
				for _, row := range rows {
					if row.Kind == kind {
						found = append(found, map[string]string{"uuid": row.ID, "description": "translated display"})
					}
				}
				writeJSON(w, 200, map[string]any{"total": len(found), "rows": found})
				return
			}
			if strings.HasPrefix(path, base+"get_"+ep.Command+"/") {
				id := strings.TrimPrefix(path, base+"get_"+ep.Command+"/")
				for _, row := range rows {
					if row.Kind == kind && row.ID == id {
						fields := map[string]any{}
						for field, value := range row.Fields {
							switch field {
							case "subnet":
								if kind == "reservation" {
									fields[field] = map[string]any{value: map[string]any{"value": "Translated subnet", "selected": 1}}
								} else {
									fields[field] = value
								}
							case "accesslist", "reverse", "RequestMatcher":
								fields[field] = map[string]any{}
								if value != "" {
									fields[field] = map[string]any{value: map[string]any{"value": "Translated relation", "selected": 1}}
								}
							case "clientIps":
								options := map[string]any{}
								for _, cidr := range strings.Split(value, ",") {
									options[cidr] = map[string]any{"value": cidr, "selected": 1}
								}
								fields[field] = options
							default:
								fields[field] = value
							}
						}
						writeJSON(w, 200, map[string]any{ep.Wrapper: fields})
						return
					}
				}
				http.NotFound(w, r)
				return
			}
			if path == base+"add_"+ep.Command || strings.HasPrefix(path, base+"set_"+ep.Command+"/") {
				if kind == "subnet" || kind == "acl" {
					t.Fatal("wrote discovered shared infrastructure", path)
				}
				writes = append(writes, path)
				var body map[string]map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				fields, exists := body[ep.Wrapper]
				if !exists {
					t.Fatal("wrong request wrapper", body)
				}
				if kind == "domain" {
					if _, exists := fields["CustomCertificate"]; exists {
						t.Fatal("required invented certificate", fields)
					}
				}
				id := strings.TrimPrefix(path, base+"set_"+ep.Command+"/")
				if path == base+"add_"+ep.Command {
					serial++
					id = fmt.Sprintf("bbbbbbbb-1111-4111-8111-%012d", serial)
					rows = append(rows, Resource{ID: id, Kind: kind, Fields: clone(fields)})
				} else {
					for i, row := range rows {
						if row.ID == id && row.Kind == kind {
							for field, value := range fields {
								rows[i].Fields[field] = value
							}
						}
					}
				}
				writeJSON(w, 200, map[string]string{"result": "saved", "uuid": id})
				return
			}
		}
		t.Errorf("unexpected provider prerequisite or write %s %s", r.Method, path)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	s, p, store := demoService(t)
	if err := store.Update(func(st *State) error {
		o := &st.Config.OPNsenses[0]
		o.BaseURL = srv.URL
		o.CAPEM = trustServer(srv)
		o.APIKey = "fixture-key"
		o.APISecret = "fixture-secret"
		o.PrivateAccessListID = sharedDemoACL
		o.SubnetID = ""
		o.CertificateID = ""
		o.DomainReady = false
		o.UpstreamScheme = ""
		o.UpstreamPort = 0
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s.Providers = &fixtureRealProvider{RealProviders: &RealProviders{}, demo: p.DemoProviders}
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604})
	if err != nil {
		t.Fatal(err)
	}
	if plan.DesiredRoute.Port != 80 || plan.ResolvedSubnet == nil || plan.ResolvedSubnet.CIDR != "192.168.6.0/23" || len(writes) != 0 {
		t.Fatal(plan, writes)
	}
	failApply = true
	op, err := s.ApplyPlan(context.Background(), plan.ID)
	if err == nil || op.Step != "reconfigure" || op.ManualRecovery {
		t.Fatal(op, err)
	}
	failApply = false
	if _, err = s.Recover(context.Background(), op.ID); err != nil {
		t.Fatal(err)
	}
	healthy := false
	for _, item := range s.Reconcile(context.Background()).Items {
		if item.Key == "main:604" {
			healthy = item.State == "healthy" && item.PrivateAccessList != nil && strings.Join(item.PrivateAccessList.CIDRs, ",") == "10.8.0.0/24,fd00:1234::/64"
		}
	}
	if !healthy {
		t.Fatal("real getter route did not reconcile")
	}
}

func TestLegacyAppRouteMigratesFromVerifiedHandler(t *testing.T) {
	s, p, store := demoService(t)
	scheme, port := "https", 9443
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604, UpstreamScheme: &scheme, UpstreamPort: &port})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
		t.Fatal(err)
	}
	if err = store.Update(func(st *State) error {
		own := st.Owned["main:604"]
		own.DesiredRoute = RouteConfig{}
		for i := range own.Resources {
			own.Resources[i].SubnetID = ""
		}
		st.Owned[own.Key] = own
		st.Config.OPNsenses[0].UpstreamScheme = "http"
		st.Config.OPNsenses[0].UpstreamPort = 1234
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	plan, err = s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 604})
	if err != nil || plan.DesiredRoute.Scheme != "https" || plan.DesiredRoute.Port != 9443 {
		t.Fatal("silently changed legacy upstream", plan, err)
	}
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
		t.Fatal(err)
	}
	before := len(p.events)
	plan, err = s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 603})
	if err != nil || plan.DesiredRoute.Port != 80 || plan.DesiredRoute.Scheme != "http" || len(p.events) != before {
		t.Fatal("new app inherited hidden firewall defaults", plan, err)
	}
}
