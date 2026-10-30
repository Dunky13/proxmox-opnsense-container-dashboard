package dashboard

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func providerFixtureCertificate(t *testing.T, address string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "isolated fixture"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true, IPAddresses: []net.IP{net.ParseIP(address)},
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func TestProviderHTTPTrustRevisionIsolation(t *testing.T) {
	fixture := snapshotReadFixture(t, 2, nil)
	var accepted atomic.Int64
	srv, connections := providerReadServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accepted.Add(1)
		fixture.ServeHTTP(w, r)
	}), providerFixtureCertificate(t, "127.0.0.1"))
	other := providerFixtureCertificate(t, "127.0.0.1")
	wrongCA := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: other.Certificate[0]}))
	ctx, closeHTTP := withProviderHTTP(context.Background())
	defer closeHTTP()
	p := &RealProviders{}
	o := fixtureRouter(srv)
	if _, err := p.AccessLists(ctx, o); err != nil {
		t.Fatal(err)
	}
	before := accepted.Load()
	for _, ca := range []string{"", wrongCA, "invalid PEM"} {
		o.CAPEM = ca
		if _, err := p.AccessLists(ctx, o); err == nil {
			t.Fatal("warm connection accepted a different trust revision")
		}
	}
	if accepted.Load() != before {
		t.Fatal("untrusted request reached provider handler")
	}
	o.CAPEM = trustServer(srv)
	o.BaseURL = strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	if _, err := p.AccessLists(ctx, o); err == nil {
		t.Fatal("TLS hostname verification was bypassed")
	}
	o.BaseURL = srv.URL
	if _, err := p.AccessLists(ctx, o); err != nil {
		t.Fatal("failed trust revision poisoned original client", err)
	}
	closeHTTP()
	connections.assertClosed(t)
}

func TestProviderHTTPConcurrentCredentialIsolation(t *testing.T) {
	var mu sync.Mutex
	connectionUsers := map[string]string{}
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	fixture := snapshotReadFixture(t, 3, func(w http.ResponseWriter, r *http.Request, kind string, _ int) bool {
		ep, _ := resourceEndpoint(kind)
		user, _, _ := r.BasicAuth()
		writeJSON(w, 200, map[string]any{ep.Wrapper: map[string]string{"description": user}})
		return true
	})
	srv, connections := providerReadServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, secret, ok := r.BasicAuth()
		if !ok || (user != "first" && user != "second") || secret != user+"-secret" {
			t.Error("request mixed provider credentials")
			http.Error(w, "bad auth", 403)
			return
		}
		mu.Lock()
		if previous := connectionUsers[r.RemoteAddr]; previous != "" && previous != user {
			t.Error("independent operations shared a credential-bound connection")
		}
		connectionUsers[r.RemoteAddr] = user
		mu.Unlock()
		if strings.Contains(r.URL.Path, "/search_") {
			started <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		fixture.ServeHTTP(w, r)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &RealProviders{}
	done := make(chan error, 2)
	for _, user := range []string{"first", "second"} {
		o := fixtureRouter(srv)
		o.APIKey, o.APISecret = user, user+"-secret"
		go func() {
			rows, err := p.AccessLists(ctx, o)
			for _, row := range rows {
				if row.Fields["description"] != o.APIKey {
					t.Error("operation observed another request's credentials")
				}
			}
			done <- err
		}()
	}
	awaitProviderRead(t, started)
	awaitProviderRead(t, started)
	close(release)
	for range 2 {
		if err := awaitProviderRead(t, done); err != nil {
			t.Fatal(err)
		}
	}
	connections.assertClosed(t)
}

func TestProviderHTTPCredentialsStayOnEachRequest(t *testing.T) {
	var calls atomic.Int64
	fixture := snapshotReadFixture(t, 2, nil)
	srv, connections := providerReadServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, secret, ok := r.BasicAuth()
		want := "first"
		if calls.Add(1) > 3 {
			want = "second"
		}
		if !ok || user != want || secret != want+"-secret" {
			t.Error("client cached credentials across settings revisions")
		}
		fixture.ServeHTTP(w, r)
	}))
	ctx, closeHTTP := withProviderHTTP(context.Background())
	defer closeHTTP()
	for _, user := range []string{"first", "second"} {
		o := fixtureRouter(srv)
		o.APIKey, o.APISecret = user, user+"-secret"
		if _, err := (&RealProviders{}).AccessLists(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 6 || connections.opened.Load() > 2 {
		t.Fatalf("expected six fresh reads with bounded reuse: reads=%d connections=%d", calls.Load(), connections.opened.Load())
	}
	closeHTTP()
	connections.assertClosed(t)
}

func TestProviderHTTPForbidsRedirectsAndWriteRetries(t *testing.T) {
	var redirected, writes atomic.Int64
	target, targetConnections := providerReadServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
		writeJSON(w, 200, map[string]string{"result": "saved"})
	}))
	for _, status := range []int{302, 307, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv, connections := providerReadServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writes.Add(1)
				if r.Method != "POST" {
					t.Error("unexpected followup read or method")
				}
				w.Header().Set("Location", target.URL)
				http.Error(w, "sensitive body", status)
			}))
			if _, err := (&RealProviders{}).Create(context.Background(), fixtureRouter(srv), "handle", map[string]string{"enabled": "1"}); err == nil || strings.Contains(err.Error(), "sensitive") {
				t.Fatalf("write failure accepted or leaked: %v", err)
			}
			connections.assertClosed(t)
		})
	}
	if writes.Load() != 3 || redirected.Load() != 0 {
		t.Fatalf("write retried or redirected: writes=%d redirected=%d", writes.Load(), redirected.Load())
	}
	targetConnections.assertClosed(t)
}

func providerDisplayAPIFixture(t *testing.T) (http.Handler, *atomic.Int64, *providerConnections) {
	t.Helper()
	var reads atomic.Int64
	opn := snapshotReadFixture(t, 40, nil)
	pve := inventoryReadFixture(t, 33, nil)
	srv, connections := providerReadServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if strings.HasPrefix(r.URL.Path, "/api2/") {
			if r.Header.Get("Authorization") != "PVEAPIToken=fixture@pve!test=fixture-secret" {
				t.Error("Proxmox credentials crossed with OPNsense credentials")
			}
			pve.ServeHTTP(w, r)
			return
		}
		user, secret, ok := r.BasicAuth()
		if !ok || user != "fixture-key" || secret != "fixture-secret" {
			t.Error("OPNsense credentials crossed with Proxmox credentials")
		}
		opn.ServeHTTP(w, r)
	}))
	s, _, store := demoService(t)
	s.Providers = &RealProviders{}
	h1, h2 := fixtureHost(srv), fixtureHost(srv)
	h2.ID = "other"
	h1.OPNsenseID, h2.OPNsenseID = "edge", "edge"
	o := fixtureRouter(srv)
	o.ID = "edge"
	if err := store.Update(func(st *State) error {
		st.Config = Config{Revision: 1, Hosts: []Host{h1, h2}, OPNsenses: []OPNsense{o}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	auth, err := NewAuth(context.Background(), AuthConfig{AdminToken: "fixture-admin"})
	if err != nil {
		t.Fatal(err)
	}
	api := NewAPI(s, auth, nil)
	return api, &reads, connections
}

func TestProviderAPIDisplayReusesObservationsAndCloses(t *testing.T) {
	api, reads, connections := providerDisplayAPIFixture(t)
	var previous int64
	for request := int64(1); request <= 2; request++ {
		r := httptest.NewRequest("GET", "/api/v1/reconciliation", nil)
		r.Header.Set("Authorization", "Bearer fixture-admin")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		var out Reconciliation
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || !out.Inventory.Complete || len(out.Inventory.Containers) != 66 {
			t.Fatalf("reconciliation failed: HTTP %d", w.Code)
		}
		// Two host inventories and one shared firewall snapshot. A nearby browser
		// reload reuses these display observations without resetting observedAt.
		if reads.Load() != 346 {
			t.Fatalf("display provider reads: want 346 after request %d, got %d", request, reads.Load())
		}
		opened := connections.opened.Load()
		if (request == 1 && (opened == 0 || opened > 4)) || (request == 2 && opened != previous) {
			t.Fatalf("display connection count: previous=%d now=%d", previous, opened)
		}
		t.Logf("request=%d provider reads=%d connections=%d containers=%d", request, reads.Load(), opened, len(out.Inventory.Containers))
		previous = opened
		connections.assertClosed(t)
	}
}

func TestProviderAPIMutationReadsStayFreshAfterDisplay(t *testing.T) {
	api, reads, connections := providerDisplayAPIFixture(t)
	get := httptest.NewRequest("GET", "/api/v1/reconciliation", nil)
	get.Header.Set("Authorization", "Bearer fixture-admin")
	w := httptest.NewRecorder()
	api.ServeHTTP(w, get)
	if w.Code != 200 || reads.Load() != 346 {
		t.Fatal("display observation fixture failed")
	}
	// A missing target is rejected only after a fresh complete inventory and
	// snapshot, even though GET display observations are warm. No writes occur.
	post := httptest.NewRequest("POST", "/api/v1/plans", strings.NewReader(`{"action":"ensure","hostId":"main","vmid":999}`))
	post.Header.Set("Authorization", "Bearer fixture-admin")
	post.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	api.ServeHTTP(w, post)
	if w.Code != 409 || reads.Load() != 692 {
		t.Fatalf("plan bypassed fresh reads: HTTP %d provider reads=%d", w.Code, reads.Load())
	}
	t.Logf("display reads=346; fresh plan added=%d; total=%d", reads.Load()-346, reads.Load())
	connections.assertClosed(t)
}
