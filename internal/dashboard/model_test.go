package dashboard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecimalProfilesAndHoles(t *testing.T) {
	h := DefaultProfiles()[0]
	mac, ip, e := DeriveAddress(h, 612)
	if e != nil || mac != "d0:cc:37:00:06:12" || ip != "192.168.6.12" {
		t.Fatalf("%s %s %v", mac, ip, e)
	}
	h2 := DefaultProfiles()[1]
	mac, ip, e = DeriveAddress(h2, 799)
	if e != nil || mac != "d0:cc:37:00:07:99" || ip != "192.168.7.99" {
		t.Fatalf("%s %s %v", mac, ip, e)
	}
	for _, change := range []func(*Host){func(h *Host) { h.Network = "192.168.7.0/24" }, func(h *Host) { h.MACPrefix = "d0:cc:37:00:07" }, func(h *Host) { h.MACPrefix = "invalid" }} {
		badHost := h
		change(&badHost)
		if _, _, e = DeriveAddress(badHost, 612); e == nil {
			t.Fatal("accepted mismatched profile")
		}
	}
	guests := []Guest{{601, "lxc", "a"}, {602, "qemu", "b"}, {604, "lxc", "a"}}
	next, e := NextVMID(h, guests, true)
	if e != nil || next == nil || *next != 603 {
		t.Fatalf("hole %v %v", next, e)
	}
	if next, e = NextVMID(h, guests, false); e == nil || next != nil {
		t.Fatal("claimed a free VMID after failed inventory")
	}
}
func TestStoreSecurityRecoveryAndMode(t *testing.T) {
	dir := t.TempDir()
	s, e := OpenStore(dir, false)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = OpenStore(dir, false); e == nil {
		t.Fatal("allowed concurrent state owner")
	}
	if e = s.Update(func(st *State) error {
		st.Jobs["job"] = Job{ID: "job", Status: "running"}
		st.Operations["op"] = Operation{ID: "op", Status: "running", ManualRecovery: true}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	s.Close()
	for path, want := range map[string]os.FileMode{dir: 0700, filepath.Join(dir, "state.json"): 0600} {
		info, e := os.Stat(path)
		if e != nil || info.Mode().Perm() != want {
			t.Fatalf("permissions %s %v %v", path, info, e)
		}
	}
	s, e = OpenStore(dir, false)
	if e != nil {
		t.Fatal(e)
	}
	st := s.Read()
	if st.Jobs["job"].Status != "interrupted" || st.Operations["op"].Status != "interrupted" || !st.Operations["op"].ManualRecovery {
		t.Fatal("lost uncertain state")
	}
	s.Close()
	if _, e = OpenStore(dir, true); e == nil {
		t.Fatal("accepted live state as demo")
	}
	if e = os.WriteFile(filepath.Join(dir, "state.json"), []byte("{"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = OpenStore(dir, false); e == nil {
		t.Fatal("accepted corrupt state")
	}
}
func TestSettingsSecretEdits(t *testing.T) {
	s, _, store := demoService(t)
	st := store.Read()
	st.Config.Hosts[0].TokenSecret = "host-secret"
	st.Config.OPNsenses[0].APISecret = "api-secret"
	st.Config.OPNsenses[0].APIKey = "api-key"
	if e := store.Update(func(next *State) error { next.Config = st.Config; return nil }); e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(s.Settings())
	if strings.Contains(string(b), "host-secret") || strings.Contains(string(b), "api-secret") || strings.Contains(string(b), "api-key") {
		t.Fatalf("credential leak: %s", b)
	}
	var edit ConfigEdit
	if e := json.Unmarshal(b, &edit); e == nil { // Read flags are deliberately not accepted by strict HTTP decoding.
		edit.Revision = st.Config.Revision
		view, e := s.UpdateConfig(edit)
		if e != nil {
			t.Fatal(e)
		}
		current := store.Read()
		if current.Config.Hosts[0].TokenSecret != "host-secret" || current.Config.OPNsenses[0].APISecret != "api-secret" || current.Config.OPNsenses[0].APIKey != "api-key" {
			t.Fatal("omitted secret was lost")
		}
		empty := ""
		edit.Revision = view.Revision
		edit.OPNsenses[0].Secret = &empty
		edit.OPNsenses[0].APIKeyValue = &empty
		edit.Hosts[0].Secret = &empty
		if _, e = s.UpdateConfig(edit); e != nil {
			t.Fatal(e)
		}
		current = store.Read()
		if current.Config.Hosts[0].TokenSecret != "" || current.Config.OPNsenses[0].APISecret != "" || current.Config.OPNsenses[0].APIKey != "" {
			t.Fatal("intentional clear failed")
		}
	} else {
		t.Fatal(e)
	}
}
