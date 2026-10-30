import { useEffect, useRef, useState } from 'react';
import {
  ArrowSquareOut,
  ArrowUp,
  HardDrives,
  LockSimple,
  X,
} from '@phosphor-icons/react';
import { Api, ApiError, messageOf } from './api';
import {
  jobSchema,
  operationSchema,
  planSchema,
  schemeSchema,
  type Job,
  type Plan,
  type Reconciliation,
} from './schemas';
import { AppIcon, Badge, Field, Notice, Status } from './ui';
import {
  ensureBlocked,
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
}: {
  api: Api;
  plan: Plan;
  onDone: () => Promise<void>;
  onClose: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [clock, setClock] = useState(Date.now());
  const [publicReviewed, setPublicReviewed] = useState(false);
  const [consumed, setConsumed] = useState(false);
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
  async function apply() {
    setBusy(true);
    setError('');
    let succeeded = false;
    try {
      const operation = await api.request(
        `/plans/${encodeURIComponent(plan.id)}/apply`,
        operationSchema,
        { method: 'POST', body: '{}' },
      );
      succeeded = operation.status === 'succeeded';
      if (mounted.current && !succeeded)
        setError(
          `Operation ${operation.status}. Inspect Activity before any further change.`,
        );
    } catch (err) {
      if (mounted.current)
        setError(
          `${messageOf(err instanceof Error ? err : null)} The response may have been lost after a write. Review Activity and refreshed state before creating a new plan.`,
        );
    } finally {
      // Apply may fail after a provider mutation. Never infer rollback from HTTP failure.
      await onDone();
      if (mounted.current) {
        setBusy(false);
        setConsumed(true);
        if (succeeded) onClose();
      }
    }
  }
  return (
    <section className="plan-review" aria-label="Review network plan">
      <div className="section-heading">
        <h3 ref={heading} tabIndex={-1}>
          {plan.action === 'cleanup'
            ? 'Review owned resource cleanup'
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
        {plan.action === 'cleanup'
          ? 'Deletes only the resources listed below.'
          : `${plan.desiredRoute.scheme.toUpperCase()} upstream, port ${plan.desiredRoute.port}.`}
      </p>
      {plan.action === 'ensure' && (
        <Badge tone={plan.public ? 'warning' : 'good'}>
          {plan.public ? 'Public internet access' : 'LAN / VPN allowlist'}
        </Badge>
      )}
      <p className="muted">
        Config revision {plan.revision}. Expires {formatTime(plan.expiresAt)}.
        The server checks fresh identity and provider state again on apply.
      </p>
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
                      <span className="change-before">
                        {change.before || '(empty)'}
                      </span>
                      <span aria-hidden="true"> → </span>
                      <span className="sr-only"> changes to </span>
                      <strong>{change.after || '(empty)'}</strong>
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
      {plan.public && plan.action === 'ensure' && (
        <label className="check">
          <input
            type="checkbox"
            checked={publicReviewed}
            onChange={(event) => setPublicReviewed(event.target.checked)}
            disabled={busy || consumed}
          />
          I reviewed the public exposure. This removes the private allowlist
          attachment for this domain.
        </label>
      )}
      {expired && (
        <Notice>
          This plan has expired. Close it and review a fresh plan.
        </Notice>
      )}
      {consumed && (
        <Notice>
          Apply was attempted. Review refreshed Activity before requesting a new
          plan. This plan cannot be submitted again here.
        </Notice>
      )}
      {error && (
        <Notice tone="danger" role="alert">
          {error}
        </Notice>
      )}
      <button
        className={
          plan.action === 'cleanup' ? 'button danger-button' : 'button primary'
        }
        disabled={
          busy ||
          expired ||
          consumed ||
          plan.effects.length === 0 ||
          (plan.public && plan.action === 'ensure' && !publicReviewed)
        }
        onClick={() => {
          void apply();
        }}
      >
        {busy
          ? 'Applying and refreshing…'
          : plan.action === 'cleanup'
            ? 'Delete listed owned resources'
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
}: {
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
  const routeUnavailable =
    !!view.item?.routeUnavailable || (!!view.item?.owned && !persisted);
  const [scheme, setScheme] = useState(
    persisted?.scheme ?? (routeUnavailable ? '' : 'http'),
  );
  const [port, setPort] = useState(
    persisted ? String(persisted.port) : routeUnavailable ? '' : '80',
  );
  const [isPublic, setPublic] = useState(persisted?.public ?? false);
  const [plan, setPlan] = useState<Plan | null>(null);
  const reviewButton = useRef<HTMLButtonElement | null>(null);
  const previousPlan = useRef<Plan | null>(null);
  useEffect(() => {
    if (previousPlan.current && !plan) reviewButton.current?.focus();
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
  const parsedPort = Number(port);
  const validScheme = schemeSchema.safeParse(scheme).success;
  const validPort =
    /^\d+$/.test(port) &&
    Number.isInteger(parsedPort) &&
    parsedPort >= 1 &&
    parsedPort <= 65535;
  const networkBusy = busy || !!plan;
  async function review() {
    setBusy(true);
    setError('');
    try {
      const next = await api.request('/plans', planSchema, {
        method: 'POST',
        body: JSON.stringify({
          action: 'ensure',
          hostId: c.hostId,
          vmid: c.vmid,
          public: isPublic,
          upstreamScheme: scheme,
          upstreamPort: parsedPort,
        }),
      });
      if (
        next.key !== c.key ||
        next.identity !== c.identity ||
        next.action !== 'ensure'
      ) {
        throw new ApiError(
          'Container identity changed while preparing the plan. Refreshed inventory must be inspected before requesting a new plan.',
          409,
          'identityChanged',
        );
      }
      if (alive.current) setPlan(next);
    } catch (err) {
      if (alive.current) setError(messageOf(err instanceof Error ? err : null));
      await onRefresh();
    } finally {
      if (alive.current) setBusy(false);
    }
  }
  async function startJob(kind: Job['kind']) {
    if (
      jobAttemptConsumed.current ||
      jobAttemptState ||
      !activityLoaded ||
      updateBlocked(view, jobs, kind)
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
        <Status value={view.item?.state ?? 'unknown'} />
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
          <dt>Desired access</dt>
          <dd>{view.exposure}</dd>
        </div>
        {persisted && (
          <div className="upstream-fact">
            <dt>Desired upstream</dt>
            <dd>
              <code>
                {persisted.scheme}://
                {c.expectedIp || '(expected IP unavailable)'}:{persisted.port}
              </code>
            </dd>
          </div>
        )}
      </dl>
      {[...c.warnings, ...(view.item?.issues ?? [])].length > 0 && (
        <ul className="warnings">
          {[...new Set([...c.warnings, ...(view.item?.issues ?? [])])].map(
            (warning) => (
              <li key={warning}>{warning}</li>
            ),
          )}
        </ul>
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
                  if (result.success) setScheme(result.data);
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
                onChange={(event) => setPort(event.target.value)}
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
              onChange={(event) => setPublic(event.target.value === 'public')}
            >
              <option value="private">LAN / VPN allowlist</option>
              <option value="public">Public internet</option>
            </select>
          )}
        </Field>
        {isPublic && (
          <Notice>
            Public removes the private access restriction on this domain. You
            must acknowledge the concrete plan before applying.
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
            Existing app upstream could not be verified. Choose this app's
            scheme and port explicitly before repairing its route.
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
          onDone={onRefresh}
          onClose={() => setPlan(null)}
        />
      )}
      <section className="detail-section">
        <h3>Maintenance</h3>
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
                : updateBlocked(view, jobs, kind);
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
                  !!updateBlocked(view, jobs, jobToConfirm)
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
