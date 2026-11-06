# 1. Action eligibility, mismatch scope and issue codes

Status: accepted

## Context

Whether an action was allowed was decided in five places (cards, rows, the inspector, Mismatches, import) with their own copies of the 5-minute freshness rule and the VMID-in-profile check. The same container could show different reasons, or none. The Mismatches badge, the "N need review" button and the Mismatches page counted different sets, and mixed in containers that were simply not configured yet. The client matched server prose to detect conditions.

## Decisions

1. **Mismatches split.** The sidebar badge, the "N need review" button and the Mismatches page count the same set (`needsReview`): owned orphans, owned resources changed or missing, unfinished operations, real conflicts, and *eligible* protected external candidates. Protected candidates that are not eligible are informational: their total is shown in the external-records section and they are not counted. Live fleet data (192 protected candidates, 1 eligible) showed that counting all of them would drown the badge in records nobody can act on.
   These are separate groups and Configuration filter options, excluded from counts:
   - *Not configured*: no provider record exists yet.
   - *Existing reservation, proxy not configured* (`reservationOnly`): see decision 4.
   - *Import available*: an unambiguous existing configuration.
   - *Unverified*: provider state could not be read. It stays visible as its own group with a count on the Mismatches page, but is not counted in the badge.
   The Activity count uses the `uncertainJob` rule and no longer includes `failed` jobs.
2. **One eligibility module.** `web/src/eligibility.ts` returns, per container and context, allowed or one reason for: network change, add reservation, import, accept, app update, OS update, readiness check and cleanup. It consolidates; it adds no new permission. Where server and client disagreed, the stricter rule is kept. A server refusal for a reservation is merged with the client reason into one message. The readiness-check button shows its reason when disabled.
3. **Stable issue codes.** Each reconciliation item carries `issueDetails: [{code, message}]` aligned one-to-one with `issues`. `issues` keeps its prose and field name. The client validates this with Zod and matches on codes. The `hostProfileMismatch` code replaces prose matching. A dead legacy-string filter for a message the backend no longer sends was removed.
4. **Reservation only.** The server sets `reservationOnly: true` on an unowned item whose only existing provider record is one reservation matching the container's IP and MAC exactly, with no domain or handle found. It is a boolean rather than an issue code because it describes the shape of the existing resources, not a problem; issue codes stay append-only. Live data showed 23 of 27 non-importable conflicts were hand-made reservations that predate the dashboard, a normal starting point. The signal is classification and display only: import eligibility, plans, observation pinning and expiry are unchanged, and import stays unavailable until a proxy exists. An unfinished operation still turns the item into a mismatch, which stays in `needsReview`.

## Consequences

- Guards are unchanged: plan/apply never-retry, observation pinning and expiry stay server-side and untouched.
- A new action or blocking rule is added in one file and appears on every surface.
- `issueDetails` is required in the reconciliation response. An older server without it is refused at the Zod boundary instead of being guessed at.
