# Container Proxy

A self-hosted Proxmox and OPNsense dashboard. Runs as one Go binary with its web UI embedded; Node and Go are only needed to build it.

- Sidebar navigation with card and row inventory views, shared filtering and container details.
- Running and stopped LXC containers, distribution information and inferred application identity.
- Smallest free VMID in each host's range, counting cluster-wide LXC and VM occupancy.
- KEA DHCPv4 reservations and Caddy reverse proxies, private to configured LAN/VPN networks by default.
- Reviewed per-container upstream ports, public/private changes, and cleanup of verified dashboard-owned records.
- Explicit app/OS update jobs through Proxmox SSH, with durable progress and recovery state.
- Multiple Proxmox hosts linked N:1 to multiple OPNsense configurations.
- OIDC sign-in and/or an administrator bearer token. System light/dark appearance.

## Build and try the isolated demo

Build prerequisites: Go 1.26+, Node 24.21+, pnpm 11.24, and Make. From the repository root:

```sh
make deps
make demo
```

The application binds port 8080 on all interfaces. Open `http://<dashboard-LAN-IP>:8080`. Demo mode is explicitly labeled, bypasses authentication, uses separate state in `.local/demo`, and never contacts real Proxmox, OPNsense, or SSH hosts. Stop it with Ctrl-C. Do not expose demo mode publicly.

The example fleet shows main proxmox containers 603, 604, 606, 607, 608 and pvedell containers 701, 702, 704. QEMU guests occupy main's 601 and 602, so the next VMIDs are 605 and 703. Records are illustrative. Demo settings edits persist until you intentionally choose a new empty demo state directory.

```sh
make check
make linux-amd64
```

`make check` builds the frontend, typechecks/lints it, runs frontend and Go tests, and runs Go vet. Browser checks are separate; see [frontend verification](docs/frontend.md). `make linux-amd64` produces `bin/container-proxy-linux-amd64`; `make linux-arm64` targets ARM64. Both builds disable CGO and embed the built UI. Build assets before invoking `go test ./...` directly. Generated binaries and frontend assets stay outside version control.

GitHub Actions runs these checks and the standalone browser suite on pull requests and `main`. Successful runs upload `container-proxy-linux-<commit>` with AMD64/ARM64 archives, installation files, `SHA256SUMS`, `SOURCE_COMMIT` and `BUILD_TOOLCHAIN`. Artifacts are retained for 14 days. For deployment, download the successful `main` run for the intended merged commit, verify `SOURCE_COMMIT` and run `sha256sum -c SHA256SUMS` before extracting the matching archive. The archives preserve the `bin/` and `deploy/` layout used below. CI does not automatically deploy to the homelab.

## Install inside a Debian LXC

Use a Debian 12 or 13 LXC with systemd and outbound access to your Proxmox and OPNsense management endpoints. The dashboard needs no privileged container, Docker, or Node runtime. The following commands run **inside that LXC as root**. First copy the matching Linux binary from `bin/` and the repository's `deploy/` files into the current directory, preserving the `bin` and `deploy` directories.

```sh
apt-get update
apt-get install -y ca-certificates openssh-client openssl
useradd --system --user-group --home-dir /var/lib/container-proxy --no-create-home --shell /usr/sbin/nologin container-proxy
install -d -m 0700 -o container-proxy -g container-proxy /var/lib/container-proxy /etc/container-proxy
install -m 0755 bin/container-proxy-linux-amd64 /usr/local/bin/container-proxy
install -m 0644 deploy/container-proxy.service /etc/systemd/system/container-proxy.service
install -m 0600 deploy/dashboard.env.example /etc/container-proxy/dashboard.env
openssl rand -hex 32 > /etc/container-proxy/admin-token
chown container-proxy:container-proxy /etc/container-proxy/admin-token
chmod 0600 /etc/container-proxy/admin-token
```

Run the account creation once; reuse the existing account during binary upgrades. For ARM64, substitute the ARM64 binary. Edit `/etc/container-proxy/dashboard.env`, set the exact HTTPS origin, then configure your existing reverse proxy to forward that hostname to the dashboard LXC on port 8080. Keep dashboard access restricted to LAN/VPN. The HTTP backend relies on that HTTPS proxy for transport security; never send the token over untrusted HTTP.

```sh
systemctl daemon-reload
systemctl enable --now container-proxy
systemctl status container-proxy --no-pager
journalctl -u container-proxy -n 50 --no-pager
```

Expected: the service remains active, `/healthz` returns `{"status":"ok"}`, and opening the HTTPS dashboard offers authentication. Read the token file privately to use the administrator-token sign-in; do not paste it into tickets or chat. OIDC setup can coexist with the token: configure issuer, client registration, secret file, exact callback URL, and explicit administrator subject and/or verified-email allowlist in the environment file. Restart the service after changing environment configuration. OIDC sessions expire and are cleared on server restart.

## Connect the providers

1. Add an OPNsense configuration in Settings. Supply its API key/secret, verified HTTPS address and domain base. Fetch its existing Caddy access lists and select the restricted LAN/VPN list for private routes, or explicitly configure CIDRs for a dashboard-managed list. Existing selected lists remain shared and are never modified or deleted by the dashboard. Native remote-IP and client-IP policies are supported; client-IP policies retain Caddy's existing proxy trust and header configuration. Routes use Caddy's existing automatic HTTPS configuration; no certificate reference is required. Install/enable KEA and `os-caddy` beforehand; DNS, global TLS settings, listening ports and global firewall rules remain managed in OPNsense. A connection test reports missing prerequisites; saving a record does not prove readiness.
2. Add each Proxmox host, choose its node and address-range profile, and link its OPNsense configuration. The dashboard discovers KEA IPv4 subnets and matches each container's intended address to its subnet automatically. Main's `.6.xx` and pvedell's `.7.xx` can use different VLAN subnets on the same firewall; no KEA subnet UUID is required in setup. Missing or ambiguous matches block reservation changes with an explanation. Configure trusted CA PEM where the appliance certificate is not publicly trusted. The app does not offer an insecure TLS bypass.
3. Give the Proxmox API token propagated cluster-wide `VM.Audit` at `/`, covering all nodes and guests. Permission-filtered inventory cannot establish a free VMID or safe removal. Inventory remains read-only; the app does not create, start, stop or delete containers.
4. Test the saved connections, then sync. Default profiles map main 601-699 to `192.168.6.0/24` and pvedell 701-799 to `192.168.7.0/24`. MAC suffixes are decimal text: VMID 612 maps to `d0:cc:37:00:06:12` and `192.168.6.12`, not hexadecimal conversion.
5. Open a container, select that app's upstream scheme and port, review its network plan, then apply. Scheme and port are per-app settings, so applications on the same firewall can use different upstreams. New routes use the private LAN/VPN allowlist. Public exposure requires an explicit reviewed change. A successful saved record is not enough; the app also checks the service-application result.

### Container identity prerequisite

Inventory is available without enrollment. Before a real container can receive network or update actions, the app requires a trustworthy creation-generation identifier. Current Proxmox LXC schemas do not guarantee one. For these containers, generate a UUID (`cat /proc/sys/kernel/random/uuid` inside Linux), then add `container-dashboard-generation=<UUID>` to that container's Proxmox description. Preserve the rest of the description. Generate a new value for every recreated or cloned container; never copy a generation marker. The app does not modify Proxmox descriptions automatically.

This one-time prerequisite prevents a recycled VMID from inheriting another container's actions or ownership. Missing or ambiguous identity is displayed as a blocked action, not silently bypassed. Node/rootfs changes and migrations need inspection and deliberate reenrollment. See [provider limitations and recovery](docs/provider-notes.md).

### Optional SSH maintenance

The Go binary includes the web UI, but container updates additionally need the OS `ssh` client. Provision a private key and verified `known_hosts` file readable only by the `container-proxy` service account, for example under `/etc/container-proxy/ssh/`. Verify each Proxmox host key fingerprint through an independent trusted channel before adding it; do not blindly trust `ssh-keyscan` output. Set the server-side file paths, host, user and port in the host's Settings.

The remote account must be allowed to execute `pct exec` for the intended containers; a restricted SSH forced-command policy is preferable to an unrestricted administrator key. Test your chosen restrictions manually. App update runs the container's existing `/usr/bin/update`; it may itself fetch its configured community-scripts updater. Interactive-only updaters can fail in this noninteractive path. OS update supports Debian/Ubuntu `apt-get update` plus `apt-get -y upgrade`, and Alpine `apk update` plus `apk upgrade`. No automatic start, reboot, or package-manager fallback is performed. Remote output is withheld to avoid leaking secrets; Activity exposes bounded execution phases and sanitized results.

## Mismatches, state and recovery

Refresh reads both running and stopped containers. Only explicitly requested, freshly reviewed plans mutate network configuration. Cleanup requires the container's VMID to be absent from complete observed inventory and each affected resource UUID/fingerprint to match durable dashboard ownership. Unrelated existing reservations/routes are conflicts and are never automatically adopted or deleted.

Unknown reads do not become orphans. Partial/interrupted changes remain visible and block conflicting operations. Supported recovery can repeat service application after verified complete resource writes; uncertain creates/deletes require manual inspection of the provider and preserved state. Updates that time out or are interrupted require inspection and explicit acknowledgement before another request. No remote outcome is inferred from a lost HTTP response.

The state directory stores configuration credentials, ownership, plans and job history in a private atomic JSON file with a single-process lock. Back up the complete directory securely; **do not discard it to resolve an error**, because that loses ownership and recovery evidence. Credentials are permission-protected, not encrypted at rest; use encrypted disks/backups as appropriate. One instance must own a given state directory. Demo/live modes refuse to share state.

For a binary upgrade, stop the service, back up its state, replace only the binary, and start the service again. Preserve environment files, SSH keys, known-hosts and state.

## Implementation and acceptance

- [API contract](docs/backend-api.md)
- [Provider sources, exact shapes and limitations](docs/provider-notes.md)
- [Confirmed interface brief](docs/dashboard-brief.md)

Local fixture/demo checks are distinct from authentic provider testing. Real Proxmox/OPNsense versions, private/public reachability, TLS/DNS, SSH execution, LXC service startup and your actual OIDC issuer require acceptance in your environment. This initial implementation does not deploy or change your infrastructure.
