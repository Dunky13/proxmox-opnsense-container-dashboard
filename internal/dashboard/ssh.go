package dashboard

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

type SSHRunner interface {
	Run(context.Context, Host, int, string) (RunResult, error)
}
type OpenSSH struct{}

const rebootMarker = "DASHBOARD_REBOOT_REQUIRED"

var distroID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var distroVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_-]{0,63}$`)

// Retain only enough output to recognize a marker split across writes. Package
// output may be arbitrarily verbose and is never retained or returned.
type rebootOutput struct {
	tail  string
	found bool
}

func (out *rebootOutput) Write(p []byte) (int, error) {
	text := out.tail + string(p)
	out.found = out.found || strings.Contains(text, rebootMarker)
	if len(text) >= len(rebootMarker) {
		text = text[len(text)-len(rebootMarker)+1:]
	}
	out.tail = text
	return len(p), nil
}

type boundedOutput struct {
	mu    sync.Mutex
	b     bytes.Buffer
	limit int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if b.b.Len() < b.limit {
		take := b.limit - b.b.Len()
		if len(p) > take {
			p = p[:take]
		}
		b.b.Write(p)
	}
	return n, nil
}
func sshArgs(s SSHConfig, command string) []string {
	return []string{"-F", "/dev/null", "-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + s.KnownHostsFile, "-o", "IdentitiesOnly=yes", "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2", "-p", strconv.Itoa(s.Port), "-i", s.PrivateKeyFile, s.User + "@" + s.Host, command}
}
func fixedCommand(id int, kind string) (string, error) {
	if id < 100 || id > 999999999 {
		return "", bad("invalid VMID")
	}
	prefix := fmt.Sprintf("pct exec %d -- ", id)
	switch kind {
	case "inspect":
		return prefix + "cat /etc/os-release", nil
	case "capabilities":
		return prefix + `/bin/sh -c 'set -eu; cat /etc/os-release; if test -x /usr/bin/update; then printf "\nDASHBOARD_APP_UPDATE=1\n"; else printf "\nDASHBOARD_APP_UPDATE=0\n"; fi'`, nil
	case "appUpdate":
		return prefix + "/usr/bin/update", nil
	case "osUpdate":
		// Fixed program only. No caller data is interpolated into shell syntax.
		return prefix + `/bin/sh -c 'set -eu; . /etc/os-release; case "$ID" in debian|ubuntu) export DEBIAN_FRONTEND=noninteractive; apt-get update; apt-get -y upgrade ;; alpine) apk update; apk upgrade ;; *) exit 64 ;; esac; if [ -f /var/run/reboot-required ]; then printf "DASHBOARD_REBOOT_REQUIRED\n"; fi'`, nil
	}
	return "", bad("unsupported SSH operation")
}
func (OpenSSH) Run(ctx context.Context, h Host, id int, kind string) (RunResult, error) {
	if !h.SSH.Enabled {
		return RunResult{}, conflict("SSH is not enabled for this host")
	}
	if e := validateSSH(h.SSH); e != nil {
		return RunResult{}, e
	}
	command, e := fixedCommand(id, kind)
	if e != nil {
		return RunResult{}, e
	}
	cmd := exec.CommandContext(ctx, "ssh", sshArgs(h.SSH, command)...)
	cmd.Stdin = nil
	cmd.WaitDelay = 2e9
	out := &boundedOutput{limit: 32768}
	reboot := &rebootOutput{}
	if kind == "inspect" || kind == "capabilities" {
		cmd.Stdout = out
	} else if kind == "osUpdate" {
		cmd.Stdout = reboot
	} else {
		cmd.Stdout = io.Discard
	}
	cmd.Stderr = io.Discard
	if e = cmd.Run(); e != nil {
		if ctx.Err() != nil {
			return RunResult{}, ctx.Err()
		}
		return RunResult{}, unavailable("SSH operation failed; remote outcome may require inspection")
	}
	if kind == "inspect" {
		return parseOSRelease(out.b.String())
	}
	if kind == "capabilities" {
		return parseCapabilities(out.b.String())
	}
	return RunResult{RebootRequired: reboot.found}, nil
}

func parseCapabilities(raw string) (RunResult, error) {
	r, err := parseOSRelease(raw)
	if err != nil {
		return r, err
	}
	seen := 0
	for _, line := range strings.Split(raw, "\n") {
		if line == "DASHBOARD_APP_UPDATE=1" || line == "DASHBOARD_APP_UPDATE=0" {
			seen++
			r.AppUpdate = line == "DASHBOARD_APP_UPDATE=1"
		}
	}
	if seen != 1 {
		return RunResult{}, unavailable("SSH capability response is incomplete")
	}
	return r, nil
}
func parseOSRelease(raw string) (RunResult, error) {
	result := RunResult{}
	for _, line := range strings.Split(raw, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, "\"'")
		if len(v) > 100 {
			continue
		}
		switch k {
		case "ID":
			result.Distribution = v
		case "VERSION_ID":
			result.Version = v
		}
	}
	if !distroID.MatchString(result.Distribution) || (result.Version != "" && !distroVersion.MatchString(result.Version)) {
		return result, unavailable("container distribution is unavailable")
	}
	return result, nil
}
