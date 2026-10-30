# Dashboard read recovery

Settings and the two activity histories render as their independent reads
complete. Provider reconciliation can take much longer. A bottom-right loading
toast has one labeled bar for each dashboard read: Settings, Containers and proxy
configuration (one combined request), Update history and Operation history.
Pending bars are indeterminate, with no invented percentages or provider stages.
Rows show Loaded only after success, Failed after an error, and Cancelled or Not
applied for stopped or superseded results. After all reads settle, their actual
terminal states stay visible for 1.2 seconds before the toast disappears.
Actionable errors remain in the main content. Dismissing
it hides only that refresh's toast, without cancelling reads or suppressing
errors; a new explicit refresh can show it again. Background activity polling
does not open the toast. It uses a polite status region, a keyboard-accessible
close control and the existing themes, with safe-area spacing on phones.
Reduced motion uses static pending bars and the same textual states.

**Cancel refresh** stops waiting without sending a
provider mutation, and **Refresh** or **Retry refresh** starts a new read cycle.
Configured Settings and Activity remain reachable while inventory is pending.

Client deadlines cover both fetching headers and reading the complete response
body. Reads allow 105 seconds; POST and PUT requests allow 120 seconds, beyond
the server's 90-second API budget and 100-second socket write timeout. These
limits do not increase backend timeouts. Writes are never retried automatically.
A timed-out write may have completed remotely, so the existing consumed plan
and update-confirmation safeguards still require inspection of the outcome.

Successful settings can update host navigation immediately. Inventory becomes
current only when configuration and reconciliation from the same refresh have
both succeeded. Previously observed inventory retains its original settings
mapping while a newer revision is loading. Failed, cancelled, expired, or
pending inventory remains display only, with stale labels, unavailable free
VMIDs and disabled mutations. Partial host reads retain observations only
within the same configuration revision. Both current jobs and operations are
required before maintenance is enabled; usable inventory alone is insufficient.
Activity polling failures withdraw that readiness until a full successful refresh.

Refresh generations, activity epochs, caller cancellation and authentication
attempts prevent superseded responses from changing current state or ending a
newer session. Unmounting cancels owned reads and clears their deadlines.

## Regression checks

From the repository root, using the installed package manager and dependencies:

```sh
pnpm --dir web check
pnpm --dir web exec prettier --check src/App.tsx src/ContainerDetail.tsx src/ContainerIssues.tsx src/Inventory.tsx src/LoadingToast.tsx src/api.ts src/styles.css src/use-dashboard.ts tests/api.test.ts tests/async.test.tsx tests/conflicts.test.tsx tests/loading.test.tsx tests/loading-toast.test.tsx tests/browser/conflicts.spec.ts tests/browser/loading.spec.ts
pnpm --dir web test
pnpm --dir web build
```

Run browser tests with one worker against an isolated demo binary built with
the current embedded frontend. Use a fresh dedicated demo state directory for
each full suite: the existing discovery tests intentionally persist app routes,
so reusing a previous run's state changes later filtering expectations.
Set `DASHBOARD_WEB_URL` to that binary's URL and
`DASHBOARD_EVIDENCE_DIR` to an ignored evidence directory relative to `web`:

```sh
pnpm --dir web exec playwright test tests/browser/loading.spec.ts --workers=1
```

`web/tests/browser/loading.spec.ts` uses only the existing fixture module and
Playwright. Its desktop and mobile pending-inventory cases capture and attach a
screenshot before their first assertion of repaired behavior. The file can be
published on its own against the base UI to obtain honest before artifacts:
fast settings and history return, reconciliation remains held, and the host-name
assertion fails. Against the repaired build, the same cases exercise Settings,
Activity, cancellation and recovery. Additional cases cover inventory before
hung history and a mobile timeout using Playwright's clock.

API and component tests use deterministic clocks and response barriers for
fetch/body deadlines, cancellation, late unauthorized responses, unmount,
overlapping retries, revised settings, activity readiness, and consumed update
confirmations. These are local fixture checks. They establish neither provider
acceptance nor real phone-network behavior after deployment.

## Conflicts and container verification

Cards, rows and the inspector share the server's actual issues and warnings.
Address-rule explanations are shown only when the server reports a host-profile
mismatch; other conflicts keep their own reasons. The explanation shows the
observed VMID, MAC and IP alongside all configured address profiles. It does not
derive an expected address outside supported ranges or modify a legacy layout.

Identity enrollment is a separate prerequisite. For a container whose identity
is unavailable, **Verification help** provides a selectable Linux command:

```sh
cat /proc/sys/kernel/random/uuid
```

Generate a unique UUID for that container. In Proxmox, select the CT, then open
**Summary → Notes → Edit**. Preserve the existing notes and append a new line,
replacing `<UUID>` with the generated value:

```text
container-dashboard-generation=<UUID>
```

Save and refresh the dashboard for server verification. Generate a new UUID for
every recreated or cloned container; never reuse a marker. This does not fix an
address-profile mismatch or configure SSH. Existing server mutation rules still
apply. The UI contains no verification toggle or enrollment bypass.

`web/tests/browser/conflicts.spec.ts` covers the reported legacy CT200/CT201
layouts in cards, rows and the inspector, retaining foreign-resource conflict
reasons and checking disabled network/maintenance controls. It also captures
overview and inspector screenshots before its first repaired-behavior assertion,
so the same fixture file can capture the base UI's terse warnings.
