# Container maintenance and manual cleanup

## Contract

This feature keeps the Go single binary and embedded frontend, multiple Proxmox
hosts linked to one or more OPNsense firewalls, sidebar, cards/rows, and the
15-second display observation reuse. Provider writes continue to require fresh
complete inventory, creation identity, current configuration, resource
fingerprints, dependency checks and single-use review plans.

Cleanup is **manual only**: discover, select, review, run. There is no schedule,
interval, background deletion worker, unattended repair or automatic exposure
change. Select one app's resource UUIDs, apply its reviewed plan, refresh, then
review the next app against the new state. Plans prepared before another change
are rejected explicitly; the application never retries a POST or silently
replans a confirmed selection.

Before enabling maintenance, provision restricted SSH access and verify its
capabilities from the dashboard service account. Application deployment and
runtime checks are separate from applying a production update, import or cleanup:
each operation requires its own explicit review. Fixtures and source contain no
real credentials or provider resource UUIDs.

## Maintenance readiness

Open a running container and choose **Check maintenance readiness**. This reads
fresh inventory and runs one fixed capability command through the host's saved
SSH configuration. It reports connection status, actual `/etc/os-release` distro
and version with `source: ssh`, executable `/usr/bin/update` presence, and OS
support (Debian, Ubuntu or Alpine). Absence of an app update script is an explicit
unavailable capability. No available-version claim is made. Connection failures
return setup guidance, never stderr, remote package output or credential values.
**Open maintenance settings** leads to the host's SSH configuration and setup
instructions. API inventory access alone does not grant SSH maintenance access.

Bulk inventory and plan inventory do not inspect every running container by SSH.
Their distro comes from Proxmox `ostype`, with an empty version when unobserved.
The on-demand response is retained only in the inspector for display, bound to
identity/fingerprint and limited to 60 seconds. Starting and executing a job each
perform an independent fresh capability check and fresh container preflight.
Demo capability results explicitly use `source: demo` and are labeled simulated; they are not SSH runtime proof. Network mismatch does not decide update availability. Existing queued/running,
uncertain, reboot-required, timeout and acknowledgement behavior is preserved.
Maintenance and network changes cannot overlap on the linked firewall; network
writes remain serialized. Update package output is discarded, retaining only the
existing reboot marker and sanitized phases in Activity. No reboot is issued.

## Dedicated restricted SSH bootstrap (parent/operator)

Run these commands only after reviewing the wrapper and approving provisioning.
The dashboard service runs as `container-proxy`, not root. Commands below target
Debian-based dashboard/Proxmox hosts, use POSIX shell, and assume an existing
trusted root console on each host. Replace example host/source values with the
approved values. Do not copy a personal private key or loosen global root SSH.
The wrapper requires root on Proxmox because `pct exec` requires that authority;
only the dedicated key receives the forced-command restrictions.

1. On the dashboard server, as root, create a **new key per Proxmox host**. Stop
   if the destination already exists; do not overwrite a key in use.

   ```sh
   set -eu
   install -d -o container-proxy -g container-proxy -m 0700 /etc/container-proxy/ssh
   test ! -e /etc/container-proxy/ssh/pve-main
   test ! -e /etc/container-proxy/ssh/pve-main.pub
   runuser -u container-proxy -- ssh-keygen -t ed25519 -N '' \
     -C container-proxy-main -f /etc/container-proxy/ssh/pve-main
   chmod 0600 /etc/container-proxy/ssh/pve-main
   ```

   Expected: a new private/public pair; the service user alone can read the
   private key. Transfer **only the public file** to the trusted Proxmox console.

2. On that Proxmox root console, from the reviewed source checkout, install the
   fixed wrapper. The file and all parent directories must remain root-owned
   and unwritable by other users. Do not install into a writable directory.

   ```sh
   set -eu
   command -v pct
   test -x /usr/sbin/pct
   /bin/sh -n deploy/container-proxy-ssh
   install -d -o root -g root -m 0755 /usr/local/libexec
   install -o root -g root -m 0755 deploy/container-proxy-ssh \
     /usr/local/libexec/container-proxy-ssh
   /usr/sbin/sshd -t
   /usr/sbin/sshd -T | grep '^permitrootlogin '
   ```

   Expected: `/usr/sbin/pct`, successful syntax/config checks, and an existing
   policy permitting forced public-key commands. If effective Match rules differ,
   inspect `sshd -T -C user=root,addr=192.0.2.70,host=dashboard.example.test` with
   the actual source. Do not change global SSH policy as a workaround.

3. Preserve the existing authorized keys. Append this **one dedicated key**,
   restricted to the dashboard's exact source address. The example uses a
   documentation address; substitute the approved source before running.
   `DEDICATED_PUBLIC_KEY_FILE` must be the transferred single-line public file.

   ```sh
   set -eu
   DASHBOARD_SOURCE=192.0.2.70
   DEDICATED_PUBLIC_KEY_FILE=/root/container-proxy-main.pub
   test "$(wc -l < "$DEDICATED_PUBLIC_KEY_FILE" | tr -d ' ')" = 1
   IFS=' ' read -r key_type key_data key_comment < "$DEDICATED_PUBLIC_KEY_FILE"
   test "$key_type" = ssh-ed25519
   test -n "$key_data"
   install -d -o root -g root -m 0700 /root/.ssh
   touch /root/.ssh/authorized_keys
   chmod 0600 /root/.ssh/authorized_keys
   printf '\nrestrict,from="%s",command="/usr/local/libexec/container-proxy-ssh" %s %s\n' \
     "$DASHBOARD_SOURCE" "$key_type" "$key_data" >> /root/.ssh/authorized_keys
   ```

   OpenSSH `restrict` disables forwarding, PTY allocation and user rc execution;
   `command` binds execution to the wrapper, including subsystem requests. The
   wrapper rejects shells, arbitrary commands, alternate paths, malformed VMIDs,
   argument changes, shell syntax and PTYs. It never evaluates caller command
   text. Its only accepted operations are the exact fixed `pct exec` inspection,
   capability, app update and supported OS update commands emitted by the Go
   runner. VMIDs must be canonical decimal 100–999999999. These commands can run
   existing container update scripts with their existing authority; review those
   scripts separately before requesting an update.

4. Pin the Proxmox host's **public** SSH key from its trusted console. Verify
   `/etc/ssh/ssh_host_ed25519_key.pub` and its fingerprint there, then transfer
   that public file through the approved trusted channel. On the dashboard root
   console, associate it with the exact saved SSH host name (or `[host]:port`
   for a nondefault port) and make the file readable by `container-proxy`.

   ```sh
   set -eu
   PVE_SSH_HOST=pve.example.test
   APPROVED_HOST_KEY_FILE=/root/approved-pve-host-key.pub
   printf '%s ' "$PVE_SSH_HOST" >> /etc/container-proxy/ssh/known_hosts
   cat "$APPROVED_HOST_KEY_FILE" >> /etc/container-proxy/ssh/known_hosts
   chown container-proxy:container-proxy /etc/container-proxy/ssh/known_hosts
   chmod 0600 /etc/container-proxy/ssh/known_hosts
   ```

   Never accept an unverified network scan as host-key authority. The runner
   always uses `StrictHostKeyChecking=yes`, a configured known-hosts file,
   `BatchMode=yes`, `IdentitiesOnly=yes`, and no ambient SSH config. The operator
   should separately confirm forwarding and a shell are denied for this key.

5. Parent provisions the narrowly scoped dashboard-source → Proxmox TCP 22 rule.
   Save SSH enabled, host, port 22, user root, the dedicated private-key path and
   known-hosts path in host Settings. This does not publish key material. Run the
   dashboard readiness check on the intended CT. Expected: actual distro/version
   and distinct app/OS capability results. A host without `/usr/bin/update` must
   continue to report app update unavailable. Readiness does not run an update.

The local wrapper dispatch tests use a disposable stub executable. They verify
all four exact commands, decimal-ID rejection, injection rejection, subsystem
rejection and PTY rejection. They do not establish live SSH authorization,
forwarding behavior, remote `pct` compatibility or firewall reachability. The
installed local client was OpenSSH 10.3p1; target versions must be checked during
bootstrap. The options follow the primary [sshd authorized-key documentation](https://man.openbsd.org/sshd.8)
and [SSH client configuration documentation](https://man.openbsd.org/ssh_config.5).

## Exact owned drift and acknowledgement

Reconciliation reports `drift` entries with exact kind, recorded UUID,
`missing`/`changed`, and allowlisted before/after fields when a fingerprint-verified
private template exists. Headers, authentication data, arbitrary provider fields
and repair templates never enter diagnostics. A changed fingerprint without a
verified template remains an explicit unknown-field blocker.

**Review observed hostname change** prepares a metadata-only `accept` plan. It
can accept only a reservation hostname corrected to the current container name,
with every other resource byte unchanged. It requires current creation identity,
profile-derived MAC/IP, conflict checks, unchanged UUIDs/dependencies/parent
context, verified subnet and selected shared policy/trust, and the recorded
upstream, desired route and exposure. The complete configuration, ownership,
inventory and provider snapshot are bound to review and rechecked on apply.
Existing public access requires explicit acknowledgement. Missing UUIDs,
unsupported changes, unknown auth state, changed route/exposure or significant
fields cannot be rebased. No newly observed foreign UUID is adopted. Successful
apply atomically updates only the acknowledged fingerprint/template and records
an Activity operation; it sends no provider write or service application.

## Add missing records

**Add missing reservation** prepares action `reservation`, without an upstream
port. It creates only the missing profile/subnet-resolved reservation, preserving
supported advanced fields from its private template. Existing reservation UUIDs
are never overwritten through this action. The recorded app route, exposure,
proxy, external parent and shared ACL remain unchanged. Only KEA is applied.
A new reservation-only app has no invented route; a later proxy review requires
explicit scheme and port.

**Add missing proxy / repair configuration** uses the existing guarded `ensure`
workflow with explicit per-app scheme and port. The review lists every create,
update and retained resource, including reservation repair if also missing, and
preserves imported advanced settings and native child relations. This is a whole
app repair; it does not promise that selecting the app repairs just one provider
row. Close the review if its effects exceed the intended change. Private is the
new-route default, with the existing ACL selector and no firewall-wide upstream
default. Fresh subnet/trust/identity guards remain in force before writes and
application. No public route is created by background work.

## Remove stale owned reservations or proxies

Mismatches classifies an owned orphan only from complete fresh cluster inventory
and a complete provider snapshot. Running or stopped guests still present, VMID
reuse/migration anywhere in enrolled clusters, changed or uncertain fingerprints,
another container using a selected IP/MAC/upstream, foreign references, shared
ACLs and wildcard parents prevent unsafe cleanup. Dashboard-owned UUIDs use this
normal owned path; they cannot enter the external cleanup path below.

For an owned orphan, select the exact reservation/proxy UUIDs and choose **Review
owned resource cleanup**. Unselected records remain owned. Dependencies retained
outside the selection block deletion. A reservation cannot be removed while a
retained or foreign proxy still forwards to its address; select the linked owned
proxy resources as well, or resolve the protected foreign dependency first. Confirm **Delete listed owned resources**
once; fresh absence, readiness, resource fingerprints and reference checks run
before every deletion and service apply. Already absent selected records are
forgotten without a delete request; if all selected records are absent no service
apply is sent. Deletes use handle → child → domain → owned ACL → reservation;
service apply and recovery use **Caddy before KEA**. The second service is withheld
if the first fails. Partial, interrupted and uncertain operations remain durable
and block subsequent changes. Recovery retries only verified service application,
never an unknown delete/create. Activity records exact operation and phase.
A lost HTTP response is not treated as rollback and never triggers a POST retry.

## Remove eligible manually created records

Mismatches also lists **Possible stale external records**. Existing manually
created reservations, ordinary app domains/children and their handles can be
selected, reviewed and deleted without importing or adopting ownership. There
is no scheduler. Select one absent app's exact UUIDs, acknowledge external use,
and choose **Review selected external cleanup**. Review the allowlisted effects
and confirm again that these exact UUIDs remain unused by external or unmanaged
clients before choosing **Delete listed external resources**.

Eligibility requires a unique configured linked-host address-profile mapping.
A reservation must match both the derived MAC and IP, and its selected KEA subnet
must be the uniquely containing subnet. An ordinary app domain/child must be one
exact DNS label under the configured domain base. All related upstream handles
must be fully observed, ordinary and target the same unique profile-derived
address/VMID. That VMID must be absent across complete cluster occupancy,
including stopped VMs and guests on other nodes; no observed container may use
the address or MAC. Absence from enrolled containers or a domain suffix alone
never establishes eligibility. The administrator's acknowledgement covers the
remaining uncertainty about external DHCP clients and unmanaged proxy users.

Select an app domain/child and every related eligible handle together. Retained
or foreign consumers block deletion. A reservation remains protected while any
retained handle may reference its IP, including ambiguous upstream strings. Reservation review and each write also
require complete snapshots from every other configured firewall; a retained
handle there blocks release until that proxy is separately cleaned. Those
firewalls remain read-only dependencies bound by fingerprint.
Caddy deletes and successful Caddy application precede reservation deletion and
KEA application. A reservation-only cleanup without proxy consumers applies KEA
only; proxy-only cleanup applies Caddy only. Shared wildcard parents, siblings,
ACLs and trust/header settings are never selected or changed. A child below a
verified external wildcard parent may be selected with its handles; the parent
is retained as bound context. Unknown authentication/policy, advanced routes,
ambiguous mappings, external addresses and shared app scopes stay protected with
a specific explanation. No arbitrary resource writer or ownership transfer is
available through this path.

Plans bind explicit UUIDs, kinds and fingerprints, configuration/ownership,
complete inventory, provider snapshot and identity of the profile mapping. They
expire after five minutes and are single use. Fresh complete observations and
reference checks run before every delete and service application. Any changed
context, occupancy, selected UUID or fingerprint blocks the workflow. Provider
APIs have no cross-system transaction or conditional delete, so avoid concurrent
provider changes while applying a plan.

Partial or interrupted external cleanup stays blocked in Activity. The private
journal retains original getter evidence, exact deletion targets and completed
phases even when rows disappear. It never becomes dashboard ownership and never
uses generic owned recovery. Choose **Review fresh external cleanup recovery**
after acknowledging the original UUIDs again. This inspects current complete
occupancy and every original target; only unchanged remaining rows or proven
absent selected rows can produce a new reviewed plan. Reappeared deleted UUIDs,
new foreign references or changed retained context block recovery. Remaining
rows can be deliberately deleted through that new plan, and pending service
application can be deliberately repeated. Neither a lost response nor process
restart automatically retries a delete or service action. A failed recovery
keeps the partial journal and never reports success or silently unblocks writes.

## Validation and acceptance limits

Use `make check`, `go test -race -p 2 -parallel 2 ./...` and the web browser suite
with one worker. `make build` regenerates the embedded web distribution before
building the single binary. Tests include real adapter fixtures, synthetic native
route ownership, hostname acknowledgement, reservation-only creation, selected
owned and external cleanup, external-use acknowledgement, partial recovery,
dependency/address/VMID protections, no bulk SSH, fixed-command wrapper
rejection, capability failure, UI readiness, lost responses and exact selection.
Screenshots and logs remain in ignored local evidence storage. Baseline/candidate
screenshots use synthetic records rendered by the actual application. Isolated
Go/demo/API/browser checks are separate from live provider acceptance; parent must
review the combined source, confirm current restricted SSH capability,
publish/deploy, and verify
the intended maintenance flow against fresh live evidence.


## Review and recovery boundaries

Owned and external reservation cleanup both bind the complete snapshots of other
configured firewalls during review. They recheck retained upstream references
before every deletion, service application and recovery, including after the
selected reservation row has disappeared. Missing provider readiness blocks the
first delete, not only service application. A cleanup selection may retain a
protected reservation while deleting an independent verified owned proxy group;
the backend checks dependencies against the selected UUIDs.

Reservation-only repair checks reservation IP/MAC collisions independently of
unowned proxy domains. It can create a missing reservation beside an existing
manual proxy without changing or adopting that proxy. Current container and
reservation collision checks and provider readiness repeat immediately before
KEA application and during recovery. Action-specific eligibility never permits
stale inventory or incomplete operations to authorize a repair.

An external cleanup journal retains its original policy hash separately from
credential material and configuration revision. Credential or CA repairs allowed
by Settings do not change that policy. A newly reviewed recovery plan binds the
current full configuration and credentials, while policy/ownership changes still
block recovery. Legacy journals without a policy hash retain their stricter full
configuration binding. Maintenance capability observations expire on their own
60-second deadline, without waiting for an unrelated dashboard render.
