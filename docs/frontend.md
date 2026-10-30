# Container Proxy frontend

The dashboard is a static React and TypeScript application in `web/`. Vite produces `web/dist` for the Go embed package in `web/assets.go`. No Node server, external font service, CDN, or Next.js runtime is needed after building. The interface follows the confirmed product and dashboard brief: compact sidebar navigation, host-grouped Cards, compact Rows, and a shared detail inspector.

## Development and checks

Use Node 24.21 or newer and pnpm 11.24. The exact dependency graph is in `web/pnpm-lock.yaml`.

From the repository root:

```sh
pnpm --dir web install --frozen-lockfile
pnpm --dir web check
pnpm --dir web test
pnpm --dir web build
```

`check` runs the TypeScript checker and Oxlint with warnings treated as failures. No ESLint configuration exists in this package. Vitest uses at most two workers. `build` checks types and produces the actual production assets, including the self-hosted Geist variable font. It must run before compiling the Go embed package.

To develop against an isolated demo API, choose a new empty state directory. Keep the API and development server in separate terminals:

```sh
mkdir -p .local
demo_dir=$(mktemp -d .local/frontend-demo.XXXXXX)
go run ./cmd/dashboard -demo -listen :8080 -state-dir "$demo_dir"
```

```sh
pnpm --dir web dev --port 5173 --strictPort
```

Vite binds to all interfaces and proxies `/api` to the loopback demo API by default. Override the target using `DASHBOARD_API_ORIGIN` when needed. Use the LAN address printed by Vite for a shared review. These services remain foreground processes; stop only the processes you started after the review window. Preserve demo state needed for evidence or continued testing.

## Browser verification

Install the Playwright browser if it is not already available:

```sh
pnpm --dir web exec playwright install chromium
pnpm --dir web test:browser
```

The ordinary browser suite intercepts API requests with typed fixtures. It checks desktop (1600×1000), small laptop (1024×850), and mobile (390×844), both system themes, manual theme switching, navigation drawer focus, retained drafts, plan acknowledgement, partial-write refresh, and recovery displays. Axe checks the inventory and inspector for WCAG A/AA violations. Screenshots go into the ignored local evidence directory and must stay out of Git.

Set `DASHBOARD_WEB_URL` to test another running frontend origin, including the standalone embedded binary. No Vite-specific browser API is required. The optional real demo suite refuses non-demo auth status before requesting any mutation:

```sh
DASHBOARD_DEMO_TEST=1 pnpm --dir web test:browser
```

Keep `DASHBOARD_WEB_URL` set when targeting the embedded binary. The demo suite expects the seeded Main and pvedell hosts, eight LXC containers, and next VMIDs 605 and 703. It captures both views and composed filters, then explicitly selects private access before reviewing and applying a port-2283 route, changes it to public with acknowledgement, and requests a mock app update. It does not change settings fixtures or reset an existing state directory. Its route and job changes persist in that isolated demo state. The private-first step makes the workflow repeatable after a previous public-route test.

## Module ownership

| Module                                            | Responsibility                                                                                 |
| ------------------------------------------------- | ---------------------------------------------------------------------------------------------- |
| `src/schemas.ts`, `src/api.ts`                    | Zod response boundaries, authentication headers, sanitized transport errors                    |
| `src/use-dashboard.ts`                            | Initial loading, retained stale data, explicit refresh and bounded active-work polling         |
| `src/viewmodel.ts`                                | Shared container identity, search, filters, status labels and action exclusions                |
| `src/Inventory.tsx`, `src/ContainerDetail.tsx`    | Cards/Rows, host context, one network configuration and maintenance owner                      |
| `src/Mismatches.tsx`                              | Unknown/conflict/missing/drift/orphan review with protected cleanup                            |
| `src/Activity.tsx`                                | Durable operations, sanitized job phases and explicit supported recovery                       |
| `src/settings-model.ts`, `src/SettingsEditor.tsx` | Complete provider drafts, explicit secret intent, revision-based writes and saved-config tests |
| `src/App.tsx`, `src/ui.tsx`, `src/styles.css`     | Authentication, shared shell, semantic components, responsive layout and theme tokens          |

Base UI owns the accessible navigation drawer. Native controls handle ordinary filters and form fields. All interface icons use Phosphor. App identifiers are treated as inferred metadata and use bundled category icons or a neutral container icon, never an assertion that an application is installed. Geist is bundled from its font package, with its font license included under `public/fonts/OFL.txt`. System theme is the default; next-themes applies the stored override before rendering the interface. The full app uses one semantic OKLCH palette and native `color-scheme` support.

## API and state behavior

Allocation claims are withdrawn while refreshing, after a failed refresh, and when inventory expires after five minutes. Failed per-host reads retain previously observed containers and inspectors for display, labeled stale with the previous observation time; the host's current incomplete status and unknown reconciliation state take precedence. Cached observations cannot enable cleanup, network review or updates. A later complete observation replaces the cache, including confirmed removals. A settings-revision change prevents observations from being carried into a different configuration.

Unverifiable legacy upstreams are shown as unavailable. Their scheme and port controls start empty and require deliberate app values; they never inherit retired firewall defaults.

The exact backend contract is [backend-api.md](backend-api.md). The frontend uses `/api/v1` and validates each known response shape. Go nil slices are accepted explicitly as JSON null. Missing fields, malformed records and wrong collection types produce visible errors; they do not become invented empty inventory.

`/auth/status` determines OIDC and bearer availability. OIDC uses the server login URL and server-managed cookies. Administrator tokens exist only in React memory and request headers. Reloading or signing out clears them. Only theme and Cards/Rows preferences use browser persistent storage. A 401 presents sign-in, while authenticated empty inventory presents an inventory empty state.

All configured hosts stay represented in the header with their smallest available VMID. Allocation comes from the backend's cluster-wide LXC/QEMU inventory. Incomplete host inventory shows Unknown; a complete null next ID shows Range full. No client-side max-plus-one allocation is used.

Search includes container identity, VMID, addresses, host/firewall context, state, distribution/version, inferred app, domain, desired exposure, upstream scheme/port and warnings. Host, state, distribution and exposure filters compose. Both views share this exact model and selection. In-page navigation preserves open network and settings drafts. Switching views preserves those drafts. Unknown distributions have a distinct filter value. Closing container details returns focus to the trigger or search field. On narrow screens, opening details focuses and scrolls to the inline inspector.

New network routes default to LAN/VPN access. Scheme, port and exposure are submitted to create an immutable backend plan. The plan displays concrete field changes, resource IDs, service-application notes, revision and expiry. Apply submits only the reviewed plan ID and `{}`. Public plans require an explicit acknowledgement. Once apply has been attempted, that plan cannot be submitted again from the same review. Returned ensure plans must match the inspected container generation before they can be reviewed. A new generation at the same VMID creates a fresh inspector and configuration draft. There is no optimistic network success. Apply errors refresh both reconciliation and durable operation history because provider writes may already have occurred.

Cleanup is offered only for a confirmed, owned orphan under complete inventory with no unfinished operation. Unknown, unowned and conflicting records remain protected. Service recovery is offered only when the operation is at the backend-supported `reconfigure` step with no manual-recovery flag. Other incomplete workflows explain the need to inspect recorded UUIDs, identity and the operation journal manually.

Update actions create actual backend jobs. Running, non-template inventory, creation identity, SSH configuration, supported OS inspection and existing job state determine eligibility. App-update script availability is verified server-side; the UI does not claim that an update or a newer version is available. Terminal uncertainty requires explicit operator inspection and acknowledgement. No container start, stop or reboot action exists.

Full inventory and settings refresh on explicit request and after mutations or action errors. Only lightweight job/operation history polls while known work is active, every 2.5 seconds in a visible tab. Expanded job logs load bounded, sanitized phases. No recurring full-inventory or SSH polling occurs. Request aborts, refresh generations and activity epochs prevent older reads from replacing newer refreshes or newly created jobs. Polls stop on unmount or terminal work. Inventory older than five minutes, failed refreshes and incomplete reads are labeled. Settings remains reachable even when the initial inventory request fails.

## Settings safety

Settings supports adding, editing and removing multiple Proxmox hosts and OPNsense configurations, plus many hosts linked to one firewall. The default profiles come from `/profiles`. Restricted IPv4/IPv6 allowlists, CA trust and optional server-side SSH key/known-hosts paths are editable. Read-only discovery fetches existing Caddy policies and KEA subnets from each saved firewall. A labeled native select explicitly chooses an external shared ACL or dashboard-managed CIDRs; no first list is adopted. Unsupported lists show reasons and cannot be newly selected. Definitions and CIDRs are server observations, and stale/failed discovery preserves the selected UUID. Connection/credential drafts must be saved before discovery. Editor-only results and read flags never enter PUT payloads. No KEA UUID is required: matching is automatic per intended IPv4 and rejects overlap or missing matches. Normal TLS uses os-caddy automatic HTTPS. The custom certificate reference is an optional advanced compatibility control; no DNS attestation is required. Upstream scheme and port exist only in each app inspector, initialized explicitly to HTTP/80 for new apps and preserved from recorded app routes.

Secret inputs are write-only. Blank inputs omit secret fields, preserving saved values. An explicit clear checkbox sends an empty string. Read-only `hasTokenSecret`, `hasApiKey` and `hasApiSecret` flags and editor-only keys are stripped from PUT payloads. Fields are validated before save; the backend remains authoritative for ownership, canonical network rules and provider prerequisites.

Writes include the saved revision. Conflicts preserve all local edits. Reloading the saved revision requires an explicit discard action. Tests are disabled while there are unsaved changes and clearly state that they read the saved server configuration. Removing an OPNsense requires relinking or removing its hosts first; the server also protects enrolled resources and unfinished operations.

## Evidence and acceptance boundary

Local frontend verification covers TypeScript, lint, unit/component regressions, Chromium layouts and interactions, automated accessibility checks, and real isolated demo plan/apply/jobs. The regression suite includes authentication versus empty inventory, Cards/Rows state preservation, secret omission/clearing, revision conflicts, partial-write refresh, unknown-distribution filtering, settings access during inventory failure, and a new job surviving an older in-flight history read.

This is frontend and isolated mock-provider evidence. Real Proxmox, OPNsense, SSH execution, external OIDC acceptance, deployment, installed-binary acceptance, and independent combined reviews require separate integration verification. Chromium viewport emulation is not a physical-device, Safari or Firefox acceptance result. Provider-side eligibility and sanitized remote phases remain authoritative; no remote version or update availability is fabricated.

Update confirmations are consumed before every submission attempt. In-flight and uncertain attempts are tracked by container generation above the inspector, so closing details or changing views does not discard the guard. A lost response requires refreshed Activity and explicit outcome inspection before another review; active or unacknowledged uncertain jobs continue to block it. Submission is disabled when activity state is unavailable. Address conflicts show labeled observed and expected IP/MAC values supplied by the backend, rather than hiding the expected values behind a blocked plan.

Both Cards and Rows share the same inspector: shared-policy name/CIDRs come from reconciliation, unavailable definitions are labeled by UUID, and manual CIDRs are never presented as shared policy. The resolved KEA CIDR is shown independently. Applying mode/policy or app-route changes still requires a concrete fresh review; discovery alone never applies network changes.

A host may add explicit additional address profiles when one Proxmox node spans several VLANs. VMID ranges cannot overlap. Profiles describe intended addresses; KEA CIDRs are discovered independently and need not have the same prefix length. No DHCP internals or UUID entry is exposed.
