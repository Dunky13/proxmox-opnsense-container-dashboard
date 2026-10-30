import { useEffect, useRef, useState } from 'react';
import {
  ArrowSquareOut,
  ArrowUp,
  HardDrives,
  LockSimple,
  X,
} from '@phosphor-icons/react';
import { Api, ApiError, messageOf } from './api';
import { ContainerIssues } from './ContainerIssues';
import {
  jobSchema,
  operationSchema,
  planSchema,
  schemeSchema,
  maintenanceSchema,
  type MaintenanceReadiness,
  type Job,
  type Plan,
  type Reconciliation,
} from './schemas';
import { AppIcon, Badge, Field, Notice, Status } from './ui';
import {
  ensureBlocked,
  importBlocked,
  formatTime,
  updateBlocked,
  type ContainerView,
  type JobAttemptState,
  activeJob,
  uncertainJob,
} from './viewmodel';

export function PlanReview({
  api,
  plan,
  onDone,
  onClose,
  blockedReason = '',
}: {
  api: Api;
  plan: Plan;
  onDone: () => Promise<void>;
  onClose: () => void;
  blockedReason?: string;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [clock, setClock] = useState(Date.now());
  const [publicReviewed, setPublicReviewed] = useState(false);
  const [ownershipReviewed, setOwnershipReviewed] = useState(false);
  const [externalReviewed, setExternalReviewed] = useState(false);
  const [consumed, setConsumed] = useState(false);
  const attempted = useRef(false);
  const mounted = useRef(true);
  const heading = useRef<HTMLHeadingElement | null>(null);
  useEffect(() => {
    mounted.current = true;
    heading.current?.focus();
    const timer = setInterval(() => setClock(Date.now()), 1000);
    return () => {
      mounted.current = false;
      clearInterval(timer);
    };
  }, []);
  const expired =
    !Number.isFinite(Date.parse(plan.expiresAt)) ||
    clock >= Date.parse(plan.expiresAt);
  const importing = plan.action === 'import';
  const accepting = plan.action === 'accept';
  const reservationOnly = plan.action === 'reservation';
  const externalCleanup = plan.action === 'externalCleanup';
  const deleting = plan.action === 'cleanup' || externalCleanup;
  const exposureReview = plan.public && !deleting && !reservationOnly;
  const invalidExternal =
    externalCleanup &&
    (!plan.externalCleanup ||
      !plan.externalCleanup.targets.length ||
      plan.effects.length !== plan.externalCleanup.targets.length ||
      new Set(plan.effects.map((effect) => effect.resourceId)).size !==
        plan.effects.length ||
      plan.effects.some(
        (effect) =>
          !['delete', 'retain'].includes(effect.action) ||
          !plan.externalCleanup?.targets.some(
            (target) =>
              target.id === effect.resourceId && target.kind === effect.kind,
          ),
      ));
  const invalidImport =
    importing &&
    (!plan.effects.some((effect) => effect.action === 'adopt') ||
      !plan.effects.every(
        (effect) => effect.action === 'adopt' || effect.action === 'retain',
      ));
  const applyDisabled =
    busy ||
    expired ||
    consumed ||
    !!blockedReason ||
    invalidImport ||
    invalidExternal ||
    plan.effects.length === 0 ||
    (exposureReview && !publicReviewed) ||
    (importing && !ownershipReviewed) ||
    (externalCleanup && !externalReviewed);
  async function apply() {
    if (attempted.current || applyDisabled) return;
    attempted.current = true;
    setConsumed(true);
    setOwnershipReviewed(false);
    setExternalReviewed(false);
    setPublicReviewed(false);
    setBusy(true);
    setError('');
    let succeeded = false;
    try {
      const operation = await api.request(
        `/plans/${encodeURIComponent(plan.id)}/apply`,
        operationSchema,
        { method: 'POST', body: '{}' },
      );
      if (
        (importing || externalCleanup) &&
        (operation.planId !== plan.id ||
          operation.key !== plan.key ||
          operation.action !== plan.action ||
          operation.opnsenseId !== plan.opnsenseId)
      )
        throw new ApiError(
          'Returned operation does not match this review. Inspect Activity before another attempt.',
          409,
          'operationMismatch',
        );
      succeeded = operation.status === 'succeeded';
      if (mounted.current && !succeeded)
        setError(
          `Operation ${operation.status}. Inspect Activity before any further change.`,
        );
    } catch (err) {
      if (mounted.current)
        setError(
          `${messageOf(err instanceof Error ? err : null)} ${importing ? 'The response may have been lost after ownership was recorded.' : 'The response may have been lost after a write.'} Review Activity and refreshed state before creating a new plan. No automatic retry was sent.`,
        );
    } finally {
      // Apply may fail after a provider mutation. Never infer rollback from HTTP failure.
      try {
        await onDone();
      } catch {
        succeeded = false;
        if (mounted.current)
          setError(
            'The outcome refresh failed. Inspect Activity and refresh current state before requesting a new plan.',
          );
      }
      if (mounted.current) {
        setBusy(false);
        setConsumed(true);
        if (succeeded) onClose();
      }
    }
  }
  return (
    <section
      className="plan-review"
      aria-label={
        importing ? 'Review existing configuration plan' : 'Review network plan'
      }
    >
      <div className="section-heading">
        <h3 ref={heading} tabIndex={-1}>
          {importing
            ? 'Review existing configuration'
            : externalCleanup
              ? 'Review external resource cleanup'
              : plan.action === 'cleanup'
                ? 'Review owned resource cleanup'
                : accepting
                  ? 'Accept observed changes'
                  : reservationOnly
                    ? 'Review missing reservation'
                    : 'Review network change'}
        </h3>
        <button
          className="icon-button"
          onClick={onClose}
          disabled={busy}
          aria-label="Close plan"
        >
          <X size={18} />
        </button>
      </div>
      <p>
        <strong>{plan.key}</strong> ·{' '}
        {deleting
          ? 'Deletes only the resources listed below.'
          : reservationOnly
            ? 'Adds only the selected missing reservation.'
            : accepting
              ? 'Acknowledges the reviewed hostname in dashboard ownership only.'
              : `${plan.desiredRoute.scheme.toUpperCase()} upstream, port ${plan.desiredRoute.port}.`}
      </p>
      {importing && (
        <Notice>
          Import records ownership in Container Proxy only. It sends no provider
          create, update, delete or service-apply requests. Existing fields,
          UUIDs, upstream and access remain unchanged. Future reviewed
          maintenance and cleanup can update or delete the imported app
          resources. Shared wildcard parents and shared access lists remain
          externally owned.
        </Notice>
      )}
      {!deleting && !reservationOnly && (
        <Badge tone={plan.public ? 'warning' : 'good'}>
          {plan.public
            ? importing
              ? 'Preserves existing public access'
              : 'Public internet access'
            : importing
              ? 'Preserves existing private access'
              : 'LAN / VPN allowlist'}
        </Badge>
      )}
      <p className="muted">
        Config revision {plan.revision}. Expires {formatTime(plan.expiresAt)}.
        The server checks fresh identity and provider state again on apply.
      </p>
      {externalCleanup && plan.externalCleanup && (
        <details>
          <summary>Review identity bindings</summary>
          <p>
            {plan.externalCleanup.address.hostId} · VMID{' '}
            {plan.externalCleanup.address.vmid} ·{' '}
            <code>{plan.externalCleanup.address.ip}</code> ·{' '}
            <code>{plan.externalCleanup.address.mac}</code>
          </p>
          <ul className="resource-list">
            {plan.externalCleanup.targets.map((target) => (
              <li key={target.id}>
                {target.kind} · <code>{target.id}</code>
                <br />
                Fingerprint: <code>{target.fingerprint}</code>
              </li>
            ))}
          </ul>
        </details>
      )}
      {plan.effects.length === 0 ? (
        <Notice>
          No resource effects were returned. Create a new plan before applying.
        </Notice>
      ) : (
        <div className="plan-effects">
          {plan.effects.map((effect, index) => (
            <div className="effect" key={`${effect.kind}-${index}`}>
              <div>
                <Badge tone={effect.action === 'delete' ? 'danger' : 'neutral'}>
                  {effect.action}
                </Badge>{' '}
                <strong>{effect.kind}</strong>
              </div>
              <p>{effect.name || 'Unnamed resource'}</p>
              {effect.resourceId && <code>{effect.resourceId}</code>}
              <dl>
                {effect.changes.map((change, i) => (
                  <div key={`${change.field}-${i}`}>
                    <dt>{change.field}</dt>
                    <dd>
                      {importing && change.before === change.after ? (
                        <span>
                          {change.before || '(empty)'}{' '}
                          <span className="muted">(preserved)</span>
                        </span>
                      ) : (
                        <>
                          <span className="change-before">
                            {change.before || '(empty)'}
                          </span>
                          <span aria-hidden="true"> → </span>
                          <span className="sr-only"> changes to </span>
                          <strong>{change.after || '(empty)'}</strong>
                        </>
                      )}
                    </dd>
                  </div>
                ))}
              </dl>
            </div>
          ))}
        </div>
      )}
      {plan.applyNotes.length > 0 && (
        <ul className="notes">
          {plan.applyNotes.map((note, i) => (
            <li key={i}>{note}</li>
          ))}
        </ul>
      )}
      {importing && (
        <label className="check">
          <input
            type="checkbox"
            checked={ownershipReviewed}
            onChange={(event) => setOwnershipReviewed(event.target.checked)}
            disabled={busy || consumed}
          />
          I reviewed the exact resource UUIDs and accept managed ownership,
          including future reviewed changes and cleanup of these app resources.
        </label>
      )}
      {externalCleanup && (
        <label className="check">
          <input
            type="checkbox"
            checked={externalReviewed}
            onChange={(event) => setExternalReviewed(event.target.checked)}
            disabled={busy || consumed || !!blockedReason}
          />
          I confirm these exact UUIDs remain unused by external or unmanaged
          clients and approve only the listed deletion and service effects.
        </label>
      )}
      {exposureReview && (
        <label className="check">
          <input
            type="checkbox"
            checked={publicReviewed}
            onChange={(event) => setPublicReviewed(event.target.checked)}
            disabled={busy || consumed}
          />
          {accepting
            ? 'I acknowledge the reviewed configuration preserves existing public access.'
            : importing
              ? 'I acknowledge this import preserves existing public access. It does not add or remove an access restriction.'
              : 'I reviewed the public exposure. This removes the private allowlist attachment for this domain.'}
        </label>
      )}
      {expired && (
        <Notice>
          This plan has expired. Close it and review a fresh plan.
        </Notice>
      )}
      {consumed && !busy && (
        <Notice>
          Apply was attempted. Review refreshed Activity before requesting a new
          plan. This plan cannot be submitted again here.
        </Notice>
      )}
      {blockedReason && <Notice>{blockedReason}</Notice>}
      {invalidImport && (
        <Notice>
          Import returned a provider-changing effect. Close this plan and
          inspect refreshed state before another review.
        </Notice>
      )}
      {invalidExternal && (
        <Notice>
          External cleanup effects do not match the exact reviewed UUIDs and
          kinds. Close this plan and inspect refreshed state.
        </Notice>
      )}
      {error && (
        <Notice tone="danger" role="alert">
          {error}
        </Notice>
      )}
      <button
        className={deleting ? 'button danger-button' : 'button primary'}
        disabled={applyDisabled}
        onClick={() => {
          void apply();
        }}
      >
        {busy
          ? importing
            ? 'Importing and refreshing…'
            : 'Applying and refreshing…'
          : importing
            ? 'Import reviewed configuration'
            : externalCleanup
              ? 'Delete listed external resources'
              : plan.action === 'cleanup'
                ? 'Delete listed owned resources'
                : accepting
                  ? 'Accept reviewed hostname'
                  : reservationOnly
                    ? 'Add reviewed reservation'
                    : 'Apply reviewed change'}
      </button>
    </section>
  );
}

export function ContainerDetail({
  view,
  data,
  api,
  jobs,
  activityLoaded,
  onRefresh,
  onJob,
  onClose,
  requestedJob = null,
  onJobRequestConsumed,
  onJobReviewRequest,
  jobAttemptState,
  onJobAttemptState,
  onSettings,
}: {
  onSettings?: () => void;
  view: ContainerView;
  data: Reconciliation;
  api: Api;
  jobs: Job[];
  activityLoaded: boolean;
  onRefresh: () => Promise<void>;
  onJob: (job: Job) => void;
  onClose: () => void;
  requestedJob?: Job['kind'] | null;
  onJobRequestConsumed?: () => void;
  onJobReviewRequest: (kind: Job['kind']) => void;
  jobAttemptState?: JobAttemptState;
  onJobAttemptState: (state: JobAttemptState | null) => void;
}) {
  const c = view.container;
  const persisted = c.desiredRoute ?? view.item?.desiredRoute;
  const observedRoute = view.item?.existingRoute;
  const usingObservedRoute = !view.item?.owned && !!observedRoute;
  const initialRoute = usingObservedRoute
    ? observedRoute
    : (persisted ?? observedRoute);
  const routeUnavailable =
    !!view.item?.routeUnavailable ||
    (!!view.item?.owned && !persisted) ||
    (!!view.item?.existingResources?.length && !initialRoute);
  const [scheme, setScheme] = useState(
    initialRoute?.scheme ?? (routeUnavailable ? '' : 'http'),
  );
  const [port, setPort] = useState(
    initialRoute ? String(initialRoute.port) : '',
  );
  const [isPublic, setPublic] = useState(initialRoute?.public ?? false);
  const routeDraftEdited = useRef(false);
  useEffect(() => {
    if (routeDraftEdited.current || !initialRoute) return;
    setScheme(initialRoute.scheme);
    setPort(String(initialRoute.port));
    setPublic(initialRoute.public);
  }, [initialRoute]);
  const [plan, setPlan] = useState<Plan | null>(null);
  const reviewButton = useRef<HTMLButtonElement | null>(null);
  const importButton = useRef<HTMLButtonElement | null>(null);
  const previousPlan = useRef<Plan | null>(null);
  const importObservation = useRef<Reconciliation | null>(null);
  const preparing = useRef(false);
  const planObservation = useRef<Reconciliation | null>(null);
  useEffect(() => {
    if (previousPlan.current && !plan)
      (previousPlan.current.action === 'import' && importButton.current
        ? importButton.current
        : reviewButton.current
      )?.focus();
    previousPlan.current = plan;
  }, [plan]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const jobToConfirm = requestedJob;
  const jobAttemptConsumed = useRef(false);
  const maintenanceReview = useRef<HTMLHeadingElement | null>(null);
  const [jobBusy, setJobBusy] = useState(false);
  const alive = useRef(true);
  const title = useRef<HTMLHeadingElement | null>(null);
  useEffect(() => {
    alive.current = true;
    title.current?.focus({ preventScroll: true });
    if (window.innerWidth < 1280)
      title.current?.scrollIntoView?.({ block: 'start' });
    return () => {
      alive.current = false;
    };
  }, []);
  useEffect(() => {
    if (jobToConfirm) {
      jobAttemptConsumed.current = false;
      maintenanceReview.current?.focus({ preventScroll: true });
      maintenanceReview.current?.scrollIntoView?.({ block: 'nearest' });
    }
  }, [jobToConfirm]);
  const block = ensureBlocked(view, data);
  const reservationView: ContainerView = view.item?.canAddReservation
    ? { ...view, item: { ...view.item, state: 'missing' } }
    : view;
  const reservationBlock = view.stale
    ? 'Refresh current inventory before repairing a reservation.'
    : ensureBlocked(reservationView, data) ||
      (view.item?.canAddReservation === false
        ? view.item.reservationBlocked || 'Reservation repair is unavailable.'
        : '');
  const importBlock = importBlocked(
    view,
    data,
    jobs,
    activityLoaded,
    jobAttemptState,
  );
  const importContext = useRef({ data, block: importBlock });
  importContext.current = { data, block: importBlock };
  const showImport =
    !view.item?.owned &&
    (!!view.item?.importAvailable ||
      !!view.item?.existingResources?.length ||
      !!view.item?.importIssues?.length);
  const parsedPort = Number(port);
  const validScheme = schemeSchema.safeParse(scheme).success;
  const validPort =
    /^\d+$/.test(port) &&
    Number.isInteger(parsedPort) &&
    parsedPort >= 1 &&
    parsedPort <= 65535;
  const networkBusy = busy || !!plan;
  async function review(
    action: 'ensure' | 'import' | 'accept' | 'reservation' = 'ensure',
  ) {
    if (
      preparing.current ||
      plan ||
      (action === 'import' && importBlock) ||
      (action === 'reservation' && reservationBlock)
    )
      return;
    preparing.current = true;
    const observation = data;
    setBusy(true);
    setError('');
    try {
      const next = await api.request('/plans', planSchema, {
        method: 'POST',
        body: JSON.stringify({
          action,
          hostId: c.hostId,
          vmid: c.vmid,
          ...(action === 'ensure'
            ? {
                public: isPublic,
                upstreamScheme: scheme,
                upstreamPort: parsedPort,
              }
            : action === 'accept'
              ? { public: persisted?.public ?? false }
              : {}),
        }),
      });
      if (
        next.key !== c.key ||
        next.identity !== c.identity ||
        next.action !== action
      ) {
        throw new ApiError(
          'Container identity changed while preparing the plan. Refreshed inventory must be inspected before requesting a new plan.',
          409,
          'identityChanged',
        );
      }
      if (
        action === 'import' &&
        (next.hostId !== c.hostId ||
          next.vmid !== c.vmid ||
          next.opnsenseId !== view.firewall?.id ||
          next.public !== observedRoute?.public ||
          next.desiredRoute.public !== observedRoute?.public ||
          next.desiredRoute.scheme !== observedRoute?.scheme ||
          next.desiredRoute.port !== observedRoute?.port)
      )
        throw new ApiError(
          'The returned import does not match this container and its observed route. Inspect refreshed state before requesting a new plan.',
          409,
          'importMismatch',
        );
      if (
        action === 'import' &&
        (importContext.current.data !== observation ||
          importContext.current.block)
      )
        throw new ApiError(
          'Observations changed while preparing the import. Inspect refreshed state and request a new plan.',
          409,
          'staleImport',
        );
      importObservation.current = action === 'import' ? observation : null;
      if (alive.current) {
        planObservation.current = observation;
        setPlan(next);
      }
    } catch (err) {
      if (alive.current) setError(messageOf(err instanceof Error ? err : null));
      await onRefresh();
    } finally {
      preparing.current = false;
      if (alive.current) setBusy(false);
    }
  }
  async function startJob(kind: Job['kind']) {
    if (
      jobAttemptConsumed.current ||
      jobAttemptState ||
      !activityLoaded ||
      updateBlocked(maintenanceView, jobs, kind) ||
      capabilityReason(kind)
    )
      return;
    jobAttemptConsumed.current = true;
    onJobRequestConsumed?.();
    onJobAttemptState('requesting');
    setJobBusy(true);
    setError('');
    try {
      const job = await api.request('/jobs', jobSchema, {
        method: 'POST',
        body: JSON.stringify({
          hostId: c.hostId,
          vmid: c.vmid,
          kind,
          fingerprint: c.fingerprint,
        }),
      });
      if (
        job.key !== c.key ||
        job.kind !== kind ||
        job.fingerprint !== c.fingerprint
      ) {
        throw new ApiError(
          'Returned job does not match this update request. Inspect Activity before another attempt.',
          409,
          'jobMismatch',
        );
      }
      onJob(job);
      onJobAttemptState(null);
    } catch (err) {
      onJobAttemptState('uncertain');
      if (alive.current)
        setError(
          `${err instanceof ApiError && err.status > 0 ? `Server detail: ${err.message} ` : ''}The update request outcome is unknown. Inspect Activity and the container before acknowledging this attempt. No automatic retry was sent.`,
        );
      await onRefresh();
    } finally {
      if (alive.current) setJobBusy(false);
    }
  }
  const [readiness, setReadiness] = useState<MaintenanceReadiness | null>(null);
  const [checkingMaintenance, setCheckingMaintenance] = useState(false);
  useEffect(() => {
    if (!readiness) return;
    const remaining = Date.parse(readiness.observedAt) + 60000 - Date.now();
    if (!Number.isFinite(remaining) || remaining <= 0) {
      setReadiness(null);
      return;
    }
    const timer = setTimeout(() => {
      setReadiness((current) => (current === readiness ? null : current));
    }, remaining);
    return () => clearTimeout(timer);
  }, [readiness]);
  const currentReadiness =
    readiness?.identity === c.identity &&
    readiness.fingerprint === c.fingerprint &&
    Date.now() - Date.parse(readiness.observedAt) < 60000
      ? readiness
      : null;
  const maintenanceView: ContainerView = currentReadiness?.connected
    ? {
        ...view,
        container: {
          ...c,
          distribution: currentReadiness.distribution,
          version: currentReadiness.version,
          distroSource: 'ssh',
        },
      }
    : view;
  function capabilityReason(kind: Job['kind']) {
    if (!currentReadiness) return '';
    return (
      currentReadiness.reason ||
      (kind === 'appUpdate'
        ? currentReadiness.appReason
        : currentReadiness.osReason)
    );
  }
  async function checkMaintenance() {
    if (checkingMaintenance) return;
    setCheckingMaintenance(true);
    setReadiness(null);
    setError('');
    try {
      const result = await api.request(
        `/hosts/${encodeURIComponent(c.hostId)}/containers/${c.vmid}/maintenance`,
        maintenanceSchema,
        { method: 'POST', body: '{}' },
      );
      if (
        result.key !== c.key ||
        result.identity !== c.identity ||
        result.fingerprint !== c.fingerprint
      )
        throw new ApiError(
          'Container changed during maintenance check. Refresh inventory.',
          409,
          'staleMaintenance',
        );
      if (alive.current) setReadiness(result);
    } catch (err) {
      if (alive.current) setError(messageOf(err instanceof Error ? err : null));
    } finally {
      if (alive.current) setCheckingMaintenance(false);
    }
  }
  const containerJobs = jobs.filter((job) => job.key === c.key);
  return (
    <aside className="inspector" aria-label={`Details for ${c.name}`}>
      <div className="inspector-title">
        <AppIcon app={c.app} />
        <div>
          <h2 ref={title} tabIndex={-1}>
            {c.name}
          </h2>
          <span className="muted">
            VMID <code>{c.vmid}</code> · {view.host?.name ?? c.hostId}
          </span>
        </div>
        <button
          className="icon-button"
          onClick={onClose}
          aria-label="Close container details"
          disabled={busy || jobBusy}
        >
          <X size={18} />
        </button>
      </div>
      <div className="badges">
        <Status value={c.status} />
        {view.item?.importAvailable ? (
          <Badge tone="warning">Existing configuration</Badge>
        ) : (
          <Status value={view.item?.state ?? 'unknown'} />
        )}
        {c.template && <Badge>Template</Badge>}
      </div>
      {view.domain &&
        (view.item?.state === 'healthy' ? (
          <a
            className="domain"
            href={`https://${view.domain}`}
            target="_blank"
            rel="noreferrer"
          >
            {view.domain}
            <ArrowSquareOut size={14} />
          </a>
        ) : (
          <p className="muted domain">Proposed domain: {view.domain}</p>
        ))}
      <dl className="facts">
        <div>
          <dt>Observed IP address</dt>
          <dd>
            <code>{c.ip || 'Unknown'}</code>
          </dd>
        </div>
        {c.expectedIp && c.expectedIp !== c.ip && (
          <div>
            <dt>Expected IP address</dt>
            <dd>
              <code>{c.expectedIp}</code>
            </dd>
          </div>
        )}
        <div>
          <dt>Observed MAC address</dt>
          <dd>
            <code>{c.mac || 'Unknown'}</code>
          </dd>
        </div>
        {c.expectedMac &&
          c.expectedMac.toLowerCase() !== c.mac.toLowerCase() && (
            <div>
              <dt>Expected MAC address</dt>
              <dd>
                <code>{c.expectedMac}</code>
              </dd>
            </div>
          )}
        <div>
          <dt>Distribution</dt>
          <dd>{view.distribution}</dd>
        </div>
        <div>
          <dt>Source</dt>
          <dd>{c.distroSource || 'Unavailable'}</dd>
        </div>
        <div>
          <dt>App identity</dt>
          <dd>
            {c.app && c.app !== 'unknown' ? `${c.app} (inferred)` : 'Unknown'}
          </dd>
        </div>
        <div>
          <dt>{usingObservedRoute ? 'Observed access' : 'Desired access'}</dt>
          <dd>{view.exposure}</dd>
        </div>
        {initialRoute && (
          <div className="upstream-fact">
            <dt>
              {usingObservedRoute ? 'Observed upstream' : 'Desired upstream'}
            </dt>
            <dd>
              <code>
                {initialRoute.scheme}://
                {usingObservedRoute
                  ? c.ip
                  : c.expectedIp || '(expected IP unavailable)'}
                :{initialRoute.port}
              </code>
            </dd>
          </div>
        )}
      </dl>
      <ContainerIssues view={view} expandedResources />
      {showImport && (
        <section
          className="detail-section"
          aria-label="Existing configuration import"
        >
          <h3>Manage existing configuration</h3>
          <p>
            Review the exact reservation and app route before recording managed
            ownership. Import preserves their current values and access,
            including any stale DHCP hostname. It sends no provider writes.
          </p>
          {importBlock && (
            <p className="disabled-reason" id={`import-block-${c.key}`}>
              {importBlock}
            </p>
          )}
          <button
            className="button primary"
            ref={importButton}
            disabled={!!importBlock || networkBusy}
            aria-describedby={importBlock ? `import-block-${c.key}` : undefined}
            onClick={() => {
              void review('import');
            }}
          >
            {busy ? 'Preparing plan…' : 'Review existing configuration'}
          </button>
        </section>
      )}
      <section className="detail-section">
        <h3>Network configuration</h3>
        <p className="muted">
          Review a plan before changing KEA and Caddy. LAN / VPN is the default
          for new routes.
        </p>
        <div className="form-grid two">
          <Field label="Upstream scheme">
            {(id) => (
              <select
                id={id}
                value={scheme}
                disabled={networkBusy}
                onChange={(event) => {
                  const result = schemeSchema.safeParse(event.target.value);
                  if (result.success) {
                    routeDraftEdited.current = true;
                    setScheme(result.data);
                  }
                }}
              >
                {routeUnavailable && (
                  <option value="" disabled>
                    Choose this app's scheme
                  </option>
                )}
                <option value="http">HTTP</option>
                <option value="https">HTTPS</option>
              </select>
            )}
          </Field>
          <Field label="Upstream port" help="1–65535">
            {(id, helpId) => (
              <input
                id={id}
                aria-describedby={helpId}
                inputMode="numeric"
                type="number"
                min={1}
                max={65535}
                value={port}
                disabled={networkBusy}
                onChange={(event) => {
                  routeDraftEdited.current = true;
                  setPort(event.target.value);
                }}
              />
            )}
          </Field>
        </div>
        <Field label="Access policy">
          {(id) => (
            <select
              id={id}
              value={isPublic ? 'public' : 'private'}
              disabled={networkBusy}
              onChange={(event) => {
                routeDraftEdited.current = true;
                setPublic(event.target.value === 'public');
              }}
            >
              <option value="private">LAN / VPN allowlist</option>
              <option value="public">Public internet</option>
            </select>
          )}
        </Field>
        {isPublic && (
          <Notice>
            {showImport
              ? 'Import preserves the existing public access. A separate reviewed network change is required to change access.'
              : 'Public removes the private access restriction on this domain. You must acknowledge the concrete plan before applying.'}
          </Notice>
        )}
        {!isPublic && (
          <p className="muted">
            <LockSimple size={14} aria-hidden="true" />{' '}
            {view.firewall?.privateAccessListId ? (
              <>
                Default shared private policy:{' '}
                {view.item?.privateAccessList?.name ||
                  view.firewall.privateAccessListId}
                .{' '}
                {view.item?.privateAccessList?.cidrs.join(', ') ||
                  'Definition unavailable.'}{' '}
                {view.item?.privateAccessList?.reason} Shared policy remains
                unchanged and excluded from cleanup. Review verifies its current
                definition.
              </>
            ) : (
              <>
                Dashboard-managed allowed networks:{' '}
                {view.firewall?.allowlistCidrs.join(', ') ||
                  'None configured. Set explicit LAN / VPN CIDRs in Settings.'}
              </>
            )}
          </p>
        )}
        <p className="muted">
          KEA subnet:{' '}
          {view.item?.resolvedSubnet?.cidr ||
            'Resolved automatically from the intended IPv4 during review.'}
        </p>
        {block && <p className="disabled-reason">{block}</p>}
        {routeUnavailable && !persisted && (
          <Notice>
            {showImport
              ? 'Existing app upstream could not be verified safely. Resolve the exact import blockers before reviewing ownership or a network change.'
              : "Existing app upstream could not be verified. Choose this app's scheme and port explicitly before repairing its route."}
          </Notice>
        )}
        {(!validPort || !validScheme) && (
          <p className="field-error">
            Choose HTTP or HTTPS and enter an upstream port from 1 to 65535.
          </p>
        )}
        <button
          className="button primary"
          ref={reviewButton}
          disabled={!!block || !validPort || !validScheme || networkBusy}
          onClick={() => {
            void review();
          }}
        >
          {busy ? 'Preparing plan…' : 'Review network change'}
        </button>
      </section>
      {plan && (
        <PlanReview
          key={plan.id}
          api={api}
          plan={plan}
          blockedReason={
            plan.action === 'import'
              ? importBlock ||
                (importObservation.current !== data
                  ? 'Observations changed after review. Close this plan, inspect refreshed state and request a new plan.'
                  : '')
              : view.stale ||
                  !data.inventory.complete ||
                  planObservation.current !== data
                ? 'Observations changed after review. Close this plan and review refreshed state.'
                : plan.action === 'reservation'
                  ? reservationBlock
                  : block
          }
          onDone={async () => {
            if (plan.action === 'import') {
              routeDraftEdited.current = false;
              setScheme(plan.desiredRoute.scheme);
              setPort(String(plan.desiredRoute.port));
              setPublic(plan.desiredRoute.public);
            }
            await onRefresh();
          }}
          onClose={() => setPlan(null)}
        />
      )}
      <section className="detail-section" aria-label="Repair missing records">
        <h3>Repair missing records</h3>
        <p className="muted">
          Review the exact resource effects before applying. Reservation-only
          repair requires no app port. Proxy configuration requires this app's
          scheme and port above.
        </p>
        <div className="actions">
          <button
            className="button"
            disabled={networkBusy || !!reservationBlock}
            title={reservationBlock || undefined}
            onClick={() => {
              void review('reservation');
            }}
          >
            Add missing reservation
          </button>
          <button
            className="button"
            disabled={networkBusy || !!block || !validScheme || !validPort}
            onClick={() => {
              void review('ensure');
            }}
          >
            Add missing proxy / repair configuration
          </button>
          {view.item?.canAcceptChanges && (
            <button
              className="button"
              disabled={networkBusy || !!block}
              onClick={() => {
                void review('accept');
              }}
            >
              Review observed hostname change
            </button>
          )}
        </div>
        {view.item?.drift?.length && view.item.acceptBlocked ? (
          <p className="disabled-reason">
            Acknowledgement blocked: {view.item.acceptBlocked}
          </p>
        ) : null}
      </section>
      <section className="detail-section">
        <h3>Maintenance</h3>
        <div className="actions">
          <button
            className="button"
            disabled={
              checkingMaintenance ||
              view.stale ||
              c.status !== 'running' ||
              !c.identity
            }
            onClick={() => {
              void checkMaintenance();
            }}
          >
            {checkingMaintenance
              ? 'Checking SSH and commands…'
              : 'Check maintenance readiness'}
          </button>
          {onSettings && (
            <button className="button" onClick={onSettings}>
              Open maintenance settings
            </button>
          )}
        </div>
        <p className="muted">
          Use a dedicated key restricted by the supplied forced-command wrapper.
          Verify the host key, TCP 22 access from the dashboard and server-side
          key paths before enabling SSH. Network drift does not determine update
          availability.
        </p>
        {currentReadiness && (
          <div role="status">
            {currentReadiness.connected ? (
              <p>
                {currentReadiness.source === 'demo'
                  ? 'Simulated maintenance'
                  : 'SSH connected'}{' '}
                · {currentReadiness.distribution}{' '}
                {currentReadiness.version || 'version unavailable'} · source:{' '}
                {currentReadiness.source === 'ssh'
                  ? 'SSH'
                  : currentReadiness.source}{' '}
                · checked {formatTime(currentReadiness.observedAt)}. App update:{' '}
                {currentReadiness.appUpdate ? 'available' : 'unavailable'}. OS
                update:{' '}
                {currentReadiness.osUpdate ? 'supported' : 'unsupported'}.
              </p>
            ) : (
              <Notice>{currentReadiness.reason}</Notice>
            )}
          </div>
        )}

        <p className="muted">
          No version check is available. Each action requests a real server job.
          App updates run the container’s existing /usr/bin/update; OS updates
          use its supported package manager.
        </p>
        <div className="actions">
          {(['appUpdate', 'osUpdate'] satisfies Job['kind'][]).map((kind) => {
            const reason = jobAttemptState
              ? 'Inspect the previous update request before another review.'
              : !activityLoaded
                ? 'Refresh Activity before starting an update; current job state is unavailable.'
                : updateBlocked(maintenanceView, jobs, kind) ||
                  capabilityReason(kind);
            return (
              <div key={kind}>
                <button
                  className="button"
                  disabled={!!reason || jobBusy}
                  onClick={() => onJobReviewRequest(kind)}
                >
                  {kind === 'appUpdate' ? (
                    <ArrowUp size={16} />
                  ) : (
                    <HardDrives size={16} />
                  )}
                  {kind === 'appUpdate' ? 'App update' : 'OS update'}
                </button>
                {reason && <p className="disabled-reason">{reason}</p>}
              </div>
            );
          })}
        </div>
        {jobAttemptState && !jobBusy && (
          <Notice role="status">
            <p>
              {jobAttemptState === 'requesting'
                ? 'An update request is still in progress.'
                : 'An update request was attempted. Inspect Activity and the container to determine its outcome before preparing another review.'}
            </p>
            <button
              className="button"
              disabled={
                jobAttemptState === 'requesting' ||
                !activityLoaded ||
                containerJobs.some((job) => activeJob(job) || uncertainJob(job))
              }
              onClick={() => {
                onJobAttemptState(null);
                setError('');
              }}
            >
              I inspected the update outcome
            </button>
          </Notice>
        )}
        {jobToConfirm && !jobAttemptState && (
          <div className="inline-review">
            <h4 ref={maintenanceReview} tabIndex={-1}>
              Run {jobToConfirm === 'appUpdate' ? 'app' : 'OS'} update on{' '}
              {c.name}?
            </h4>
            <p>
              {jobToConfirm === 'appUpdate'
                ? 'The existing update script may fetch upstream code. Interactive scripts can fail without a terminal.'
                : 'Packages may change and a reboot may be required. The dashboard will not reboot the container.'}{' '}
              This cannot be undone by the dashboard.
            </p>
            <div className="actions">
              <button
                className="button primary"
                disabled={
                  jobBusy ||
                  !activityLoaded ||
                  !!updateBlocked(maintenanceView, jobs, jobToConfirm) ||
                  !!capabilityReason(jobToConfirm)
                }
                onClick={() => {
                  void startJob(jobToConfirm);
                }}
              >
                {jobBusy ? 'Requesting update…' : 'Request update'}
              </button>
              <button
                className="button"
                disabled={jobBusy}
                onClick={() => {
                  onJobRequestConsumed?.();
                }}
              >
                Keep current version
              </button>
            </div>
          </div>
        )}
        {containerJobs.length > 0 && (
          <div className="recent-jobs">
            <h4>Recent updates</h4>
            {containerJobs.slice(0, 3).map((job) => (
              <div key={job.id}>
                <span>
                  {job.kind === 'appUpdate' ? 'App update' : 'OS update'}
                </span>
                <Status value={job.status} />
                {job.error && <p className="field-error">{job.error}</p>}
              </div>
            ))}
            <p className="muted">
              View sanitized execution phases and recovery in Activity.
            </p>
          </div>
        )}
      </section>
      {error && (
        <Notice tone="danger" role="alert">
          {error}
        </Notice>
      )}
    </aside>
  );
}
