import { useEffect, useRef, useState } from 'react';
import { ArrowRight } from '@phosphor-icons/react';
import { Api, messageOf } from './api';
import {
  planSchema,
  type Plan,
  type Reconciliation,
  type ReconcileItem,
  type Settings,
} from './schemas';
import { PlanReview } from './ContainerDetail';
import { Badge, Empty, Notice, Status } from './ui';

export function Mismatches({
  api,
  data,
  settings,
  onInspect,
  onRefresh,
  onActivity,
}: {
  api: Api;
  data: Reconciliation;
  settings: Settings;
  onInspect: (key: string) => void;
  onRefresh: () => Promise<void>;
  onActivity: () => void;
}) {
  const [plan, setPlan] = useState<Plan | null>(null);
  const [busy, setBusy] = useState('');
  const [error, setError] = useState('');
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  const items = data.items.filter((item) => item.state !== 'healthy');
  async function cleanup(item: ReconcileItem) {
    const containerKey = item.key.split(':');
    const hostId = containerKey[0];
    const vmid = Number(containerKey[1]);
    if (!hostId || !Number.isInteger(vmid)) {
      setError(
        'The server returned an invalid container key. Refresh before planning.',
      );
      return;
    }
    setBusy(item.key);
    setError('');
    try {
      const next = await api.request('/plans', planSchema, {
        method: 'POST',
        body: JSON.stringify({
          action: 'cleanup',
          hostId,
          vmid,
          public: false,
        }),
      });
      if (alive.current) setPlan(next);
    } catch (err) {
      if (alive.current) setError(messageOf(err instanceof Error ? err : null));
      await onRefresh();
    } finally {
      if (alive.current) setBusy('');
    }
  }
  return (
    <div className="mismatch-page">
      <div className="page-heading">
        <div>
          <h1>Mismatches</h1>
          <p>
            Compare fresh inventory with dashboard-owned KEA and Caddy
            resources.
          </p>
        </div>
        <Badge>{items.length} need review</Badge>
      </div>
      {!data.inventory.complete && (
        <Notice>
          Inventory is incomplete. Unknown records cannot be classified as
          orphans or cleaned up.
        </Notice>
      )}
      {items.length === 0 && (
        <Empty title="No configuration mismatches">
          <p>
            The latest observed inventory matches dashboard-owned resources.
            Refresh to check for new provider changes.
          </p>
        </Empty>
      )}
      <div className="mismatch-list">
        {items.map((item) => {
          const container = data.inventory.containers.find(
            (c) => c.key === item.key,
          );
          const host = settings.hosts.find(
            (h) => h.id === item.key.split(':')[0],
          );
          const unfinished = data.operations.some(
            (op) => op.key === item.key && op.status !== 'succeeded',
          );
          const canCleanup =
            item.state === 'ownedOrphan' &&
            item.owned &&
            data.inventory.complete &&
            !unfinished;
          return (
            <article className="mismatch-entry" key={item.key}>
              <div className="section-heading">
                <div>
                  <h2>{container?.name || item.key}</h2>
                  <p className="muted">
                    {host?.name || item.key.split(':')[0]} ·{' '}
                    {container
                      ? `VMID ${container.vmid}`
                      : 'Container absent from complete inventory'}
                  </p>
                </div>
                <Status value={item.state} />
              </div>
              <ul>
                {item.issues.map((issue, i) => (
                  <li key={i}>{issue}</li>
                ))}
              </ul>
              <div className="actions">
                {container && (
                  <button
                    className="button"
                    onClick={() => onInspect(item.key)}
                  >
                    Inspect and review configuration
                    <ArrowRight size={16} />
                  </button>
                )}
                {!container && (
                  <button
                    className="button danger-text"
                    disabled={!canCleanup || !!busy || !!plan}
                    onClick={() => {
                      void cleanup(item);
                    }}
                  >
                    {busy === item.key
                      ? 'Preparing cleanup plan…'
                      : 'Review owned resource cleanup'}
                  </button>
                )}
                {unfinished && (
                  <button className="button" onClick={onActivity}>
                    Inspect incomplete operation
                  </button>
                )}
              </div>
              {!container && !canCleanup && (
                <p className="disabled-reason">
                  Cleanup requires a confirmed owned orphan, complete fresh
                  inventory and no incomplete operation. Unknown, conflicting
                  and unowned records are protected.
                </p>
              )}
            </article>
          );
        })}
      </div>
      {error && (
        <Notice tone="danger" role="alert">
          {error}
        </Notice>
      )}
      {plan && (
        <PlanReview
          key={plan.id}
          api={api}
          plan={plan}
          onClose={() => setPlan(null)}
          onDone={onRefresh}
        />
      )}
    </div>
  );
}
