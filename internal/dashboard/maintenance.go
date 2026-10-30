package dashboard

import (
	"context"
	"time"
)

// Readiness is an on-demand observation only. Jobs always run their own fresh
// capability and identity preflight, never consume this display result.
type MaintenanceReadiness struct {
	Key          string    `json:"key"`
	Identity     string    `json:"identity"`
	Fingerprint  string    `json:"fingerprint"`
	ObservedAt   time.Time `json:"observedAt"`
	Connected    bool      `json:"connected"`
	Distribution string    `json:"distribution"`
	Version      string    `json:"version"`
	Source       string    `json:"source"`
	AppUpdate    bool      `json:"appUpdate"`
	OSUpdate     bool      `json:"osUpdate"`
	Reason       string    `json:"reason"`
	AppReason    string    `json:"appReason"`
	OSReason     string    `json:"osReason"`
}

func osSupported(d string) bool { return d == "debian" || d == "ubuntu" || d == "alpine" }

func (s *Service) Maintenance(ctx context.Context, hostID string, vmid int) (MaintenanceReadiness, error) {
	st := s.Store.Read()
	h, ok := st.Config.Host(hostID)
	if !ok {
		return MaintenanceReadiness{}, bad("unknown host")
	}
	inv := s.Providers.Inventory(ctx, st.Config)
	c, err := ensureContainer(inv, h, vmid)
	if err != nil {
		return MaintenanceReadiness{}, err
	}
	r := MaintenanceReadiness{Key: c.Key, Identity: c.Identity, Fingerprint: c.Fingerprint, ObservedAt: time.Now().UTC()}
	switch {
	case !h.SSH.Enabled && !st.Demo:
		r.Reason = "SSH is disabled. Configure a dedicated restricted key, verified host key and TCP 22 access in Settings."
	case c.Status != "running":
		r.Reason = "Start the container in Proxmox before checking maintenance."
	default:
		checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		result, e := s.Providers.Run(checkCtx, h, vmid, "capabilities")
		if e != nil {
			r.Reason = "SSH check failed. Verify TCP reachability, dedicated key permissions, pinned host key and the forced-command wrapper."
		} else {
			latest := s.Providers.Inventory(checkCtx, st.Config)
			fresh, e := ensureContainer(latest, h, vmid)
			if e != nil || fresh.Fingerprint != c.Fingerprint || fresh.Identity != c.Identity {
				return r, conflict("container changed during maintenance check; refresh")
			}
			if fingerprint(st.Config) != fingerprint(s.Store.Read().Config) {
				return r, conflict("settings changed during maintenance check")
			}
			r.Connected, r.Distribution, r.Version, r.Source = true, result.Distribution, result.Version, "ssh"
			if st.Demo {
				r.Source = "demo"
			}
			r.AppUpdate, r.OSUpdate = result.AppUpdate, osSupported(result.Distribution)
			if !r.AppUpdate {
				r.AppReason = "No executable /usr/bin/update exists in this container. App update is unavailable."
			}
			if !r.OSUpdate {
				r.OSReason = "OS updates support Debian, Ubuntu and Alpine only."
			}
		}
	}
	return r, nil
}

func (s *Service) freshJobCapability(ctx context.Context, h Host, vmid int, kind string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	r, err := s.Providers.Run(ctx, h, vmid, "capabilities")
	if err != nil {
		return conflict("maintenance connection check failed; verify restricted SSH setup")
	}
	if kind == "appUpdate" && !r.AppUpdate {
		return conflict("no executable /usr/bin/update exists in this container")
	}
	if kind == "osUpdate" && !osSupported(r.Distribution) {
		return conflict("OS updates support Debian, Ubuntu and Alpine only")
	}
	return nil
}
