import { useEffect, useRef, useState } from 'react';
import { Api, ApiError, messageOf } from './api';
import {
  planSchema,
  type Operation,
  type Plan,
  type Reconciliation,
} from './schemas';
import { PlanReview } from './ContainerDetail';
import { Notice } from './ui';

type Candidate = NonNullable<Reconciliation['protectedCandidates']>[number];
type Target = NonNullable<Operation['externalCleanup']>['targets'][number];

function ExternalGroup({
  api,
  hostId,
  vmid,
  candidates = [],
  recovery,
  observation,
  onRefresh,
  onPrepared,
  onPreparationError,
  reviewActive = false,
}: {
  api: Api;
  hostId: string;
  vmid: number;
  candidates?: Candidate[];
  recovery?: Operation;
  observation: Reconciliation | Operation;
  onRefresh: () => Promise<void>;
  onPrepared?: (plan: Plan, observation: Reconciliation | Operation) => void;
  onPreparationError?: (message: string) => void;
  reviewActive?: boolean;
}) {
  const [selected, setSelected] = useState<string[]>([]);
  const [acknowledged, setAcknowledged] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [plan, setPlan] = useState<Plan | null>(null);
  const planObservation = useRef<Reconciliation | Operation | null>(null);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  useEffect(() => {
    setAcknowledged(false);
    setSelected([]);
  }, [observation]);
  const targets: Target[] =
    recovery?.externalCleanup?.targets ??
    candidates.flatMap((c) =>
      selected.includes(c.resource.id) && c.fingerprint
        ? [
            {
              id: c.resource.id,
              kind: c.resource.kind,
              fingerprint: c.fingerprint,
            },
          ]
        : [],
    );
  async function prepare() {
    if (!acknowledged || !targets.length || busy || plan || reviewActive)
      return;
    const observed = observation;
    setBusy(true);
    setError('');
    onPreparationError?.('');
    try {
      const next = await api.request('/plans', planSchema, {
        method: 'POST',
        body: JSON.stringify({
          action: 'externalCleanup',
          hostId,
          vmid,
          selectedResources: targets,
          externalUseAcknowledged: true,
          ...(recovery ? { recoveryId: recovery.id } : {}),
        }),
      });
      const returned = next.externalCleanup;
      if (
        next.action !== 'externalCleanup' ||
        next.key !== `${hostId}:${vmid}` ||
        !returned ||
        returned.recoveryId !== recovery?.id ||
        returned.targets.length !== targets.length ||
        new Set(returned.targets.map((t) => t.id)).size !== targets.length ||
        returned.targets.some(
          (t) =>
            !targets.some(
              (s) =>
                s.id === t.id &&
                s.kind === t.kind &&
                s.fingerprint === t.fingerprint,
            ),
        )
      ) {
        throw new ApiError(
          'External cleanup response does not match the selected UUIDs, kinds and fingerprints. Refresh before review.',
          409,
          'externalSelectionMismatch',
        );
      }
      if (alive.current) {
        if (onPrepared) onPrepared(next, observed);
        else {
          planObservation.current = observed;
          setPlan(next);
        }
        setAcknowledged(false);
      }
    } catch (err) {
      if (alive.current) {
        const message = messageOf(err instanceof Error ? err : null);
        if (onPreparationError) onPreparationError(message);
        else setError(message);
      }
      await onRefresh();
    } finally {
      if (alive.current) setBusy(false);
    }
  }
  return (
    <div className="mismatch-entry">
      <h3>
        {recovery
          ? 'Inspect and review external cleanup recovery'
          : `${hostId} · absent VMID ${vmid}`}
      </h3>
      {recovery && (
        <Notice>
          This cleanup is incomplete. The server will freshly inspect all
          original UUIDs, deleted-row evidence, complete occupancy and foreign
          references before offering a new single-use plan. Remaining exact rows
          may be deliberately deleted; missing rows retain their evidence. No
          ownership is adopted or generic recovery sent.
        </Notice>
      )}
      <form
        onSubmit={(event) => {
          event.preventDefault();
          void prepare();
        }}
      >
        <fieldset disabled={busy || !!plan || reviewActive}>
          <legend>
            {recovery
              ? 'Original reviewed UUIDs'
              : 'Select unused external records'}
          </legend>
          {recovery ? (
            <ul className="resource-list">
              {targets.map((t) => (
                <li key={t.id}>
                  {t.kind} · <code>{t.id}</code>
                </li>
              ))}
            </ul>
          ) : (
            candidates.map((candidate) => (
              <label className="check" key={candidate.resource.id}>
                <input
                  type="checkbox"
                  name="selectedResource"
                  checked={selected.includes(candidate.resource.id)}
                  onChange={(event) => {
                    setAcknowledged(false);
                    setSelected((previous) =>
                      event.target.checked
                        ? [...previous, candidate.resource.id]
                        : previous.filter((id) => id !== candidate.resource.id),
                    );
                  }}
                />
                <span>
                  {candidate.resource.kind} ·{' '}
                  <code>{candidate.resource.id}</code>
                  <br />
                  {candidate.resource.name}
                </span>
              </label>
            ))
          )}
          <p className="muted">
            Select the app domain or child with every related handle. A
            reservation stays protected while a retained handle uses its IP.
            Shared parents, siblings, ACLs and trust settings are retained.
          </p>
          <label className="check">
            <input
              type="checkbox"
              name="externalUseAcknowledged"
              checked={acknowledged}
              disabled={!targets.length}
              onChange={(event) => setAcknowledged(event.target.checked)}
            />
            I verified these selected UUIDs are unused by external or unmanaged
            clients. I understand deletion does not adopt ownership.
          </label>
          <button
            className="button danger-text"
            type="submit"
            disabled={!acknowledged || !targets.length}
          >
            {busy
              ? 'Inspecting current cleanup state…'
              : recovery
                ? 'Review fresh external cleanup recovery'
                : 'Review selected external cleanup'}
          </button>
        </fieldset>
      </form>
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
            planObservation.current !== observation
              ? 'Observations changed after review. Close this plan and review the current selection.'
              : ''
          }
          onDone={onRefresh}
          onClose={() => setPlan(null)}
        />
      )}
    </div>
  );
}
export function ExternalCleanup({
  api,
  data,
  onRefresh,
}: {
  api: Api;
  data: Reconciliation;
  onRefresh: () => Promise<void>;
}) {
  const [review, setReview] = useState<{
    plan: Plan;
    observation: Reconciliation | Operation;
  } | null>(null);
  const [preparationError, setPreparationError] = useState('');
  const candidates = data.protectedCandidates ?? [];
  const groups = new Map<
    string,
    { hostId: string; vmid: number; candidates: Candidate[] }
  >();
  for (const c of candidates) {
    if (
      !c.eligible ||
      !c.hostId ||
      !c.vmid ||
      !c.fingerprint ||
      !data.inventory.complete
    )
      continue;
    const key = `${c.hostId}:${c.vmid}`;
    const group = groups.get(key) ?? {
      hostId: c.hostId,
      vmid: c.vmid,
      candidates: [],
    };
    group.candidates.push(c);
    groups.set(key, group);
  }
  if (!candidates.length && !review && !preparationError) return null;
  return (
    <section className="detail-section" aria-label="External records">
      <h2>Possible stale external records</h2>
      <p>
        Eligible manually created records can be selected, reviewed and deleted
        after external-use acknowledgement. Unknown or ambiguous records stay
        protected for the reason shown.
      </p>
      {[...groups].map(([key, group]) => (
        <ExternalGroup
          key={key}
          api={api}
          {...group}
          observation={data}
          onRefresh={onRefresh}
          reviewActive={!!review}
          onPrepared={(plan, observation) => setReview({ plan, observation })}
          onPreparationError={setPreparationError}
        />
      ))}
      {candidates
        .filter((c) => !c.eligible)
        .map((candidate) => (
          <details key={`${candidate.opnsenseId}:${candidate.resource.id}`}>
            <summary>
              Review protected {candidate.resource.kind} ·{' '}
              <code>{candidate.resource.id}</code>
            </summary>
            <p>{candidate.resource.summary}</p>
            <p>{candidate.reason}</p>
            <dl>
              {candidate.observations.map((o) => (
                <div key={o.field}>
                  <dt>{o.field}</dt>
                  <dd>{o.before || '(empty)'}</dd>
                </div>
              ))}
            </dl>
          </details>
        ))}
      {preparationError && (
        <Notice tone="danger" role="alert">
          {preparationError}
        </Notice>
      )}
      {review && (
        <PlanReview
          key={review.plan.id}
          api={api}
          plan={review.plan}
          blockedReason={
            review.observation !== data
              ? 'Observations changed after review. Close this plan and review the current selection.'
              : ''
          }
          onDone={onRefresh}
          onClose={() => setReview(null)}
        />
      )}
    </section>
  );
}
export function ExternalRecovery({
  api,
  operation,
  onRefresh,
}: {
  api: Api;
  operation: Operation;
  onRefresh: () => Promise<void>;
}) {
  const address = operation.externalCleanup?.address;
  return address ? (
    <ExternalGroup
      api={api}
      hostId={address.hostId}
      vmid={address.vmid}
      recovery={operation}
      observation={operation}
      onRefresh={onRefresh}
    />
  ) : (
    <Notice>
      External cleanup evidence is unavailable. Keep this operation blocked and
      preserve its journal for inspection.
    </Notice>
  );
}
