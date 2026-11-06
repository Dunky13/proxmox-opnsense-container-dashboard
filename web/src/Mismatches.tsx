import { useEffect, useRef, useState } from 'react';
import { ArrowRight } from '@phosphor-icons/react';
import { Api, ApiError, messageOf } from './api';
import {
  planSchema,
  type Plan,
  type Reconciliation,
  type ReconcileItem,
  type Settings,
} from './schemas';
import { PlanReview } from './ContainerDetail';
import { ResourceDrift } from './ResourceDrift';
import { ExternalCleanup } from './ExternalCleanup';
import { cleanupEligibility, reasonOf } from './eligibility';
import { Badge, Empty, Notice, Status } from './ui';
import { mismatchCounts, reviewScope, type ReviewScope } from './viewmodel';

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
  const [selected, setSelected] = useState<Record<string, string[]>>({});
  const alive = useRef(true);
  const planObservation = useRef<Reconciliation | null>(null);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  const counts = mismatchCounts(data);
  const inGroup = (group: ReviewScope) =>
    data.items.filter((item) => reviewScope(item) === group);
  const items = inGroup('needsReview');
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
          ...(item.managedResources?.length
            ? { resourceIds: selected[item.key] ?? [] }
            : {}),
        }),
      });
      if (next.action !== 'cleanup' || next.key !== item.key)
        throw new ApiError(
          'Cleanup response does not match the requested app.',
          409,
          'cleanupMismatch',
        );
      const requested = selected[item.key];
      if (
        requested?.length &&
        (new Set(next.effects.map((effect) => effect.resourceId)).size !==
          requested.length ||
          next.effects.some((effect) => !requested.includes(effect.resourceId)))
      )
        throw new ApiError(
          'Cleanup effects do not match the selected resources.',
          409,
          'cleanupSelectionMismatch',
        );
      if (alive.current) {
        planObservation.current = data;
        setPlan(next);
      }
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
            Stale or drifted records only. Select stale reservations or proxies,
            review exact UUIDs, then run cleanup manually. Containers that are
            simply not configured yet, or have an importable existing
            configuration, are grouped below and are not counted.
          </p>
        </div>
        <Badge>{counts.needsReview} need review</Badge>
      </div>
      {!data.inventory.complete && (
        <Notice>
          Inventory is incomplete. Unknown records cannot be classified as
          orphans or cleaned up.
        </Notice>
      )}
      {counts.needsReview === 0 && (
        <Empty title="No stale or drifted records">
          <p>
            No dashboard-owned resource is stale, changed, missing or in
            conflict in the latest observed inventory. Refresh to check for new
            provider changes.
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
          const cleanupVerdict = cleanupEligibility(item, data);
          const canCleanup = cleanupVerdict.allowed;
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
              <ResourceDrift item={item} />
              {canCleanup && !!item.managedResources?.length && (
                <fieldset disabled={!!busy || !!plan}>
                  <legend>Select stale resources to remove</legend>
                  {item.managedResources.map((resource) => (
                    <label className="check" key={resource.id}>
                      <input
                        type="checkbox"
                        checked={
                          selected[item.key]?.includes(resource.id) ?? false
                        }
                        onChange={(event) =>
                          setSelected((previous) => ({
                            ...previous,
                            [item.key]: event.target.checked
                              ? [...(previous[item.key] ?? []), resource.id]
                              : (previous[item.key] ?? []).filter(
                                  (id) => id !== resource.id,
                                ),
                          }))
                        }
                      />
                      <span>
                        {resource.kind === 'reservation'
                          ? 'Remove stale reservation'
                          : 'Remove stale proxy resource'}{' '}
                        · {resource.kind} · <code>{resource.id}</code>
                        <br />
                        {resource.name}
                      </span>
                    </label>
                  ))}
                  <p className="muted">
                    Select linked proxy resources with their reservation and
                    domain. Retained or foreign references block deletion.
                    Already absent selected UUIDs only lose their ownership
                    record.
                  </p>
                </fieldset>
              )}
              <div className="actions">
                {container && (
                  <button
                    className="button"
                    onClick={() => onInspect(item.key)}
                  >
                    Add missing / review configuration
                    <ArrowRight size={16} />
                  </button>
                )}
                {!container && (
                  <button
                    className="button danger-text"
                    disabled={
                      !canCleanup ||
                      !!busy ||
                      !!plan ||
                      (!!item.managedResources?.length &&
                        !selected[item.key]?.length)
                    }
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
                <p className="disabled-reason">{reasonOf(cleanupVerdict)}</p>
              )}
            </article>
          );
        })}
      </div>
      <SetupGroup
        title="Not configured"
        description="No reservation or proxy exists yet. This is a starting point, not drift."
        action="Configure"
        items={inGroup('notConfigured')}
        data={data}
        settings={settings}
        onInspect={onInspect}
      />
      <SetupGroup
        title="Import available"
        description="An unambiguous existing configuration can be reviewed and imported. Nothing is imported automatically."
        action="Review existing configuration"
        items={inGroup('importAvailable')}
        data={data}
        settings={settings}
        onInspect={onInspect}
      />
      <SetupGroup
        title="Unverified"
        description="Provider state could not be read, so these records are neither confirmed nor ruled out. Refresh to verify."
        action="Inspect"
        items={inGroup('unverified')}
        data={data}
        settings={settings}
        onInspect={onInspect}
      />
      <ExternalCleanup api={api} data={data} onRefresh={onRefresh} />
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
          blockedReason={
            planObservation.current !== data || !data.inventory.complete
              ? 'Observations changed after review. Close this plan and review the current selection.'
              : ''
          }
          onClose={() => setPlan(null)}
          onDone={onRefresh}
        />
      )}
    </div>
  );
}

function SetupGroup({
  title,
  description,
  action,
  items,
  data,
  settings,
  onInspect,
}: {
  title: string;
  description: string;
  action: string;
  items: ReconcileItem[];
  data: Reconciliation;
  settings: Settings;
  onInspect: (key: string) => void;
}) {
  if (items.length === 0) return null;
  return (
    <section className="setup-group" aria-label={title}>
      <div className="title-line">
        <h2>{title}</h2>
        <Badge>{items.length}</Badge>
      </div>
      <p className="muted">{description}</p>
      <ul className="setup-list">
        {items.map((item) => {
          const container = data.inventory.containers.find(
            (c) => c.key === item.key,
          );
          const host = settings.hosts.find(
            (h) => h.id === item.key.split(':')[0],
          );
          return (
            <li key={item.key}>
              <div>
                <strong>{container?.name || item.key}</strong>
                <span className="muted">
                  {host?.name || item.key.split(':')[0]}
                  {container ? ` · VMID ${container.vmid}` : ''}
                </span>
              </div>
              {container && (
                <button className="button" onClick={() => onInspect(item.key)}>
                  {action}
                  <ArrowRight size={16} />
                </button>
              )}
            </li>
          );
        })}
      </ul>
    </section>
  );
}
