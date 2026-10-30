package dashboard

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func trustServer(s *httptest.Server) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw}))
}
func TestProxmoxRealShapesStoppedAndFailures(t *testing.T) {
	fail := false
	interfaces := 0
	diskSize, volume := "8G", "-disk-0"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "PVEAPIToken=dashboard@pve!test=secret" {
			t.Error("wrong auth")
		}
		switch r.URL.Path {
		case "/api2/json/access/permissions":
			if r.URL.Query().Get("path") != "/" {
				t.Error("missing permissions path")
			}
			writeJSON(w, 200, map[string]any{"data": map[string]any{"/": map[string]int{"VM.Audit": 1}}})
		case "/api2/json/nodes":
			writeJSON(w, 200, map[string]any{"data": []any{map[string]any{"node": "pve", "status": "online"}}})
		case "/api2/json/cluster/status":
			writeJSON(w, 200, map[string]any{"data": []any{map[string]any{"type": "cluster", "quorate": 1}}})
		case "/api2/json/cluster/resources":
			if fail {
				http.Error(w, "sensitive upstream body", 500)
				return
			}
			if r.URL.Query().Get("type") != "vm" {
				t.Error("wrong resource type")
			}
			writeJSON(w, 200, map[string]any{"data": []any{map[string]any{"vmid": 601, "type": "qemu", "node": "other", "status": "stopped", "template": 1}, map[string]any{"vmid": 612, "type": "lxc", "node": "pve", "name": "jellyfin", "status": "stopped"}, map[string]any{"vmid": 602, "type": "lxc", "node": "pve", "name": "grafana", "status": "running"}}})
		case "/api2/json/nodes/pve/lxc/612/config", "/api2/json/nodes/pve/lxc/602/config":
			id := "612"
			suffix := "12"
			if strings.Contains(r.URL.Path, "602") {
				id = "602"
				suffix = "02"
			}
			writeJSON(w, 200, map[string]any{"data": map[string]any{"hostname": "app-" + id, "ostype": "debian", "digest": "digest-" + id + diskSize + volume, "net0": "name=eth0,hwaddr=D0:CC:37:00:06:" + suffix + ",ip=192.168.6." + suffix + "/24", "rootfs": "local:vm-" + id + volume + ",size=" + diskSize, "description": "container-dashboard-generation=12345678-1111-4111-8111-123456789" + id}})
		case "/api2/json/nodes/pve/lxc/602/interfaces":
			interfaces++
			writeJSON(w, 200, map[string]any{"data": []any{map[string]any{"name": "eth0", "hwaddr": "d0:cc:37:00:06:02", "inet": "192.168.6.2/24"}}})
		default:
			t.Errorf("unexpected endpoint %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	h := DefaultProfiles()[0]
	h.BaseURL = srv.URL
	h.CAPEM = trustServer(srv)
	h.TokenID = "dashboard@pve!test"
	h.TokenSecret = "secret"
	h.Node = "pve"
	p := &RealProviders{}
	inv := p.Inventory(context.Background(), Config{Hosts: []Host{h}})
	if !inv.Complete || len(inv.Containers) != 2 || inv.Hosts[0].NextVMID == nil || *inv.Hosts[0].NextVMID != 603 {
		t.Fatalf("inventory %+v", inv)
	}
	if interfaces != 1 {
		t.Fatal("queried stopped-container interfaces")
	}
	ct, _ := inv.Find("main:612")
	if ct.Status != "stopped" || ct.Version != "" || ct.Identity == "" {
		t.Fatalf("stopped/distro/identity %+v", ct)
	}
	diskSize = "16G"
	resized, _ := p.Inventory(context.Background(), Config{Hosts: []Host{h}}).Find("main:612")
	if resized.Identity != ct.Identity || resized.Fingerprint == ct.Fingerprint {
		t.Fatal("resize must preserve generation and invalidate stale plans", ct, resized)
	}
	volume = "-disk-1"
	replaced, _ := p.Inventory(context.Background(), Config{Hosts: []Host{h}}).Find("main:612")
	if replaced.Identity == ct.Identity {
		t.Fatal("different root volume must change identity")
	}
	fail = true
	inv = p.Inventory(context.Background(), Config{Hosts: []Host{h}})
	if inv.Complete || inv.Hosts[0].NextVMID != nil || strings.Contains(inv.Hosts[0].Error, "sensitive") {
		t.Fatalf("failure handling %+v", inv)
	}
	h.CAPEM = ""
	inv = p.Inventory(context.Background(), Config{Hosts: []Host{h}})
	if inv.Complete {
		t.Fatal("silently accepted untrusted TLS")
	}
}
func TestOPNsenseEndpointShapesAndIncompleteSnapshot(t *testing.T) {
	id := "12345678-1111-4111-8111-123456789012"
	applyFail := false
	incomplete := false
	var created map[string]any
	fields := map[string]any{"subnet": map[string]any{id: map[string]any{"value": "192.168.6.0/24", "selected": 1}}, "hw_address": "d0:cc:37:00:06:12", "ip_address": "192.168.6.12", "hostname": "jellyfin", "description": "owned"}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "api-key" || pass != "api-secret" {
			t.Error("wrong basic auth")
		}
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/search_"):
			total := 0
			rows := []any{}
			if strings.HasSuffix(path, "search_reservation") {
				total = 1
				rows = append(rows, map[string]any{"uuid": id, "subnet": "translated display"})
			}
			if incomplete {
				total++
			}
			if r.Method != "GET" || r.URL.Query().Get("rowCount") != "-1" {
				t.Error("wrong pagination")
			}
			writeJSON(w, 200, map[string]any{"total": total, "rows": rows})
		case strings.HasSuffix(path, "get_reservation/"+id):
			writeJSON(w, 200, map[string]any{"reservation": fields})
		case path == "/api/caddy/general/get":
			writeJSON(w, 200, map[string]any{"caddy": map[string]any{"general": map[string]any{"enabled": "1", "accesslist": ""}}})
		case path == "/api/kea/dhcpv4/get":
			writeJSON(w, 200, map[string]any{"dhcpv4": map[string]any{"general": map[string]any{"enabled": "1"}}})
		case strings.Contains(path, "get_subnet/"):
			writeJSON(w, 200, map[string]any{"subnet4": map[string]any{"subnet": "192.168.6.0/24"}})
		case strings.HasSuffix(path, "/status"):
			writeJSON(w, 200, map[string]any{"status": "running"})
		case path == "/api/caddy/reverse_proxy/get_reverse_proxy":
			writeJSON(w, 200, map[string]any{"reverse": map[string]any{"CustomCertificate": map[string]any{"cert": map[string]any{"value": "existing cert", "selected": 0}}}})
		case path == "/api/kea/dhcpv4/add_reservation":
			if r.Method != "POST" {
				t.Error("wrong create method")
			}
			_ = json.NewDecoder(r.Body).Decode(&created)
			writeJSON(w, 200, map[string]string{"result": "saved", "uuid": id})
		case path == "/api/kea/service/reconfigure":
			if applyFail {
				writeJSON(w, 200, map[string]string{"status": "failed", "status_msg": "sensitive"})
			} else {
				writeJSON(w, 200, map[string]string{"status": "ok"})
			}
		default:
			t.Errorf("unexpected %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	o := OPNsense{BaseURL: srv.URL, CAPEM: trustServer(srv), APIKey: "api-key", APISecret: "api-secret", SubnetID: id, CertificateID: "cert", DomainReady: true}
	p := &RealProviders{}
	snap, e := p.Snapshot(context.Background(), o)
	if e != nil || !snap.Ready || len(snap.Resources) != 2 || snap.Resources[1].Fields["subnet"] != id {
		t.Fatalf("snapshot %+v %v", snap, e)
	}
	r, e := p.Create(context.Background(), o, "reservation", map[string]string{"hw_address": "d0:cc:37:00:06:12", "subnet": id, "ip_address": "192.168.6.12", "hostname": "jellyfin"})
	if e != nil || r.ID != id {
		t.Fatal(e)
	}
	nested, ok := created["reservation"].(map[string]any)
	if !ok || nested["hw_address"] != "d0:cc:37:00:06:12" || nested["subnet"] != id {
		t.Fatalf("wrong request body %+v", created)
	}
	applyFail = true
	if e = p.Apply(context.Background(), o, "kea"); e == nil || strings.Contains(e.Error(), "sensitive") {
		t.Fatal("reconfigure failure hidden or leaked")
	}
	incomplete = true
	if _, e = p.Snapshot(context.Background(), o); e == nil {
		t.Fatal("accepted incomplete resources")
	}
}
func TestSSHFixedCommandsAndOSRelease(t *testing.T) {
	cmd, e := fixedCommand(612, "appUpdate")
	if e != nil || cmd != "pct exec 612 -- /usr/bin/update" {
		t.Fatalf("%s %v", cmd, e)
	}
	for _, kind := range []string{"curl attacker", "reboot", "start"} {
		if _, e = fixedCommand(612, kind); e == nil {
			t.Fatal("accepted arbitrary operation")
		}
	}
	if _, e = fixedCommand(-1, "inspect"); e == nil {
		t.Fatal("accepted invalid VMID")
	}
	h := SSHConfig{Host: "host.example", Port: 22, User: "root", PrivateKeyFile: "key", KnownHostsFile: "known"}
	args := strings.Join(sshArgs(h, cmd), " ")
	if !strings.Contains(args, "StrictHostKeyChecking=yes") || strings.Contains(args, "accept-new") {
		t.Fatal(args)
	}
	os, e := parseOSRelease("ID=debian\nVERSION_ID=\"13\"\n")
	if e != nil || os.Distribution != "debian" || os.Version != "13" {
		t.Fatal(os, e)
	}
}

func TestCaddyWriteWrappersAndFieldNames(t *testing.T) {
	id := "12345678-1111-4111-8111-123456789012"
	saved := map[string]map[string]string{}
	wrappers := map[string]string{"access_list": "accesslist", "reverse_proxy": "reverse", "handle": "handle"}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/caddy/reverse_proxy/"), "/")
		command := parts[0]
		if strings.HasPrefix(command, "add_") {
			kind := strings.TrimPrefix(command, "add_")
			wrapper := wrappers[kind]
			var body map[string]map[string]string
			if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 1 || body[wrapper] == nil {
				t.Errorf("wrong wrapper for %s: %+v", command, body)
			}
			saved[kind] = body[wrapper]
			writeJSON(w, 200, map[string]string{"result": "saved", "uuid": id})
			return
		}
		if strings.HasPrefix(command, "get_") {
			kind := strings.TrimPrefix(command, "get_")
			writeJSON(w, 200, map[string]any{wrappers[kind]: saved[kind]})
			return
		}
		t.Errorf("unexpected endpoint %s", r.URL)
	}))
	defer srv.Close()
	o := OPNsense{BaseURL: srv.URL, CAPEM: trustServer(srv), APIKey: "key", APISecret: "secret"}
	p := &RealProviders{}
	cases := map[string]map[string]string{"acl": {"accesslistName": "private", "clientIps": "192.168.6.0/24,fd00::/64", "accesslistInvert": "0", "RequestMatcher": "remote_ip", "HttpResponseCode": "403"}, "domain": {"enabled": "0", "FromDomain": "grafana.went.io", "accesslist": id, "CustomCertificate": "cert", "DisableTls": "0"}, "handle": {"enabled": "1", "reverse": id, "HandleType": "handle", "HandleDirective": "reverse_proxy", "ToDomain": "192.168.6.12", "ToPort": "8080", "HttpTls": "0", "HttpTlsInsecureSkipVerify": "0"}}
	for kind, fields := range cases {
		r, e := p.Create(context.Background(), o, kind, fields)
		if e != nil || fingerprint(r.Fields) != fingerprint(fields) {
			t.Fatal(kind, r, e)
		}
	}
}
