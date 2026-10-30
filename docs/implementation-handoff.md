# Container Proxy implementation handoff

## Delivered behavior

One Go binary embeds a responsive dashboard with the approved sidebar, Cards/Rows switch, system light/dark appearance, composed search/filters and a shared container inspector. It supports administrator bearer tokens and OIDC with explicit administrator authorization.

Multiple Proxmox hosts link to one or more OPNsense firewalls. Inventory includes running/stopped containers, observed and expected addresses, distribution details and inferred app identity. Allocation finds the first free VMID in each configured range using complete cluster-wide VM/LXC occupancy. Failed or expired observations withdraw allocation claims. Failed host reads retain previous containers for explicitly stale display, with mutation controls disabled.

The dashboard discovers existing KEA IPv4 subnets and uniquely matches each intended container address. Main's `.6.xx` and pvedell's `.7.xx` can use separate VLAN subnets on one firewall without manually entering subnet UUIDs. Caddy access lists are fetched for selection and reused unchanged; shared lists are never adopted, modified or cleaned up. Normal routes use Caddy automatic HTTPS without a certificate reference. Scheme and port are per-app settings.

Reviewed plans bind target identity, configuration freshness, resource fingerprints, resolved subnet and selected shared policy. Partial/unknown outcomes retain durable recovery state. Cleanup deletes verified owned resources only; missing resources can be recreated or safely omitted from cleanup after review. Ensure applies KEA then Caddy; cleanup applies Caddy then KEA, including recovery after repeated failures.

App and OS updates use fixed commands over strict SSH to Proxmox. Unknown outcomes consume the confirmation and require outcome inspection before another review. Supported OS jobs preserve reboot-required detection even after verbose package output.

## Validation and delivery

Local validation passed: TypeScript, lint, production frontend build, 32 frontend tests, Go tests with `-p 2`, Go vet, 15 Playwright browser tests with one worker, and workflow validation with actionlint. Browser coverage includes desktop/mobile, both themes, keyboard navigation, accessibility checks, stale inventory, legacy app recovery, private/public plans, recovery, shared ACL discovery and independent app upstreams. Provider round trips use realistic HTTPS fixtures; demo changes never contact real providers.

GitHub Actions checks pull requests and `main`, builds static Linux AMD64/ARM64 binaries with embedded assets, and uploads archives with installation files, checksums and the exact source commit. Screenshot evidence is uploaded separately. Artifacts expire after 14 days. Deploy the successful merged-main artifact after checking `SOURCE_COMMIT` and `SHA256SUMS`; do not substitute a local rebuild.

Hosted CI, merge and deployment receipts belong to the associated PR and final operator handoff. Local checks do not prove those outcomes.

## Installation, operation and limits

Use the [README installation steps](../README.md) in an unprivileged Debian LXC. Preserve the private state directory, environment, SSH keys and known-hosts across upgrades. Stop the service and take a secure state backup before replacing its binary. A rollback restores the previous binary together with compatible preserved state; reverting source alone does not undo previously applied provider changes.

Real LXC generations require the documented unique Proxmox description marker when a trustworthy native creation identifier is unavailable. Inventory still works without enrollment; mutations remain blocked rather than inheriting a recycled VMID's authority. Unverifiable legacy app upstreams require deliberate scheme/port entry.

Actual Proxmox/OPNsense versions, OIDC issuer integration, ACME issuance, DNS, LAN/VPN/internet reachability and real SSH updates need acceptance against the configured environment. Provider operations cannot be transactional with concurrent external administrators. The app reports partial outcomes and rechecks dependencies, but cannot eliminate edits between an external read and write.

See the [API contract](backend-api.md), [provider evidence and limitations](provider-notes.md), [frontend behavior](frontend.md), and [approved dashboard brief](dashboard-brief.md).
