# Container Proxy dashboard brief

Status: confirmed by the user, with a combined layout: B's sidebar plus switchable card and row views. Product context and both access methods are confirmed.

## Feature summary

A working homelab dashboard for Marc to inspect Proxmox containers, prepare KEA reservations and Caddy routes on their linked OPNsense firewalls, review mismatches, and request container maintenance. The application ships as a Go binary with an embedded web frontend and LXC setup instructions.

## Primary action

Understand which containers need attention, then apply the specific network change or maintenance action to the intended container.

## Design direction

Calm and compact, with Linear and Raycast as the user-selected references. Use a restrained neutral palette with one muted green interaction accent and semantic warning/error colors. The physical context is an administrator checking a home lab from a desk or phone in changing ambient light, so follow system light/dark preference and provide a manual override. Use a clear sans family, fixed product typography, and monospace only for identifiers, addresses, and job output.

Design dials: variance 4, motion 2, density 6. Favor familiar layouts and short feedback transitions. Use Base UI for accessible complex controls, ordinary semantic controls where sufficient, and one icon family. No display effects or decorative imagery are required in the shipped app.

The user approved ingredients from both generated high-fidelity probes: B's sidebar and selected-container inspector, with a Cards/Rows switch to include A's grouped cards. Both modes support system light/dark themes. This combined direction is the implementation contract.

## Scope

Production-oriented implementation of the complete dashboard surface, rather than a static mock: Containers, Mismatches, Activity, Settings, first-run setup, and sign-in. Initial implementation acceptance uses isolated demo providers and provider fixture tests. The user subsequently authorized Linux build CI, signed commit, push, PR, merge after checks and reviews, and deployment of the merged build artifact to pvedell. Actual OIDC and provider runtime acceptance requires configured providers and will be reported separately. Deployment does not authorize unrelated firewall or container changes.

## Layout strategy

Use B's compact sidebar with Containers, Mismatches, Activity, Settings, and a host list. Keep the smallest available VMID for every configured Proxmox host in the header; show unavailable when a fresh complete inventory cannot establish it. Place global search, host/state/distribution/exposure filters, and the Cards/Rows switch immediately above the inventory. The All hosts scope remains available; selecting a host in the sidebar filters the same inventory.

In card mode, group container cards under plain host headings showing the linked firewall and observation freshness. In row mode, show compact container rows with a selected-container inspector as in B. Both modes expose VMID, name, inferred app icon where available, observed IP and MAC, distribution, running/stopped state, hostname, and access/configuration state. Keep App update and OS update controls accessible; disable unavailable actions with a reason. Use one shared inspector/configuration owner across both views. Keep search, filters, selected container, and host scope when switching modes; remember the view preference locally. Expand route settings and job output inline rather than requiring a modal for routine work.

On narrow screens, navigation and header indicators wrap deliberately, filters expand inline, and cards become a single column. All actions remain accessible.

## Key states

- First run: guide the administrator to add a firewall, add a Proxmox host, link them, test connections, and sync.
- Loading: stable skeletons matching the inventory structure, with an announced refresh state.
- Empty inventory or no search matches: distinguish those states and offer the relevant setup or clear-filter action.
- Provider unavailable or partial inventory: show the failure and last observation; suppress unsupported free-ID claims and destructive cleanup.
- Running/stopped: both remain in inventory; maintenance requires a running container and configured execution capability.
- Missing or conflicting network records: explain desired versus observed values and the exact proposed change.
- Public access: explicit warning and confirmation before removing private access restrictions.
- Mutation in progress: show queued/running/result state, bounded output, failure, partial application, or interrupted recovery status.
- Demo: persistent labeling and isolated simulated changes; no fallback from failed live providers into demo results.

## Interaction model

Search covers names, VMIDs, IP/MAC, host/firewall names, state, distribution, and exposure. Filters compose and can be cleared. Sync refreshes observations without applying network changes.

Prepare config creates the reservation and route with LAN/VPN access by default; the administrator sets upstream scheme and port per app in the container inspector. Do not present scheme or port as OPNsense-level settings. Changing one app's upstream must not change another's. Public exposure is deliberate. Mismatch resolution previews affected resources and applies only the requested change. Orphan cleanup requires fresh inventory and verified dashboard ownership; unrelated manually configured records are not silently adopted or deleted.

Matching existing reservations and standalone or native child routes can be imported through an explicit review of exact UUIDs and managed ownership. Import records ownership atomically without changing provider configuration, upstreams or current exposure. Existing public access requires acknowledgement. Shared wildcard parents and access lists remain external. Real collisions and unsupported layouts expose precise blockers; stale reservation names remain a visible mismatch for a separate reviewed network change.

App update runs the container's existing update command through the configured Proxmox execution path. OS update uses the supported package manager. Activity shows progress and output. The UI never invents an available-version or up-to-date claim.

Settings supports add/edit/remove for multiple Proxmox and OPNsense configurations, N:1 links, ranges, address conventions, API credentials and optional SSH execution. Discover KEA IPv4 subnets from the linked firewall and automatically resolve each container's intended address to its uniquely matching subnet. Main's `.6.xx` and pvedell's `.7.xx` can use separate VLAN subnets on the same firewall; no manual KEA subnet UUID is required. Missing or ambiguous matches explain why changes are blocked.

Fetch existing Caddy access lists and let the administrator choose a restricted default for LAN/VPN routes. Reuse that shared list without modifying, adopting or deleting it. Keep explicit CIDRs as an alternative for dashboard-managed lists. DHCP subnet selection and client access restrictions are separate decisions. Secret edits preserve existing values unless explicitly changed or cleared. Refuse removal of a firewall still linked to hosts and explain how to resolve the link.

Routes use the existing OPNsense Caddy automatic HTTPS configuration. Do not require a Caddy certificate reference or a substitute manual TLS attestation in normal setup; do not rewrite global TLS settings.

Sign-in supports the user's OIDC provider and configured administrator token. The server enforces authorization. Do not persist tokens in browser storage.

## Content requirements

Use VMID as the Proxmox term for the requested PVID. Show observed versus expected addresses explicitly when they differ. Preserve the decimal convention: CT612 means MAC suffix 06:12 and IP 192.168.6.12. The next-ID example 603, 604, 606, 607, 608 returns 605. Unknown distribution versions and update capability remain unknown with a reason.

Probes contain illustrative records, not observed infrastructure. Their incidental labels, timestamps, IPs, and version claims are not specifications. Implementation must say OPNsense, use correct 7xx addresses, disable stopped-container updates, and avoid invented update status.

## Implementation references

Use Impeccable product, spatial-design, typography, interaction-design, color-and-contrast, responsive-design, and ux-writing references. Use the frontend-design skill with PRODUCT.md context, pick-ui-library for Base UI, and modern-web-guidance for relevant CSS, theme, and accessibility patterns. The design-taste-frontend skill excludes dashboards; its landing-page composition rules do not govern this surface.

## Open questions and acceptance

The combined layout is approved; no further layout approval is required. Provider addresses, credentials, OIDC registration details, and TLS/DNS prerequisites are runtime settings, not needed to implement the app or enter secrets in chat. KEA subnet UUIDs are discovered automatically.

After confirmation: record the winning direction, implement semantic responsive UI, verify core demo workflows and error states, independently review Standards and Spec, build the embedded binary, and provide setup and evidence. Preserve the distinction between local fixture checks and live provider acceptance.
