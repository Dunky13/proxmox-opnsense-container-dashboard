package dashboard

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Count accepted connections rather than TLS wall time. The real adapter must
// read every getter/config, reuse its bounded pool and close it on return.
type providerConnections struct {
	opened atomic.Int64
	closed atomic.Int64
	events chan struct{}
}

func providerReadServer(t *testing.T, handler http.Handler, certs ...tls.Certificate) (*httptest.Server, *providerConnections) {
	t.Helper()
	connections := &providerConnections{events: make(chan struct{}, 1024)}
	srv := httptest.NewUnstartedServer(handler)
	if len(certs) != 0 {
		srv.TLS = &tls.Config{Certificates: certs}
	}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			connections.opened.Add(1)
		case http.StateClosed:
			connections.closed.Add(1)
		}
		select {
		case connections.events <- struct{}{}:
		default:
		}
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, connections
}

func (c *providerConnections) assertClosed(t *testing.T) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for c.closed.Load() != c.opened.Load() {
		select {
		case <-c.events:
		case <-deadline.C:
			t.Fatalf("provider pool leaked connections: opened=%d closed=%d", c.opened.Load(), c.closed.Load())
		}
	}
}

func fixtureResourceID(index int) string {
	return fmt.Sprintf("12345678-1111-4111-8111-%012d", index+1)
}

func snapshotReadFixture(t *testing.T, count int, getter func(http.ResponseWriter, *http.Request, string, int) bool) http.Handler {
	t.Helper()
	kinds := []string{"reservation", "acl", "domain", "handle", "subdomain", "subnet"}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected write: %s %s", r.Method, r.URL.Path)
			http.Error(w, "read-only fixture", 500)
			return
		}
		for _, kind := range kinds {
			ep, _ := resourceEndpoint(kind)
			base := "/api/" + ep.Controller + "/"
			if r.URL.Path == base+"search_"+ep.Command {
				rows := []map[string]string{}
				// Deliberately reverse upstream ordering.
				for index := count - 1; index >= 0; index-- {
					rows = append(rows, map[string]string{"uuid": fixtureResourceID(index)})
				}
				writeJSON(w, 200, map[string]any{"total": count, "rows": rows})
				return
			}
			prefix := base + "get_" + ep.Command + "/"
			if strings.HasPrefix(r.URL.Path, prefix) {
				id := strings.TrimPrefix(r.URL.Path, prefix)
				index, err := strconv.Atoi(id[len(id)-12:])
				if err != nil || index < 1 || index > count {
					t.Errorf("unexpected getter %s", r.URL.Path)
				}
				if getter != nil && getter(w, r, kind, index-1) {
					return
				}
				writeJSON(w, 200, map[string]any{ep.Wrapper: map[string]string{"description": kind + id}})
				return
			}
		}
		switch r.URL.Path {
		case "/api/caddy/general/get":
			writeJSON(w, 200, map[string]any{"caddy": map[string]any{"general": map[string]string{"enabled": "1", "accesslist": ""}}})
		case "/api/kea/dhcpv4/get":
			writeJSON(w, 200, map[string]any{"dhcpv4": map[string]any{"general": map[string]string{"enabled": "1"}}})
		case "/api/kea/service/status", "/api/caddy/service/status":
			writeJSON(w, 200, map[string]string{"status": "running"})
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
}

func inventoryReadFixture(t *testing.T, count int, config func(http.ResponseWriter, *http.Request, int) bool) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected write: %s %s", r.Method, r.URL.Path)
		}
		switch r.URL.Path {
		case "/api2/json/access/permissions":
			writeJSON(w, 200, map[string]any{"data": map[string]any{"/": map[string]int{"VM.Audit": 1}}})
		case "/api2/json/nodes":
			writeJSON(w, 200, map[string]any{"data": []any{map[string]string{"node": "pve", "status": "online"}}})
		case "/api2/json/cluster/status":
			writeJSON(w, 200, map[string]any{"data": []any{map[string]any{"type": "cluster", "quorate": 1}}})
		case "/api2/json/cluster/resources":
			rows := []map[string]any{}
			for index := count - 1; index >= 0; index-- {
				status := "stopped"
				if index%3 == 0 {
					status = "running"
				}
				rows = append(rows, map[string]any{"vmid": 600 + index, "type": "lxc", "node": "pve", "name": "app", "status": status})
			}
			writeJSON(w, 200, map[string]any{"data": rows})
		default:
			parts := strings.Split(r.URL.Path, "/")
			if len(parts) != 8 {
				t.Errorf("unexpected endpoint %s", r.URL.Path)
				http.NotFound(w, r)
				return
			}
			id, err := strconv.Atoi(parts[6])
			if err != nil || id < 600 || id >= 600+count {
				t.Errorf("unexpected container %s", r.URL.Path)
			}
			if parts[7] == "interfaces" {
				writeJSON(w, 200, map[string]any{"data": []any{}})
				return
			}
			if config != nil && config(w, r, id-600) {
				return
			}
			writeJSON(w, 200, map[string]any{"data": map[string]string{"hostname": fmt.Sprintf("app-%d", id), "digest": fmt.Sprintf("digest-%d", id), "ostype": "debian", "net0": "hwaddr=d0:cc:37:00:06:12,ip=192.168.6.12/24", "rootfs": fmt.Sprintf("local:vm-%d-disk-0,size=8G", id), "meta": "ctime=123456789"}})
		}
	})
}

func fixtureHost(srv *httptest.Server) Host {
	h := DefaultProfiles()[0]
	h.BaseURL, h.CAPEM, h.Node = srv.URL, trustServer(srv), "pve"
	h.TokenID, h.TokenSecret = "fixture@pve!test", "fixture-secret"
	return h
}

func fixtureRouter(srv *httptest.Server) OPNsense {
	return OPNsense{BaseURL: srv.URL, CAPEM: trustServer(srv), APIKey: "fixture-key", APISecret: "fixture-secret"}
}

func TestRealProviderSnapshotReusesConnections(t *testing.T) {
	var requests atomic.Int64
	fixture := snapshotReadFixture(t, 40, nil)
	srv, connections := providerReadServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		fixture.ServeHTTP(w, r)
	}))
	snap, err := (&RealProviders{}).Snapshot(context.Background(), fixtureRouter(srv))
	if err != nil || !snap.Ready || len(snap.Resources) != 241 {
		t.Fatalf("snapshot resources=%d ready=%v err=%v", len(snap.Resources), snap.Ready, err)
	}
	if requests.Load() != 250 {
		t.Fatalf("fresh complete snapshot requires 250 reads, got %d", requests.Load())
	}
	for index := 1; index < len(snap.Resources); index++ {
		a, b := snap.Resources[index-1], snap.Resources[index]
		if a.Kind+a.ID >= b.Kind+b.ID {
			t.Fatal("snapshot ordering changed")
		}
	}
	t.Logf("snapshot: reads=%d resources=%d connections=%d", requests.Load(), len(snap.Resources), connections.opened.Load())
	if connections.opened.Load() > 2 {
		t.Errorf("snapshot must reuse at most two connections; opened %d", connections.opened.Load())
	}
	connections.assertClosed(t)
}

func TestRealProviderInventoryReusesConnections(t *testing.T) {
	var requests atomic.Int64
	fixture := inventoryReadFixture(t, 33, nil)
	srv, connections := providerReadServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		fixture.ServeHTTP(w, r)
	}))
	inv := (&RealProviders{}).Inventory(context.Background(), Config{Hosts: []Host{fixtureHost(srv)}})
	if !inv.Complete || len(inv.Containers) != 33 {
		t.Fatalf("inventory %+v", inv)
	}
	if requests.Load() != 48 {
		t.Fatalf("fresh complete inventory requires 48 reads, got %d", requests.Load())
	}
	for index := 1; index < len(inv.Containers); index++ {
		if inv.Containers[index-1].Key >= inv.Containers[index].Key {
			t.Fatal("inventory ordering changed")
		}
	}
	t.Logf("inventory: reads=%d containers=%d connections=%d", requests.Load(), len(inv.Containers), connections.opened.Load())
	if connections.opened.Load() > 2 {
		t.Errorf("inventory must reuse at most two connections; opened %d", connections.opened.Load())
	}
	connections.assertClosed(t)
}

func gatedProviderRead(t *testing.T, kind string, gate func(http.ResponseWriter, *http.Request) bool) (*providerConnections, func(context.Context) error) {
	t.Helper()
	if kind == "snapshot" {
		srv, connections := providerReadServer(t, snapshotReadFixture(t, 8, func(w http.ResponseWriter, r *http.Request, _ string, _ int) bool {
			return gate(w, r)
		}))
		return connections, func(ctx context.Context) error {
			snap, err := (&RealProviders{}).Snapshot(ctx, fixtureRouter(srv))
			if err != nil && snap.Ready {
				t.Error("failed snapshot marked ready")
			}
			return err
		}
	}
	srv, connections := providerReadServer(t, inventoryReadFixture(t, 8, func(w http.ResponseWriter, r *http.Request, _ int) bool {
		return gate(w, r)
	}))
	return connections, func(ctx context.Context) error {
		inv := (&RealProviders{}).Inventory(ctx, Config{Hosts: []Host{fixtureHost(srv)}})
		if !inv.Complete && (len(inv.Containers) != 0 || inv.Hosts[0].NextVMID != nil) {
			t.Error("failed inventory exposed partial containers or allocation")
		}
		return inventoryError(inv)
	}
}

func awaitProviderRead[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("provider read did not reach logical barrier or finish")
		var zero T
		return zero
	}
}

func TestRealProviderReadsBounded(t *testing.T) {
	for _, kind := range []string{"snapshot", "inventory"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started := make(chan struct{}, 64)
			release := make(chan struct{})
			var active, maximum atomic.Int64
			connections, read := gatedProviderRead(t, kind, func(w http.ResponseWriter, r *http.Request) bool {
				now := active.Add(1)
				defer active.Add(-1)
				for old := maximum.Load(); now > old; old = maximum.Load() {
					if maximum.CompareAndSwap(old, now) {
						break
					}
				}
				started <- struct{}{}
				select {
				case <-release:
					return false
				case <-r.Context().Done():
					return true
				}
			})
			done := make(chan error, 1)
			go func() { done <- read(ctx) }()
			awaitProviderRead(t, started)
			awaitProviderRead(t, started)
			close(release)
			if err := awaitProviderRead(t, done); err != nil {
				t.Fatal(err)
			}
			if maximum.Load() != 2 {
				t.Fatalf("expected exactly two concurrent reads, got %d", maximum.Load())
			}
			connections.assertClosed(t)
		})
	}
}

func TestRealProviderReadFailureCancelsSibling(t *testing.T) {
	for _, kind := range []string{"snapshot", "inventory"} {
		for _, failure := range []string{"cancel", "http", "malformed", "incomplete"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				started := make(chan struct{}, 2)
				fail := make(chan struct{})
				var calls atomic.Int64
				var handlers sync.WaitGroup
				connections, read := gatedProviderRead(t, kind, func(w http.ResponseWriter, r *http.Request) bool {
					handlers.Add(1)
					defer handlers.Done()
					n := calls.Add(1)
					started <- struct{}{}
					if n == 1 && failure != "cancel" {
						select {
						case <-fail:
						case <-r.Context().Done():
							return true
						}
						switch failure {
						case "http":
							http.Error(w, "sensitive provider body", 500)
						case "malformed":
							_, _ = io.WriteString(w, "{")
						case "incomplete":
							writeJSON(w, 200, map[string]any{})
						}
						return true
					}
					<-r.Context().Done()
					return true
				})
				done := make(chan error, 1)
				go func() { done <- read(ctx) }()
				awaitProviderRead(t, started)
				awaitProviderRead(t, started)
				if failure == "cancel" {
					cancel()
				} else {
					close(fail)
				}
				if err := awaitProviderRead(t, done); err == nil || strings.Contains(err.Error(), "sensitive") {
					t.Fatalf("failure accepted or leaked provider body: %v", err)
				}
				joined := make(chan struct{})
				go func() { handlers.Wait(); close(joined) }()
				awaitProviderRead(t, joined)
				if calls.Load() != 2 {
					t.Fatalf("read failure started additional queued work: %d", calls.Load())
				}
				connections.assertClosed(t)
			})
		}
	}
}
