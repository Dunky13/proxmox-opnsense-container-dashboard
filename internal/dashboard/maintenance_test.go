package dashboard

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

type sshRunFunc func(context.Context, Host, int, string) (RunResult, error)

func (f sshRunFunc) Run(ctx context.Context, h Host, id int, k string) (RunResult, error) {
	return f(ctx, h, id, k)
}

func TestMaintenanceAPIUsesSanitizedFailuresAndActualDistro(t *testing.T) {
	s, p, st := demoService(t)
	api := NewAPI(s, displayTestAuth{&Auth{config: AuthConfig{Demo: true}}}, nil)
	p.run = func(context.Context, Host, int, string) (RunResult, error) {
		return RunResult{}, errors.New("PRIVATE_KEY_SENTINEL /raw/provider/output")
	}
	request := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", path, strings.NewReader("{}"))
		r.Header.Set("Content-Type", "application/json")
		api.ServeHTTP(w, r)
		return w
	}
	w := request("/api/v1/hosts/main/containers/604/maintenance")
	if w.Code != 200 || strings.Contains(w.Body.String(), "PRIVATE_KEY_SENTINEL") || !strings.Contains(w.Body.String(), `"connected":false`) {
		t.Fatal(w.Code, w.Body.String())
	}
	p.run = func(context.Context, Host, int, string) (RunResult, error) {
		return RunResult{Distribution: "fedora", Version: "44", AppUpdate: true}, nil
	}
	r, err := s.Maintenance(context.Background(), "main", 604)
	if err != nil || r.OSUpdate || !r.AppUpdate || r.Distribution != "fedora" {
		t.Fatal(r, err)
	}
	_ = st.Update(func(next *State) error { next.Demo = false; next.Config.Hosts[0].SSH.Enabled = false; return nil })
	r, err = s.Maintenance(context.Background(), "main", 604)
	if err != nil || r.Connected || !strings.Contains(r.Reason, "disabled") {
		t.Fatal(r, err)
	}
	if w = request("/api/v1/hosts/main/containers/6e2/maintenance"); w.Code != 400 {
		t.Fatal(w.Code)
	}
}

func TestMaintenanceReadinessAndFreshJobCapability(t *testing.T) {
	s, p, st := demoService(t)
	p.run = func(context.Context, Host, int, string) (RunResult, error) {
		return RunResult{Distribution: "debian", Version: "13", AppUpdate: false}, nil
	}
	r, err := s.Maintenance(context.Background(), "main", 604)
	if err != nil || !r.Connected || r.AppUpdate || !r.OSUpdate || r.Version != "13" || r.Source != "demo" {
		t.Fatalf("%+v %v", r, err)
	}
	if err = st.Update(func(next *State) error { next.Demo = false; return nil }); err != nil {
		t.Fatal(err)
	}
	r, err = s.Maintenance(context.Background(), "main", 604)
	if err != nil || r.Source != "ssh" {
		t.Fatal("live provider observation source", r, err)
	}
	c, _ := p.Inventory(context.Background(), st.Read().Config).Find("main:604")
	if _, err = s.StartJob(context.Background(), JobRequest{HostID: "main", VMID: 604, Kind: "appUpdate", Fingerprint: c.Fingerprint}); err == nil {
		t.Fatal("missing update script accepted")
	}
}

func TestCapabilitiesParseExactMarkers(t *testing.T) {
	for _, tc := range []struct {
		raw    string
		app    bool
		distro string
	}{
		{"ID=alpine\nVERSION_ID=3.22\nDASHBOARD_APP_UPDATE=1\n", true, "alpine"},
		{"ID=debian\nVERSION_ID=13\nDASHBOARD_APP_UPDATE=0\n", false, "debian"},
	} {
		r, err := parseCapabilities(tc.raw)
		if err != nil || r.AppUpdate != tc.app || r.Distribution != tc.distro {
			t.Fatalf("%+v %v", r, err)
		}
	}
	if _, err := parseCapabilities("ID=debian\n"); err == nil {
		t.Fatal("missing capability invented")
	}
	cmd, err := fixedCommand(609, "capabilities")
	if err != nil || !strings.Contains(cmd, "test -x /usr/bin/update") {
		t.Fatalf("%s %v", cmd, err)
	}
}
