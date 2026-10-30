import { useEffect, useState } from 'react';
import { Check, Warning, X } from '@phosphor-icons/react';
import type { DashboardRead, DashboardReads, ReadState } from './use-dashboard';

const readOrder: DashboardRead[] = [
  'configuration',
  'provider inventory',
  'jobs',
  'operations',
];
const labels: Record<DashboardRead, string> = {
  configuration: 'Settings',
  'provider inventory': 'Containers and proxy configuration',
  jobs: 'Update history',
  operations: 'Operation history',
};
const stateLabels: Record<ReadState, string> = {
  pending: 'Loading',
  loaded: 'Loaded',
  failed: 'Failed',
  cancelled: 'Cancelled',
  superseded: 'Not applied',
};

export function LoadingToast({
  reads,
  refreshId,
  onDismiss,
}: {
  reads: DashboardReads | null;
  refreshId: number;
  onDismiss?: () => void;
}) {
  const [dismissedRefresh, setDismissedRefresh] = useState<number | null>(null);
  const [finishedRefresh, setFinishedRefresh] = useState<number | null>(null);
  const pending =
    !!reads && readOrder.some((read) => reads[read] === 'pending');
  const visible =
    !!reads && dismissedRefresh !== refreshId && finishedRefresh !== refreshId;
  const failed = !!reads && readOrder.some((read) => reads[read] === 'failed');
  const incomplete =
    !!reads &&
    readOrder.some((read) => ['cancelled', 'superseded'].includes(reads[read]));
  useEffect(() => {
    if (!visible || pending) return;
    // Show actual terminal states briefly. Actionable errors remain in the
    // main content after this status-only notification disappears.
    const timer = setTimeout(() => setFinishedRefresh(refreshId), 1200);
    return () => clearTimeout(timer);
  }, [visible, pending, refreshId]);
  const heading = pending
    ? 'Loading dashboard'
    : failed
      ? 'Refresh finished with errors'
      : incomplete
        ? 'Refresh incomplete'
        : 'Refresh complete';
  return (
    <>
      <div
        className="sr-only"
        role="status"
        aria-live="polite"
        aria-atomic="true"
      >
        {visible && reads
          ? `${heading}. ${readOrder.map((read) => `${labels[read]}: ${stateLabels[reads[read]]}`).join('. ')}.`
          : ''}
      </div>
      {visible && reads && (
        <aside className="loading-toast" aria-label="Dashboard loading status">
          <div className="loading-toast-content">
            <strong>{heading}</strong>
            <ul>
              {readOrder.map((read) => {
                const state = reads[read];
                return (
                  <li key={read} className={`loading-read ${state}`}>
                    <div className="loading-read-label">
                      <span>{labels[read]}</span>
                      <span className="loading-read-state">
                        {state === 'loaded' ? (
                          <Check size={14} aria-hidden="true" />
                        ) : state === 'failed' ? (
                          <Warning size={14} aria-hidden="true" />
                        ) : null}
                        {stateLabels[state]}
                      </span>
                    </div>
                    {state === 'pending' ? (
                      <div
                        className="loading-read-bar"
                        role="progressbar"
                        aria-label={`${labels[read]} loading`}
                      >
                        <span />
                      </div>
                    ) : (
                      <div className="loading-read-bar" aria-hidden="true">
                        <span />
                      </div>
                    )}
                  </li>
                );
              })}
            </ul>
          </div>
          <button
            className="icon-button"
            aria-label="Dismiss loading status"
            onClick={() => {
              setDismissedRefresh(refreshId);
              onDismiss?.();
            }}
          >
            <X size={18} aria-hidden="true" />
          </button>
        </aside>
      )}
    </>
  );
}
