package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestExistingRealProviderImportAndNativeEndpoints(t *testing.T) {
	s, p, store := hikyoService(t)
	rows := clone(store.Read().DemoResources["demo-edge"])
	// Opaque provider fields are fingerprinted and preserved, but never reflected
	// through import diagnostics, plan effects, operation or cleanup previews.
	changeHikyo(rows, hikyoChild, "opaqueProviderField", "fixture-secret-not-for-review")
	changeHikyo(rows, hikyoHandle, "HeaderValue", "fixture-header-secret-not-for-review")
	before := clone(rows)
	var mu sync.Mutex
	writes := []string{}
	serial := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		user, secret, ok := r.BasicAuth()
		if !ok || user != "fixture-key" || secret != "fixture-secret" {
			t.Error("wrong provider auth")
			http.Error(w, "forbidden", 403)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/")
		switch path {
		case "caddy/general/get":
			writeJSON(w, 200, map[string]any{"caddy": map[string]any{"general": map[string]any{"enabled": "1", "accesslist": map[string]any{trustDemoACL: map[string]any{"selected": 1, "value": "Translated trust"}}, "ClientIpHeaders": map[string]any{clientDemoHeader: map[string]any{"selected": 1, "value": "Translated header"}}, "EnableLayer4": "0", "ProxyProtocolAllow": ""}}})
			return
		case "caddy/reverse_proxy/get_header/" + clientDemoHeader:
			writeJSON(w, 200, map[string]any{"header": map[string]string{"HeaderType": "X-Forwarded-For", "HeaderValue": "fixture-unread-header-secret"}})
			return
		case "kea/dhcpv4/get":
			writeJSON(w, 200, map[string]any{"dhcpv4": map[string]any{"general": map[string]string{"enabled": "1"}}})
			return
		case "kea/service/status", "caddy/service/status":
			writeJSON(w, 200, map[string]string{"status": "running"})
			return
		case "kea/service/reconfigure", "caddy/service/reconfigure":
			if r.Method != "POST" {
				t.Error("wrong apply method")
			}
			writes = append(writes, path)
			writeJSON(w, 200, map[string]string{"status": "ok"})
			return
		}
		for _, kind := range []string{"reservation", "acl", "domain", "subdomain", "handle", "subnet"} {
			ep, _ := resourceEndpoint(kind)
			base := ep.Controller + "/"
			if path == base+"search_"+ep.Command {
				found := []map[string]string{}
				for _, row := range rows {
					if row.Kind == kind {
						found = append(found, map[string]string{"uuid": row.ID})
					}
				}
				writeJSON(w, 200, map[string]any{"total": len(found), "rows": found})
				return
			}
			if strings.HasPrefix(path, base+"get_"+ep.Command+"/") {
				id := strings.TrimPrefix(path, base+"get_"+ep.Command+"/")
				for _, row := range rows {
					if row.Kind != kind || row.ID != id {
						continue
					}
					fields := map[string]any{}
					for field, value := range row.Fields {
						if field == "reverse" || field == "subdomain" || field == "accesslist" || (kind == "reservation" && field == "subnet") {
							options := map[string]any{}
							if value != "" {
								options[value] = map[string]any{"selected": true, "value": "Translated unrelated label"}
							}
							fields[field] = options
						} else {
							fields[field] = value
						}
					}
					writeJSON(w, 200, map[string]any{ep.Wrapper: fields})
					return
				}
				http.NotFound(w, r)
				return
			}
			create := path == base+"add_"+ep.Command
			set := strings.HasPrefix(path, base+"set_"+ep.Command+"/")
			del := strings.HasPrefix(path, base+"del_"+ep.Command+"/")
			if !create && !set && !del {
				continue
			}
			if r.Method != "POST" {
				t.Error("wrong mutation method")
			}
			if kind == "domain" || kind == "acl" || kind == "subnet" {
				t.Errorf("external resource mutation %s", path)
				http.Error(w, "blocked", 409)
				return
			}
			writes = append(writes, path)
			if del {
				id := strings.TrimPrefix(path, base+"del_"+ep.Command+"/")
				for i, row := range rows {
					if row.Kind == kind && row.ID == id {
						rows = append(rows[:i], rows[i+1:]...)
						writeJSON(w, 200, map[string]string{"result": "deleted"})
						return
					}
				}
				http.NotFound(w, r)
				return
			}
			var body map[string]map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				http.Error(w, "bad body", 400)
				return
			}
			fields, ok := body[ep.Wrapper]
			if !ok {
				t.Error("wrong native wrapper", body)
				http.Error(w, "bad wrapper", 400)
				return
			}
			id := strings.TrimPrefix(path, base+"set_"+ep.Command+"/")
			if create {
				serial++
				id = fmt.Sprintf("bbbbbbbb-1111-4111-8111-%012d", serial)
				rows = append(rows, Resource{ID: id, Kind: kind, Fields: clone(fields)})
			} else {
				for i, row := range rows {
					if row.Kind == kind && row.ID == id {
						for f, v := range fields {
							rows[i].Fields[f] = v
						}
					}
				}
			}
			writeJSON(w, 200, map[string]string{"result": "saved", "uuid": id})
			return
		}
		t.Errorf("unexpected endpoint %s", path)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	mutateHikyo(t, store, func(st *State, _ *[]Resource) {
		o := &st.Config.OPNsenses[0]
		o.BaseURL = srv.URL
		o.CAPEM = trustServer(srv)
		o.APIKey = "fixture-key"
		o.APISecret = "fixture-secret"
	})
	s.Providers = &fixtureRealProvider{RealProviders: &RealProviders{}, demo: p.DemoProviders, transform: p.inventory}
	item := hikyoItem(t, s)
	if !item.ImportAvailable {
		t.Fatal(item)
	}
	raw, _ := json.Marshal(item)
	if strings.Contains(string(raw), "fixture-") {
		t.Fatal("secret reflected in reconciliation", string(raw))
	}
	plan, err := s.Plan(context.Background(), PlanRequest{Action: "import", HostID: "main", VMID: 609})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(plan)
	if strings.Contains(string(raw), "fixture-") {
		t.Fatal("secret reflected in plan", string(raw))
	}
	op, err := s.ApplyPlan(context.Background(), plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(op)
	if strings.Contains(string(raw), "fixture-") {
		t.Fatal("secret reflected in operation", string(raw))
	}
	if len(writes) != 0 {
		t.Fatal("metadata-only import called provider mutation", writes)
	}
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err == nil {
		t.Fatal("replayed real adapter import")
	}
	scheme, port := "https", 8443
	plan, err = s.Plan(context.Background(), PlanRequest{Action: "ensure", HostID: "main", VMID: 609, UpstreamScheme: &scheme, UpstreamPort: &port})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
		t.Fatal(err)
	}
	if item = hikyoItem(t, s); item.State != "healthy" {
		t.Fatal(item)
	}
	requireExternalUnchanged(t, before, rows)
	if !strings.Contains(strings.Join(writes, ","), "set_subdomain/"+hikyoChild) {
		t.Fatal("child not updated via native endpoint", writes)
	}
	removeHikyo(t, p)
	// The fixture's transform was copied above; use the new absence transform.
	s.Providers.(*fixtureRealProvider).transform = p.inventory
	plan, err = s.Plan(context.Background(), PlanRequest{Action: "cleanup", HostID: "main", VMID: 609})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(plan)
	if strings.Contains(string(raw), "fixture-") {
		t.Fatal("secret reflected in cleanup plan", string(raw))
	}
	if _, err = s.ApplyPlan(context.Background(), plan.ID); err != nil {
		t.Fatal(err)
	}
	requireExternalUnchanged(t, before, rows)
	if !strings.Contains(strings.Join(writes, ","), "del_subdomain/"+hikyoChild) {
		t.Fatal("child not deleted via native endpoint", writes)
	}
}
