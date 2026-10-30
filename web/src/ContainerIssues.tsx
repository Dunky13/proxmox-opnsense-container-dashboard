import type { ContainerView } from './viewmodel';
import { Field } from './ui';

export function ContainerIssues({
  view,
  expandedResources = false,
}: {
  view: ContainerView;
  expandedResources?: boolean;
}) {
  const c = view.container;
  const issues = view.item?.issues ?? [];
  const resources = view.item?.existingResources ?? [];
  const importIssues = view.item?.importIssues ?? [];
  const warnings = [...new Set([...c.warnings, ...issues])].filter(
    (issue) =>
      !resources.length || issue !== 'unowned reservation or domain conflicts',
  );
  const profileMismatch = issues.some((issue) =>
    [
      'container does not match host profile',
      'container does not match configured host profile',
    ].includes(issue),
  );
  const profiles = view.host
    ? [view.host, ...(view.host.addressProfiles ?? [])]
    : [];
  const outsideRange =
    profiles.length > 0 &&
    !profiles.some(
      (profile) => c.vmid >= profile.vmidMin && c.vmid <= profile.vmidMax,
    );
  if (
    !warnings.length &&
    !resources.length &&
    !importIssues.length &&
    c.identity &&
    view.item?.state !== 'conflict'
  )
    return null;
  return (
    <div className="container-issues">
      {resources.length > 0 && (
        <details
          className="existing-configuration"
          open={expandedResources || undefined}
        >
          <summary>
            Existing configuration · {resources.length} resources
          </summary>
          <p>
            {view.item?.owned
              ? 'These observations describe the current provider configuration. Review any remaining drift before changing it.'
              : view.item?.importAvailable
                ? 'Matching records are available for reviewed import. Nothing is imported automatically.'
                : 'Inspect the matching records and any contradictory values below. Import is unavailable until the blockers are resolved.'}
          </p>
          <ul className="existing-resources">
            {resources.map((resource) => (
              <li key={`${resource.kind}:${resource.id}`}>
                <strong>
                  {resource.kind}: {resource.name || 'Unnamed resource'}
                </strong>
                <code>{resource.id}</code>
                <p>{resource.summary}</p>
              </li>
            ))}
          </ul>
        </details>
      )}
      {importIssues.length > 0 && (
        <div className="import-blockers">
          <strong>Import unavailable</strong>
          <ul className="warnings">
            {importIssues.map((issue, index) => (
              <li key={index}>{issue}</li>
            ))}
          </ul>
        </div>
      )}
      {warnings.length > 0 && (
        <ul className="warnings">
          {warnings.map((warning) => (
            <li key={warning}>{warning}</li>
          ))}
        </ul>
      )}
      {!warnings.length &&
        !resources.length &&
        !importIssues.length &&
        view.item?.state === 'conflict' && (
          <p>
            Conflict details were not returned. Refresh before changing this
            container.
          </p>
        )}
      {view.item?.state === 'conflict' &&
        !profileMismatch &&
        !resources.length && (
          <details>
            <summary>Why blocked?</summary>
            <p>
              A conflict blocks automatic network changes because the server
              could not safely match this container to its intended
              configuration. Inspect the exact reasons above and current
              provider records before reviewing another plan. Existing foreign
              resources are not adopted or replaced automatically.
            </p>
          </details>
        )}
      {profileMismatch && (
        <details>
          <summary>Why blocked? Address rules</summary>
          <p>
            The server reports that this container does not match its host's
            automatic address rules.
          </p>
          {outsideRange && (
            <p>
              VMID {c.vmid} is outside every configured address-profile range.
            </p>
          )}
          <dl className="facts">
            <div>
              <dt>Observed VMID</dt>
              <dd>
                <code>{c.vmid}</code>
              </dd>
            </div>
            <div>
              <dt>Observed IP</dt>
              <dd>
                <code>{c.ip || 'Unknown'}</code>
              </dd>
            </div>
            <div>
              <dt>Observed MAC</dt>
              <dd>
                <code>{c.mac || 'Unknown'}</code>
              </dd>
            </div>
          </dl>
          <p>Configured address profiles:</p>
          <ul className="address-profiles">
            {profiles.map((profile, index) => (
              <li key={index}>
                VMIDs{' '}
                <code>
                  {profile.vmidMin} to {profile.vmidMax}
                </code>
                ; network <code>{profile.network}</code>; MAC prefix{' '}
                <code>{profile.macPrefix}</code>
              </li>
            ))}
          </ul>
          {!profiles.length && (
            <p>
              Host configuration is unavailable. Refresh Settings to inspect its
              rules.
            </p>
          )}
          {outsideRange && (
            <p>No expected IP is calculated outside these supported ranges.</p>
          )}
          <p>
            Review the host's address profiles in Settings before deciding how
            to handle this legacy layout. The dashboard will not renumber,
            readdress or enroll it automatically.
          </p>
        </details>
      )}
      {!c.identity && (
        <details>
          <summary>Verification help</summary>
          <p>
            One-time identity enrollment lets the server recognize this
            container, so a different container that later reuses the VMID
            cannot be treated as this one. This is separate from address rules
            and SSH configuration.
          </p>
          <ol>
            <li>
              On Linux, generate a unique UUID for this container.
              <Field
                label="Linux UUID command"
                help="Select and copy this command; run it on Linux."
              >
                {(id, helpId) => (
                  <input
                    id={id}
                    aria-describedby={helpId}
                    readOnly
                    value="cat /proc/sys/kernel/random/uuid"
                    onFocus={(event) => event.currentTarget.select()}
                  />
                )}
              </Field>
            </li>
            <li>
              In Proxmox, select CT {c.vmid} ({c.name}), then open{' '}
              <strong>Summary → Notes → Edit</strong>. Preserve existing notes
              and append a new line, replacing <code>&lt;UUID&gt;</code> with
              the generated UUID:
              <pre>
                <code>container-dashboard-generation=&lt;UUID&gt;</code>
              </pre>
            </li>
            <li>
              Save the notes, then refresh the dashboard so the server can
              verify the observed container identity.
            </li>
          </ol>
          <p>
            Generate a new UUID for each recreated or cloned container. Never
            reuse markers.
          </p>
          <p>
            Identity enrollment does not fix an address-rule mismatch.
            Maintenance also requires configured SSH, a running container and
            current activity history. No dashboard control bypasses these
            checks.
          </p>
        </details>
      )}
    </div>
  );
}
