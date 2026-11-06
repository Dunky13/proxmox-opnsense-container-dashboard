# Glossary

- **Eligibility**: whether one container may run one action right now. `web/src/eligibility.ts` returns either allowed or a single user-facing reason. Cards, rows, the inspector and Mismatches render that verdict and never re-derive it.
- **Mismatch (stale / drift)**: a record whose dashboard-owned state no longer matches the provider. This means an owned orphan, owned resources that changed or are missing, an unfinished operation, a real conflict, or a protected external candidate that is eligible for cleanup. Protected candidates that are not eligible are informational. Only these count toward the sidebar badge, the "N need review" button and the Mismatches page.
- **Not configured**: an unowned container with no reservation, domain or proxy. A starting point, not drift. Shown in its own group and filter, never counted.
- **Reservation only**: an unowned container whose only existing provider record is one KEA reservation that matches its IP and MAC exactly, with no Caddy domain or handle found. Usually a reservation made by hand before the dashboard existed. The server marks it `reservationOnly`. Shown under "Existing reservation, proxy not configured" and in the Configuration filter, never counted. Classification only: import stays unavailable until a proxy exists.
- **Import available**: an unowned container whose existing provider configuration matches unambiguously and has no import blockers. Reviewed and imported by hand. Shown in its own group and filter, never counted.
- **Unverified**: provider state could not be read. Neither confirmed nor ruled out. Shown apart, never counted.
- **Conflict**: provider resources that clash with a container's identity (IP, MAC, name) or belong to nobody the dashboard can prove. It needs manual resolution. A conflict that is importable is *import available*, not a conflict.
- **Plan**: a server-stored, expiring, never-retried description of the exact provider writes for one container. Applying it re-reads state first.
- **Observation**: a sanitized provider read that a plan or accepted change is pinned to. If the observation changes, the plan is refused.
- **Issue code**: a stable machine-readable identifier sent next to each reconciliation issue (`issueDetails[].code`). Clients match on codes and display the prose.
