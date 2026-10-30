import type { ReconcileItem } from './schemas';

export function ResourceDrift({ item }: { item: ReconcileItem }) {
  if (!item.drift?.length) return null;
  return (
    <div className="resource-drift" aria-label="Owned resource changes">
      {item.drift.map((resource) => (
        <details key={`${resource.kind}:${resource.id}`} open>
          <summary>
            {resource.status === 'missing' ? 'Missing' : 'Changed'}{' '}
            {resource.kind} · <code>{resource.id}</code>
          </summary>
          <p className="muted">{resource.detail}</p>
          <dl>
            {resource.changes.map((change) => (
              <div key={change.field}>
                <dt>{change.field}</dt>
                <dd>
                  <span className="change-before">
                    {change.before || '(empty)'}
                  </span>{' '}
                  → <strong>{change.after || '(empty)'}</strong>
                </dd>
              </div>
            ))}
          </dl>
        </details>
      ))}
    </div>
  );
}
