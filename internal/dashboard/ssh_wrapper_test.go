package dashboard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestrictedSSHWrapperExactAllowlist(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/container-proxy-ssh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, "pct")
	wrapper := filepath.Join(dir, "wrapper")
	if err = os.WriteFile(stub, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	// Test-only copy replaces the privileged executable and UID check. The
	// shipped wrapper has no overridable binary, PATH or bypass variable.
	text := strings.ReplaceAll(string(raw), "/usr/sbin/pct", stub)
	text = strings.ReplaceAll(text, `[ "$(id -u)" = 0 ] || reject`, ":")
	if err = os.WriteFile(wrapper, []byte(text), 0700); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"inspect", "capabilities", "appUpdate", "osUpdate"} {
		command, err := fixedCommand(609, kind)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("/bin/sh", wrapper)
		cmd.Env = []string{"SSH_ORIGINAL_COMMAND=" + command}
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.HasPrefix(string(out), "exec\n609\n--\n") {
			t.Fatalf("%s: %s %v", kind, out, err)
		}
	}
	for _, command := range []string{"", "sh", "pct exec 0609 -- /usr/bin/update", "pct exec -609 -- /usr/bin/update", "pct exec 99 -- /usr/bin/update", "pct exec 1000000000 -- /usr/bin/update", "pct exec 609 -- /bin/sh", "pct exec 609 -- /usr/bin/update; id", "pct exec 609 -- /usr/bin/update\n", "pct exec 609 -- cat /etc/shadow", "pct exec $(id) -- /usr/bin/update", "scp -t /tmp/payload"} {
		cmd := exec.Command("/bin/sh", wrapper)
		cmd.Env = []string{"SSH_ORIGINAL_COMMAND=" + command}
		out, err := cmd.CombinedOutput()
		if err == nil || strings.Contains(string(out), "exec\n") {
			t.Fatalf("accepted %q: %s", command, out)
		}
	}
	cmd := exec.Command("/bin/sh", wrapper)
	command, _ := fixedCommand(609, "inspect")
	cmd.Env = []string{"SSH_ORIGINAL_COMMAND=" + command, "SSH_TTY=/dev/pts/1"}
	if err = cmd.Run(); err == nil {
		t.Fatal("interactive terminal accepted")
	}
}
