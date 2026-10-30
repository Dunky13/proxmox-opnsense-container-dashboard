# Backend API contract

Version: `/api/v1`. JSON uses camelCase. This contract is the frontend entry point; provider implementation and acceptance limits are in [provider-notes.md](provider-notes.md).

## Authentication and transport

`GET /api/v1/auth/status` is public and returns `{demo, bearerEnabled, oidcEnabled, authenticated, principal}`. Principal is empty when anonymous. `GET /api/v1/auth/login` initiates OIDC; `GET /api/v1/auth/callback` is its callback. `POST /api/v1/auth/logout` clears the session. An explicit subject or verified-email allowlist is mandatory for OIDC admin access. Sessions are server-side, expire, and use Secure/HttpOnly/SameSite=Lax cookies. An admin token is supplied only through `Authorization: Bearer …`; never put tokens in URLs or browser persistent storage. All other API routes require either method in live mode. Demo mode explicitly bypasses authentication and uses isolated mock providers and state.

All writes reject cross-origin Origin and Fetch Metadata headers. Cookie-authenticated writes require Origin. Deploy behind HTTPS with `DASHBOARD_PUBLIC_ORIGIN` matching the external origin; forwarded headers are not trusted. There is no wildcard CORS. Requests are limited to 1 MiB and reject unknown fields/trailing JSON. Responses have `Cache-Control: no-store`. Errors are `{error:{code,message}}`; validation 400, unauthenticated 401, forbidden 403, not found 404, conflict/stale/busy 409, provider failure 502, unavailable 503. Provider response bodies and credentials never appear in errors.

## Settings

- `GET /api/v1/settings` → `{revision,hosts:[],opnsenses:[]}` (secret fields absent; `hasTokenSecret` / `hasApiSecret` / `hasApiKey` flags).
- `PUT /api/v1/settings` replaces the complete configuration using the host/provider write fields below plus required `revision`. Omit the read-only `has…` flags from writes. Revision conflicts return 409. Keep an existing secret/API key by omitting its field, set with a string, intentionally clear with `""`. Removing a referenced OPNsense, or removing/changing an enrolled host/provider while ownership or unfinished operations exist, is refused.
- `GET /api/v1/profiles` → the two default host profile templates.
- `POST /api/v1/hosts/{id}/test` → HostInventory connection/inventory preflight (`complete:false`, `error` and null `nextVmid` on provider failure).
- `POST /api/v1/opnsenses/{id}/test` → KEA/Caddy preflight with explicit prerequisite findings. These test routes perform reads only.

Host fields: `id`, `name`, `baseUrl` (HTTPS), `node`, `opnsenseId`, `vmidMin`, `vmidMax`, `network`, `macPrefix` (five bytes), `tokenId`, optional write-only `tokenSecret`, optional `caPem`, optional legacy `subnetId` (preserved for compatibility, ignored by automatic resolution), optional `addressProfiles:[{vmidMin,vmidMax,network,macPrefix}]` for additional VLAN address profiles on the same host, `ssh` object (`enabled`, `host`, `port`, `user`, `privateKeyFile`, `knownHostsFile`). Host profiles: main 601–699, 192.168.6.0/24, d0:cc:37:00:06; pvedell 701–799, 192.168.7.0/24, d0:cc:37:00:07. Suffixes use two-digit decimal text: 612 → 06:12 → .6.12. Each address profile must remain in its matching hundred block with a canonical /24 and decimal MAC prefix. Additional profile ranges must not overlap; at most sixteen are allowed. The next free VMID is the smallest available across all configured profile ranges. Existing containers outside these bounds remain visible but cannot be auto-reconciled.

OPNsense fields: `id`, `name`, `baseUrl` (HTTPS), optional write-only `apiKey` and `apiSecret`, optional `caPem`, `domainBase`, `allowlistCidrs`, and optional `privateAccessListId` (empty or an existing Caddy ACL UUID). Empty selection explicitly uses dashboard-managed CIDRs. Existing-list selection reuses the list unchanged and excludes it from ownership and cleanup. Private choices must contain non-empty restricted canonical CIDRs, use `remote_ip`, and disable inversion. Missing/unsafe policy blocks private ensures and recovery, while public plans, discovery and cleanup remain independent of its suitability.

No KEA UUID, custom certificate, DNS readiness attestation or firewall-wide upstream setting is required. Legacy `subnetId`, `upstreamScheme`, `upstreamPort`, `certificateId` and `domainReady` remain accepted/preserved for compatibility. Retired subnet hints never override automatic target resolution; retired firewall upstream values never initialize new apps. Explicit existing app routes remain per app, and older ownership migrates from a fingerprint-verified handler. An optional non-empty `certificateId` requests the existing custom-certificate override. With no override the adapter omits `CustomCertificate` on creation and uses os-caddy automatic HTTPS/global TLS configuration; an existing owned override is preserved when omitted. No global TLS, DNS, subnet or pool writes occur. Service preflight is not proof that ACME issuance or external DNS/reachability succeeded.

- `GET /api/v1/opnsenses/{id}/access-lists` → `{opnsenseId,revision,accessLists:[{id,name,description,cidrs:[],matcher,inversion,suitable,reason}]}`. Authenticated, saved-configuration, Caddy-only reads using stored credentials and CA trust. Includes unsupported policies with explicit reasons. Dashboard-owned generated ACLs are unavailable for shared selection. A successful empty array is distinct from a discovery error. No KEA, certificate, domain-ready or daemon-status prerequisites.
- `GET /api/v1/opnsenses/{id}/subnets` → `{opnsenseId,revision,subnets:[{id,cidr,description}]}`. Authenticated saved-configuration KEA-only reads of every configured IPv4 subnet. No selection or UUID entry is required. Errors and incomplete inventories fail visibly instead of returning empty data.

Every ensure resolves the intended profile-derived IPv4 against the complete KEA topology, requires exactly one containing subnet, and binds its UUID, CIDR and complete normalized definition fingerprint. A host address profile /24 need not equal the DHCP subnet CIDR. Both .6 and .7 targets can resolve to different subnets on the same firewall. Overlap, disappearance, reassignment, invalid definitions or changed target freshness block writes. Resolution does not broaden or derive Caddy client access policy.

Example replacement (secrets omitted preserve existing values):

```json
{"revision":1,"hosts":[{"id":"main","name":"Main","baseUrl":"https://pve.example.test:8006","node":"pve","opnsenseId":"edge","vmidMin":601,"vmidMax":699,"network":"192.168.6.0/24","macPrefix":"d0:cc:37:00:06","tokenId":"dashboard@pve!inventory","ssh":{"enabled":false}}],"opnsenses":[{"id":"edge","name":"Edge","baseUrl":"https://firewall.example.test","apiKey":"key","domainBase":"went.io","allowlistCidrs":["192.168.6.0/24","fd00:1234::/64"]}]}
```

## Inventory and reconciliation

An owned legacy app whose handler cannot be fingerprint-verified has `routeUnavailable:true` and no invented `desiredRoute`. Repair requires explicit per-app scheme and port. Retired firewall defaults are never used to reconstruct an unavailable upstream. Cleanup does not need an app upstream and remains guarded by resource ownership and complete inventory.

Service application and recovery use the same action-specific order: ensure applies KEA then Caddy; cleanup applies Caddy then KEA. If the first service fails, the second is not applied. A failed cleanup recovery therefore cannot release a reservation while its old running proxy route remains active.

- `GET /api/v1/inventory` → `{observedAt,complete,containers:[],hosts:[]}`. Each host has `hostId`, `complete`, `error`, `nextVmid` (null unless complete and space exists). Containers include `key` (`hostId:vmid`), `hostId`, `node`, `vmid`, `name`, `status`, `template`, `ip`, `mac`, optional profile-derived `expectedIp` / `expectedMac`, `distribution`, `version`, `distroSource`, `app`, `icon`, `identity`, `fingerprint`, `warnings:[]`, and optional `desiredRoute:{scheme,port,public}` for dashboard-owned containers. Without trustworthy creation metadata, an operator must first place a unique `container-dashboard-generation=<UUID>` marker in the Proxmox description for ensure/update actions; missing generation evidence is an explicit inventory warning. Version is empty when unobtainable. App/icon are inferred identifiers, not asserted installed applications. All configured-node LXC including stopped/template entries are listed; allocation counts cluster-wide LXC and QEMU.
- `GET /api/v1/reconciliation` → `{inventory,items:[],operations:[]}`. Items have `key`, optional persisted `desiredRoute:{scheme,port,public}`, `state` (`missing`, `healthy`, `mismatch`, `ownedOrphan`, `conflict`, `unknown`), `issues:[]`, `owned` boolean. A failed/incomplete provider read yields unknown, never an orphan.
- `POST /api/v1/plans` body `{action:"ensure"|"cleanup",hostId,vmid,public:false,upstreamScheme?:"http"|"https",upstreamPort?:1..65535}` → immutable plan `{id,action,key,public,desiredRoute:{scheme,port,public},effects:[],applyNotes:[],createdAt,expiresAt,...}`. Ensure defaults private. Scheme/port overrides are persisted per container; omission preserves an existing desired route, otherwise initializes explicit HTTP/80 app values. Each effect has `kind`, `action` (`create`/`update`/`retain`/`delete`), `resourceId` (empty before create), `name`, and `changes:[{field,before,after}]` with camelCase field identifiers. Review these concrete reservation, ACL, domain and handler effects and service-application notes before apply. Cleanup accepts dashboard-owned records only. Plans expire after five minutes and pin config revision, full fresh inventory, creation identity and provider resource fingerprints. No infrastructure writes occur while planning.
- `POST /api/v1/plans/{id}/apply` body `{}` → operation. Re-reads all inventory and provider resources, refuses stale plans/identity changes/migration ambiguity, and journals intent before every provider write. Never retries uncertain creates automatically. Ensure creates/updates the reservation, optionally a generated ACL, disabled domain and handler, verifies ACL attachment, then enables the domain and reconfigures. Shared ACL selection creates only an attachment and never mutates the shared definition. Existing unrelated resources are conflicts even if values match. Cleanup deletes only exact owned UUIDs with unchanged fingerprints, in dependency order. No automatic cleanup exists.
- `GET /api/v1/operations` → array of durable operations.
- `POST /api/v1/operations/{id}/recover` body `{}` → retry service application only for fully recorded resource mutations with unchanged identity/fingerprints. Unknown write outcomes or incomplete workflows require operator inspection and remain blocked; recovery never adopts resources based on names/IPs.

Operation fields: `id`, `planId`, `key`, `opnsenseId`, `containerFingerprint` (ensure-operation freshness binding), `action`, `status` (`running`, `succeeded`, `partial`, `interrupted`), `step`, `error`, `manualRecovery`, `createdAt`, `updatedAt`, `resources:[]` (each has `id`, `kind`, `fingerprint`; an empty fingerprint records an acknowledged UUID whose readback was unverified). Non-success remains visible after process restart and blocks conflicting mutations. Reconfigure failure is partial, never success. API errors for actions retain operations for subsequent status retrieval.

## Updates and jobs

- `POST /api/v1/jobs` body `{hostId,vmid,kind:"appUpdate"|"osUpdate",fingerprint}` → 202 Job.
- `GET /api/v1/jobs` → array, newest first.
- `GET /api/v1/jobs/{id}` → Job.
- `GET /api/v1/jobs/{id}/logs` → `{jobId,logs:[],truncated}`.
- `POST /api/v1/jobs/{id}/acknowledge` body `{}` → acknowledges an uncertain terminal update after operator checks the container. Allows a new explicit request; does not rerun anything.

Jobs have `id`, `key`, `kind`, `status` (`queued`, `running`, `succeeded`, `failed`, `timedOut`, `interrupted`, `rebootRequired`), `createdAt`, `updatedAt`, `error`, `acknowledged`, `logs:[]`, `truncated`. Up to two jobs run concurrently; a per-container lock rejects duplicate queued/running or unacknowledged uncertain jobs. Queue length and history are bounded. Updates require fresh running, non-template inventory and matching fingerprint, enabled SSH, and creation identity. OS update re-reads actual `/etc/os-release`, supports only Debian/Ubuntu apt and Alpine apk. App update invokes only existing `/usr/bin/update`, which may itself fetch community-scripts upstream code; no installer URL or arbitrary command can be supplied. Noninteractive execution may fail for scripts requiring a terminal. Jobs have a fixed timeout and preserve interruption/unknown-outcome state. No container start, stop or reboot is issued. Remote output is intentionally not exposed, to avoid leaking arbitrary secrets; logs describe bounded execution phases and sanitized outcomes.

## Local startup and frontend integration

`go run ./cmd/dashboard -demo -listen :8080 -state-dir ./var/demo` starts an explicitly marked mock backend; live auth environment and secret files are ignored and real provider actions are unavailable. Default non-demo state is `./var/live`; mode is recorded in state and mismatches refuse startup. Live API requires `DASHBOARD_ADMIN_TOKEN` (or `DASHBOARD_ADMIN_TOKEN_FILE`) and/or OIDC config. Set `DASHBOARD_PUBLIC_ORIGIN` for externally served requests. For OIDC set `DASHBOARD_OIDC_ISSUER`, `DASHBOARD_OIDC_CLIENT_ID`, `DASHBOARD_OIDC_CLIENT_SECRET` (or `_FILE`), `DASHBOARD_OIDC_REDIRECT_URL`, and comma-separated `DASHBOARD_OIDC_SUBJECTS` and/or `DASHBOARD_OIDC_EMAILS`. Redirect URL must be HTTPS and use `/api/v1/auth/callback` on the configured origin. Only an in-memory server-side session store is used; process restart logs out sessions.

`dashboard.NewAPI(service, auth, frontendHandler)` is the frontend seam. A nil frontend returns 404 outside API routes. Parent can supply an embedded filesystem handler; backend contains no placeholder UI. `GET /healthz` is unauthenticated liveness. No live provider runtime acceptance is implied by fixture tests.

## Common request examples

```json
{"action":"ensure","hostId":"main","vmid":612,"public":false,"upstreamScheme":"http","upstreamPort":2283}
```

Use the returned plan ID in `POST /api/v1/plans/{id}/apply` with `{}`. A plan does not allocate/create a container, start it, or reserve a VMID. The apply endpoint is synchronous and may return a provider error after writing some steps; always refresh `/api/v1/operations` and `/api/v1/reconciliation` after an error.

```json
{"hostId":"main","vmid":612,"kind":"appUpdate","fingerprint":"<fingerprint copied from fresh inventory>"}
```

Poll job status/log endpoints until terminal; do not resubmit on a lost response. `timedOut`, `interrupted` and `rebootRequired` require operator inspection/acknowledgement before another update on that container. Acknowledgement is explicit recovery state, not evidence of remote success.

`GET /api/v1/inventory` example (abbreviated to show the stable outer shape):

```json
{"observedAt":"2026-10-30T00:00:00Z","complete":true,"containers":[],"hosts":[{"hostId":"main","complete":true,"error":"","nextVmid":603,"guests":[{"vmid":601,"type":"qemu","node":"pve"},{"vmid":602,"type":"qemu","node":"pve"}]}]}
```

This illustrative cluster contains two QEMU guests and no LXC. Actual observed LXC containers populate the container list. `nextVmid:null` on a complete host means its configured range is full; on an incomplete host it means free space is unknown. A container key is stable only within its configured host; use `identity`/`fingerprint`, not the key alone, for action freshness.

Per-container route examples: Immich can use `upstreamPort:2283`, Paperless `upstreamPort:8000`, and a TLS upstream can set `upstreamScheme:"https"`. These overrides affect only that container's owned handler. An explicit plan with `public:true` removes that owned domain's ACL attachment; `public:false` first verifies/attaches the configured private ACL and then enables the route. Existing owned healthy records are updated in place after fingerprint checks. Unowned matching reservations/domains remain conflicts and are never adopted.

A route-change plan can include this effect (the full response contains all affected resources):

```json
{"kind":"handle","action":"update","resourceId":"12345678-1111-4111-8111-123456789012","name":"Owned handle","changes":[{"field":"upstreamPort","before":"8080","after":"2283"}]}
```

New dependency references use the display markers `new:acl` and `new:domain` in effect values until creation returns real UUIDs. Apply resolves those references internally. Domain staging is explicitly shown as an update when another route setting changes, even if the final domain fields are identical; `applyNotes` describes staging and service application. A plan containing only `retain` effects performs no provider write or service reconfigure. Effects and desired route are server-persisted with the immutable plan; apply accepts only `{}`, so the reviewed route cannot be replaced by a different request body.

Reviewed repair and recovery behavior: fresh plans can recreate a verified absent owned resource using a new UUID; matching manual replacements remain conflicts. Existing recorded resources must retain their fingerprints. Previews identify recreated resources and changed dependency IDs. Cleanup skips proven-absent resources and deletes only the verified remainder. Recovery rechecks the original container fingerprint, host profile, target conflicts and owned route fields before each service application. Missing legacy freshness bindings require operator inspection. CIDR sets are canonicalized across option dictionaries and plain strings. Root-volume identity excludes mutable size/options; config digest changes still invalidate plans. OS-update reboot detection streams independently of output length.

Ensure recovery repeats unique per-target KEA containment and the bound subnet definition, current host profile, app route, shared policy and owned resource checks before each service application. Orphans with some proven-absent owned resources remain cleanup candidates only when surviving fingerprints and dependency checks pass. Inventory keeps observed addresses separate from expected profile values, including during conflicts or absent DHCP observations.


Plans and ensure operations include optional `resolvedSubnet:{id,cidr,fingerprint}` and `sharedAccessList:{id,fingerprint}` bindings. New ensure operations additionally bind `desiredRoute:{scheme,port,public}`. They are rechecked before every ensure write and service application, including both recovery applications. Older interrupted operations without these bindings require inspection; they cannot silently acquire fresh authority. Reconciliation items expose `resolvedSubnet` and, when selected, `privateAccessList` using the discovery record shape; unavailable definitions retain the selected UUID and an explicit reason. These are current desired policy observations, not a claim that a pending mode change is already applied. Review effects name the unchanged shared list, UUID and CIDRs, and explain its exclusion from cleanup. Reservation ownership records retain their original `subnetId`; older records obtain it only from fingerprint-verified provider readback. Cleanup uses recorded resource UUIDs and fingerprints across topology/profile changes, never a newly resolved subnet, and never deletes a shared ACL.
