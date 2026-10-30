import { useEffect, useRef, useState } from 'react';
import { ArrowClockwise, Check } from '@phosphor-icons/react';
import { Api, messageOf } from './api';
import {
  jobLogsSchema,
  jobSchema,
  operationSchema,
  type Job,
  type Operation,
} from './schemas';
import { Badge, Empty, Notice, Status } from './ui';
import { activeJob, formatTime, stateLabel, uncertainJob } from './viewmodel';

function JobEntry({
  api,
  job,
  onRefresh,
}: {
  api: Api;
  job: Job;
  onRefresh: () => Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [logs, setLogs] = useState(job.logs);
  const [truncated, setTruncated] = useState(job.truncated);
  const [open, setOpen] = useState(false);
  const [reviewed, setReviewed] = useState(false);
  useEffect(() => {
    setLogs(job.logs);
    setTruncated(job.truncated);
  }, [job.logs, job.truncated]);
  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    api
      .request(`/jobs/${encodeURIComponent(job.id)}/logs`, jobLogsSchema, {
        signal: controller.signal,
      })
      .then((value) => {
        if (!controller.signal.aborted) {
          setLogs(value.logs);
          setTruncated(value.truncated);
          setError('');
        }
      })
      .catch((err) => {
        if (!controller.signal.aborted)
          setError(messageOf(err instanceof Error ? err : null));
      });
    return () => controller.abort();
  }, [api, job.id, job.updatedAt, open]);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  async function acknowledge() {
    setBusy(true);
    setError('');
    try {
      await api.request(
        `/jobs/${encodeURIComponent(job.id)}/acknowledge`,
        jobSchema,
        { method: 'POST', body: '{}' },
      );
      setReviewed(false);
    } catch (err) {
      if (alive.current) setError(messageOf(err instanceof Error ? err : null));
    } finally {
      await onRefresh();
      if (alive.current) setBusy(false);
    }
  }
  return (
    <article className="activity-entry">
      <div className="section-heading">
        <div>
          <h3>
            {stateLabel(job.kind)} <span className="muted">{job.key}</span>
          </h3>
          <p className="muted">
            {formatTime(job.createdAt)} · Job <code>{job.id}</code>
          </p>
        </div>
        <Status value={job.status} />
      </div>
      {activeJob(job) && (
        <p className="muted">
          {job.status === 'queued'
            ? 'Waiting for an available worker.'
            : 'Executing on the server. Only bounded, sanitized phases are exposed.'}
        </p>
      )}
      {job.error && <Notice tone="danger">{job.error}</Notice>}
      <details onToggle={(event) => setOpen(event.currentTarget.open)}>
        <summary>Execution phases</summary>
        <p className="muted">
          Remote command output is withheld to protect secrets.
        </p>
        <pre className="job-output">
          {logs.length ? logs.join('\n') : 'No phases recorded yet.'}
        </pre>
        {truncated && (
          <p className="muted">
            Earlier phases were truncated by the server’s bounded history.
          </p>
        )}
      </details>
      {uncertainJob(job) && (
        <div className="inline-review">
          <h4>Inspect the container before another update</h4>
          <p>
            {job.status === 'rebootRequired'
              ? 'Check package state and arrange any required reboot in Proxmox. The dashboard never reboots containers.'
              : 'The remote outcome is uncertain. Inspect package or application state and running processes in the container before requesting another update.'}{' '}
            Acknowledgement unlocks future explicit requests. It does not rerun
            this job or certify success.
          </p>
          <label className="check">
            <input
              type="checkbox"
              checked={reviewed}
              onChange={(event) => setReviewed(event.target.checked)}
            />
            I checked the container and understand the outcome
          </label>
          <button
            className="button"
            disabled={!reviewed || busy}
            onClick={() => {
              void acknowledge();
            }}
          >
            <Check size={16} />
            {busy ? 'Acknowledging…' : 'Acknowledge inspected outcome'}
          </button>
        </div>
      )}
      {job.acknowledged && <Badge>Outcome acknowledged</Badge>}
      {error && (
        <Notice tone="danger" role="alert">
          {error}
        </Notice>
      )}
    </article>
  );
}
function OperationEntry({
  api,
  operation,
  onRefresh,
}: {
  api: Api;
  operation: Operation;
  onRefresh: () => Promise<void>;
}) {
  const [review, setReview] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  async function recover() {
    setBusy(true);
    setError('');
    try {
      await api.request(
        `/operations/${encodeURIComponent(operation.id)}/recover`,
        operationSchema,
        { method: 'POST', body: '{}' },
      );
    } catch (err) {
      if (alive.current) setError(messageOf(err instanceof Error ? err : null));
    } finally {
      await onRefresh();
      if (alive.current) {
        setBusy(false);
        setReview(false);
      }
    }
  }
  return (
    <article className="activity-entry">
      <div className="section-heading">
        <div>
          <h3>
            {operation.action === 'cleanup'
              ? 'Owned resource cleanup'
              : 'Network configuration'}{' '}
            <span className="muted">{operation.key}</span>
          </h3>
          <p className="muted">
            {formatTime(operation.createdAt)} · Operation{' '}
            <code>{operation.id}</code>
          </p>
        </div>
        <Status value={operation.status} />
      </div>
      <p>
        Last recorded step:{' '}
        <strong>{operation.step || 'Intent recorded'}</strong>
      </p>
      {operation.error && <Notice tone="danger">{operation.error}</Notice>}
      {operation.resources.length > 0 && (
        <details>
          <summary>Recorded resource IDs</summary>
          <ul className="resource-list">
            {operation.resources.map((resource, i) => (
              <li key={`${resource.id}-${i}`}>
                {resource.kind}: <code>{resource.id}</code>
                {!resource.fingerprint && (
                  <Badge tone="warning">Readback unverified</Badge>
                )}
              </li>
            ))}
          </ul>
        </details>
      )}
      {operation.status !== 'succeeded' &&
        operation.status !== 'running' &&
        (operation.manualRecovery || operation.step !== 'reconfigure' ? (
          <Notice>
            <strong>Manual recovery required</strong>
            <p>
              Inspect this operation’s recorded UUIDs in OPNsense and verify
              current container identity in Proxmox. A write outcome or resource
              readback is uncertain. Keep conflicting mutations blocked; do not
              create or delete resources by name or IP. Preserve the operation
              journal for operator recovery.
            </p>
          </Notice>
        ) : (
          <div className="recovery">
            <p className="muted">
              The server can retry service application after checking recorded
              resource identities. It will refuse if any mutation is uncertain.
            </p>
            <button
              className="button"
              disabled={busy}
              onClick={() => setReview(true)}
            >
              <ArrowClockwise size={16} />
              Review service recovery
            </button>
            {review && (
              <div className="inline-review">
                <p>
                  Retry KEA/Caddy service application for the recorded
                  mutations? This does not automatically recreate resources or
                  adopt matching names.
                </p>
                <div className="actions">
                  <button
                    className="button primary"
                    disabled={busy}
                    onClick={() => {
                      void recover();
                    }}
                  >
                    {busy
                      ? 'Recovering and refreshing…'
                      : 'Retry recorded service application'}
                  </button>
                  <button
                    className="button"
                    disabled={busy}
                    onClick={() => setReview(false)}
                  >
                    Keep blocked
                  </button>
                </div>
              </div>
            )}
          </div>
        ))}
      {error && (
        <Notice tone="danger" role="alert">
          {error}
        </Notice>
      )}
    </article>
  );
}
export function Activity({
  api,
  jobs,
  operations,
  loaded,
  error,
  onRefresh,
}: {
  api: Api;
  jobs: Job[];
  operations: Operation[];
  loaded: boolean;
  error: string;
  onRefresh: () => Promise<void>;
}) {
  return (
    <div className="activity-page">
      <div className="page-heading">
        <div>
          <h1>Activity</h1>
          <p>Durable network operations and container maintenance.</p>
        </div>
        <button
          className="button"
          onClick={() => {
            void onRefresh();
          }}
        >
          <ArrowClockwise size={16} />
          Refresh history
        </button>
      </div>
      {error && (
        <Notice tone="danger" role="alert">
          {error}
        </Notice>
      )}
      {!loaded && (
        <Notice>
          Complete activity history is unavailable. Refresh before assuming
          there are no active or blocked operations.
        </Notice>
      )}
      {loaded && jobs.length === 0 && operations.length === 0 && (
        <Empty title="No operations yet">
          <p>
            Reviewed network changes and explicit app or OS updates will appear
            here, including failures and recovery actions.
          </p>
        </Empty>
      )}
      {operations.length > 0 && (
        <section>
          <h2>Network operations</h2>
          {[...operations]
            .sort((a, b) => b.createdAt.localeCompare(a.createdAt))
            .map((operation) => (
              <OperationEntry
                key={operation.id}
                api={api}
                operation={operation}
                onRefresh={onRefresh}
              />
            ))}
        </section>
      )}
      {jobs.length > 0 && (
        <section>
          <h2>Maintenance jobs</h2>
          {jobs.map((job) => (
            <JobEntry key={job.id} api={api} job={job} onRefresh={onRefresh} />
          ))}
        </section>
      )}
    </div>
  );
}
