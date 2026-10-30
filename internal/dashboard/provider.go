package dashboard

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type Providers interface {
	Inventory(context.Context, Config) Inventory
	Snapshot(context.Context, OPNsense) (ProviderSnapshot, error)
	AccessLists(context.Context, OPNsense) ([]Resource, error)
	CaddyPolicy(context.Context, OPNsense) (CaddyPolicySnapshot, error)
	Subnets(context.Context, OPNsense) ([]Resource, error)
	Create(context.Context, OPNsense, string, map[string]string) (Resource, error)
	Set(context.Context, OPNsense, Resource, map[string]string) (Resource, error)
	Delete(context.Context, OPNsense, Resource) error
	Apply(context.Context, OPNsense, string) error
	Run(context.Context, Host, int, string) (RunResult, error)
}
type RunResult struct {
	AppUpdate      bool
	Distribution   string
	Version        string
	RebootRequired bool
}
type RealProviders struct{ SSH SSHRunner }

type providerHTTPKey struct{ base, ca string }
type providerHTTPContextKey struct{}

// A scope belongs to one API request or standalone provider operation. Clients
// contain only transport policy; authentication is applied to each request.
// Separate addresses and trust revisions never share a transport.
type providerHTTPScope struct {
	mu      sync.Mutex
	clients map[providerHTTPKey]*http.Client
	closed  bool
}

func withProviderHTTP(ctx context.Context) (context.Context, func()) {
	if _, ok := ctx.Value(providerHTTPContextKey{}).(*providerHTTPScope); ok {
		return ctx, func() {}
	}
	scope := &providerHTTPScope{clients: make(map[providerHTTPKey]*http.Client)}
	return context.WithValue(ctx, providerHTTPContextKey{}, scope), scope.close
}

func (s *providerHTTPScope) client(base, ca string) (*http.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, unavailable("provider request scope is closed")
	}
	key := providerHTTPKey{strings.TrimRight(base, "/"), ca}
	if client := s.clients[key]; client != nil {
		return client, nil
	}
	client, err := newHTTP(ca)
	if err != nil {
		return nil, err
	}
	s.clients[key] = client
	return client, nil
}

func (s *providerHTTPScope) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, client := range s.clients {
		client.CloseIdleConnections()
	}
	s.clients = nil
	s.closed = true
}

func newHTTP(ca string) (*http.Client, error) {
	pool, e := x509.SystemCertPool()
	if e != nil {
		pool = x509.NewCertPool()
	}
	if ca != "" && !pool.AppendCertsFromPEM([]byte(ca)) {
		return nil, bad("invalid trusted CA PEM")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	tr.MaxConnsPerHost = 2
	tr.MaxIdleConnsPerHost = 2
	tr.MaxIdleConns = 2
	tr.ResponseHeaderTimeout = 15 * time.Second
	return &http.Client{Timeout: 20 * time.Second, Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
func providerJSON(ctx context.Context, base, ca, method, path string, body any, auth func(*http.Request), out any) error {
	ctx, closeHTTP := withProviderHTTP(ctx)
	defer closeHTTP()
	client, e := ctx.Value(providerHTTPContextKey{}).(*providerHTTPScope).client(base, ca)
	if e != nil {
		return e
	}
	var b io.Reader
	if body != nil {
		raw, e := json.Marshal(body)
		if e != nil {
			return e
		}
		b = bytes.NewReader(raw)
	}
	req, e := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, b)
	if e != nil {
		return unavailable("invalid provider request")
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	auth(req)
	res, e := client.Do(req)
	if e != nil {
		return unavailable("provider connection failed; verify address, trust and credentials")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return unavailable(fmt.Sprintf("provider returned HTTP %d; check permissions and prerequisites", res.StatusCode))
	}
	raw, e := io.ReadAll(io.LimitReader(res.Body, 8*1024*1024+1))
	if e != nil || len(raw) > 8*1024*1024 {
		return unavailable("provider response exceeded limits or was interrupted")
	}
	if e = json.Unmarshal(raw, out); e != nil {
		return unavailable("provider returned malformed JSON")
	}
	return nil
}
func sortInventory(i *Inventory) {
	sort.Slice(i.Containers, func(a, b int) bool { return i.Containers[a].Key < i.Containers[b].Key })
	for n := range i.Hosts {
		sort.Slice(i.Hosts[n].Guests, func(a, b int) bool { return i.Hosts[n].Guests[a].VMID < i.Hosts[n].Guests[b].VMID })
	}
	sort.Slice(i.Hosts, func(a, b int) bool { return i.Hosts[a].HostID < i.Hosts[b].HostID })
}
