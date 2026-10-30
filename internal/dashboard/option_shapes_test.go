package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestActualPHPEmptyAndNumericOptionArrayShapes(t *testing.T) {
	mode := "valid"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatal("resource read wrote")
		}
		fields := map[string]any{"FromDomain": "app.example.test", "option": []any{}, "DisableTls": []any{map[string]any{"value": "Translated Auto HTTPS", "selected": 1}, map[string]any{"value": "Translated disable", "selected": 0}}}
		if mode == "disable" {
			fields["DisableTls"] = []any{map[string]any{"value": "label not a key", "selected": 0}, map[string]any{"value": "also not a key", "selected": true}}
		}
		if mode == "malformed" {
			fields["DisableTls"] = []any{map[string]any{"value": "missing selection"}}
		}
		if mode == "structured" {
			fields["option"] = []any{map[string]any{"name": "unmodeled collection", "value": "not an option"}}
		}
		writeJSON(w, 200, map[string]any{"reverse": fields})
	}))
	defer server.Close()
	provider := &RealProviders{}
	router := OPNsense{BaseURL: server.URL, CAPEM: trustServer(server), APIKey: "fixture-key", APISecret: "fixture-secret"}
	initial, err := provider.getResource(context.Background(), router, "domain", sharedDemoACL)
	if err != nil || initial.Fields["DisableTls"] != "0" || initial.Fields["option"] != "" {
		t.Fatal(initial, err)
	}
	mode = "disable"
	changed, err := provider.getResource(context.Background(), router, "domain", sharedDemoACL)
	if err != nil || changed.Fields["DisableTls"] != "1" || changed.Fingerprint() == initial.Fingerprint() {
		t.Fatal(changed, err)
	}
	for _, bad := range []string{"malformed", "structured"} {
		mode = bad
		if _, err = provider.getResource(context.Background(), router, "domain", sharedDemoACL); err == nil {
			t.Fatal("unsupported array accepted", bad)
		}
	}
}
