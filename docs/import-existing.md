# Reviewed import of existing KEA and Caddy routes

Existing matching provider configuration remains unowned until an administrator reviews an explicit import plan. Enrollment of a container generation identifies the container; it does not grant ownership of matching provider records. Import transfers metadata ownership without rewriting a working route.

## Review and apply

1. Read reconciliation. Exact IP/MAC reservations, the app's full domain, related handles and shared parent context appear as `{kind,id,name,summary}` observations. Reservation hostname is shown even when it is stale. `importAvailable` is true only for a fully verified eligible candidate. `importIssues` gives precise blockers otherwise. Arbitrary getter fields, headers, descriptions and secret values are excluded from observations.
2. Create the plan using the existing endpoint:

   ```json
   {"action":"import","hostId":"main","vmid":609}
   ```

3. Review observed `desiredRoute`, `public`, exact `adopt` UUIDs, `retain` context and `applyNotes`. Import ignores ensure public defaults and accepts no upstream override. Existing exposure and app port/scheme are preserved.
4. Apply the returned plan using the existing `POST /api/v1/plans/{id}/apply` with `{}`. Ownership, consumed plan and succeeded import operation are persisted together. No provider create/update/delete/reconfigure occurs.
5. Refresh operations and reconciliation after a lost response. An applied plan cannot be replayed. Any genuine drift becomes a managed mismatch. A standard reviewed ensure can subsequently align hostname or change this app's route.

For example, an existing HTTP/8080 route with an old reservation hostname is imported as HTTP/8080 with the old hostname still present. It does not become HTTP/80, private by default, or immediately rename the reservation.

## Eligibility and refusal

A single reservation must match both intended profile-derived IP and MAC, and its subnet must be the unique configured subnet containing that IPv4. IP-only or MAC-only matches are collisions. Duplicate reservations, domain matches or resource identities block import. Complete fresh container inventory, creation identity, valid profile and non-template status are required; another container using the IP/MAC blocks import.

An enabled app route must be either:

- One standalone exact FQDN, with no child domains, and one handle referencing the domain with an empty subdomain relation.
- One native exact child FQDN under one enabled external parent, and one handle whose reverse relation equals that parent and whose subdomain relation equals that child.

The handle must be an ordinary unrestricted-path `handle` using `reverse_proxy`, target the exact intended IPv4, and expose a representable HTTP/HTTPS scheme and explicit valid port. Multiple upstream targets, paths/rewrites, insecure or unsupported TLS settings, additional handle ACL/auth, disabled routes, ambiguous references and shared app records receive explicit blockers. No fallback guesses a simpler route. Parent enablement and policy must be observable; nonempty parent ACL/auth context is conservatively unsupported. Authentication fields on app domains, native children and their parents must be explicitly observed; an omitted field is unknown rather than empty. The external parent is never imported, rewritten or deleted.

A private route must already use the configured suitable external request ACL. Both native `remote_ip` and `client_ip` remain supported; client policies bind the existing restricted trust/header context. Import never chooses a different ACL or converts exposure. Selected ACLs, trust lists and header definitions remain external. Unknown exposure is withheld instead of reported as public.

## Freshness, persistence and later maintenance

Plans retain the existing five-minute expiry and pin configuration revision and hash, complete inventory and provider hashes, creation identity, resource fingerprints, unique subnet and external parent/policy context. Ownership changes and unfinished operations are checked at plan creation and apply. Apply repeats eligibility and exact bindings before the single durable state transaction. Failed persistence blocks further mutations until restart/inspection. The same atomic-file durability and host filesystem limitations as other state writes apply.

The optional route layout and app-resource templates extend schema 1 without changing legacy standalone defaults. Templates live only in the protected durable ownership ledger and are not included in plan effects or operations. Existing imported resources receive managed-field patches, so advanced fields and descriptions stay intact. Proven-absent records can be recreated from their templates in the same layout. A nested advanced template the flat provider write DTO cannot safely restore blocks automatic repair with an explicit reason; surviving records can still be maintained without flattening those settings.

Native ensure updates or repairs the child through the subdomain endpoint and preserves the parent/siblings. Parent context is fingerprint-bound and rechecked during preparation, enablement, service application and recovery. A later explicit plan can review changed supported external context without manual certificate attestation. Foreign resources referencing the app through either reverse or subdomain relations block maintenance or deletion.

Ensure retains KEA then Caddy service application. Cleanup requires fully observed container absence, verifies exact owned UUIDs and dependencies, removes handle then child/standalone app domain, owned generated ACL if present, then reservation, and applies Caddy before KEA. Partial recovery keeps the existing durable intent, verified-resource and identity guards. Parent, sibling routes and external policies never enter the deletion ledger.

## Provider evidence and limits

Primary sources are pinned to OPNsense plugins commit `81a7c9cd3ce5f487f36db3f82c4d545feee535af`:

- [Caddy reverse-proxy controller](https://github.com/opnsense/plugins/blob/81a7c9cd3ce5f487f36db3f82c4d545feee535af/www/caddy/src/opnsense/mvc/app/controllers/OPNsense/Caddy/Api/ReverseProxyController.php) defines separate native subdomain actions and wrappers.
- [Caddy model](https://github.com/opnsense/plugins/blob/81a7c9cd3ce5f487f36db3f82c4d545feee535af/www/caddy/src/opnsense/mvc/app/models/OPNsense/Caddy/Caddy.xml#L252-L338) defines child-to-parent and handle-to-parent/child relations.
- [Caddyfile template](https://github.com/opnsense/plugins/blob/81a7c9cd3ce5f487f36db3f82c4d545feee535af/www/caddy/src/opnsense/service/templates/OPNsense/Caddy/Config/Caddyfile#L615-L641) renders enabled child routes, parent context, child ACL and child-selected handles separately.

These establish model/controller behavior. API snapshots do not attest runtime custom Caddy imports, certificate issuance, external DNS, request-chain behavior or public/private reachability. Local fixtures and source tests are separate from installed-provider acceptance, signed delivery, hosted CI and deployment. Avoid concurrent manual provider edits during ensure/cleanup because the provider APIs have no cross-system transaction or conditional compare-and-swap write.
