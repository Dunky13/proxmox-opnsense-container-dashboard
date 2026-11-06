import type {
  Job,
  MaintenanceReadiness,
  Reconciliation,
  ReconcileItem,
  Container,
} from './schemas';
import {
  activeJob,
  hasIssueCode,
  inventoryExpired,
  uncertainJob,
  vmidOutsideProfiles,
  type ContainerView,
  type JobAttemptState,
} from './viewmodel';

// The single place that decides whether a container may run an action. Cards,
// rows, the inspector and Mismatches render these verdicts and never re-derive
// them, so the same container shows the same reason everywhere. This module only
// consolidates rules: where the server and the client disagreed, the stricter
// rule stays. Transient UI state (a click in flight, form validity) is not
// eligibility and stays with the component.

export type ActionId =
  | 'networkChange'
  | 'addReservation'
  | 'import'
  | 'accept'
  | 'appUpdate'
  | 'osUpdate'
  | 'readinessCheck'
  | 'cleanup';
export type Eligibility =
  { allowed: true } | { allowed: false; reason: string };
export type ActionEligibility = Record<ActionId, Eligibility>;
export type EligibilityContext = {
  data: Reconciliation;
  jobs: Job[];
  // False while Activity is loading or failed: job state is then unknown.
  activityLoaded: boolean;
  // An update request that was sent but whose outcome is not yet inspected.
  jobAttemptState?: JobAttemptState;
  // Latest maintenance readiness check for this container, if any. It is only
  // used while it still matches the container generation and is fresh.
  readiness?: MaintenanceReadiness | null;
  now: number;
};

export const readinessFreshnessMs = 60_000;

const verdict = (reason: string): Eligibility =>
  reason ? { allowed: false, reason } : { allowed: true };
export const reasonOf = (value: Eligibility): string =>
  value.allowed ? '' : value.reason;

export function currentReadiness(
  readiness: MaintenanceReadiness | null | undefined,
  container: Container,
  now: number,
): MaintenanceReadiness | null {
  return readiness &&
    readiness.identity === container.identity &&
    readiness.fingerprint === container.fingerprint &&
    now - Date.parse(readiness.observedAt) < readinessFreshnessMs
    ? readiness
    : null;
}

const unfinishedOperation = (data: Reconciliation, key: string) =>
  data.operations.some((o) => o.key === key && o.status !== 'succeeded');
const busyJob = (jobs: Job[], key: string) =>
  jobs.some((j) => j.key === key && (activeJob(j) || uncertainJob(j)));

function networkReason(view: ContainerView, data: Reconciliation): string {
  if (!data.inventory.complete)
    return 'Complete inventory is required. Resolve host errors and refresh.';
  if (!view.item || view.item.state === 'unknown')
    return 'Provider state is unknown. Resolve the inventory or firewall read failure and refresh.';
  if (view.item.state === 'conflict')
    return view.item.importAvailable
      ? 'Review and import the existing configuration before planning a network change.'
      : 'Resolve the conflicting identity or unowned provider resources before planning.';
  if (!view.container.identity)
    return 'Container generation is unverified. Add a unique container-dashboard-generation UUID marker in its Proxmox description.';
  if (unfinishedOperation(data, view.container.key))
    return 'An incomplete network operation needs recovery in Activity.';
  return '';
}

function reservationReason(view: ContainerView, data: Reconciliation): string {
  if (view.stale)
    return 'Refresh current inventory before repairing a reservation.';
  // A reservation can be repaired while the rest of the record is missing.
  const reservationView: ContainerView = view.item?.canAddReservation
    ? { ...view, item: { ...view.item, state: 'missing' } }
    : view;
  const network = networkReason(reservationView, data);
  if (view.item?.canAddReservation === true) return network;
  const server = view.item?.reservationBlocked ?? '';
  if (network)
    return server && server !== network ? `${server} ${network}` : network;
  return server || 'Reservation repair is unavailable.';
}

function importReason(view: ContainerView, ctx: EligibilityContext): string {
  const { data, jobs, activityLoaded, jobAttemptState, now } = ctx;
  const c = view.container;
  if (
    view.stale ||
    !data.inventory.complete ||
    inventoryExpired(data.inventory.observedAt, now)
  )
    return 'Current complete inventory is required. Refresh before reviewing an import.';
  if (!c.identity)
    return 'Container generation is unverified. Complete identity enrollment and refresh before import.';
  if (!view.host || !view.firewall || !c.ip || !c.mac || c.template)
    return 'A verified container, address and linked host/firewall profile are required before import.';
  if (
    vmidOutsideProfiles(view) ||
    (c.expectedIp && c.expectedIp !== c.ip) ||
    (c.expectedMac && c.expectedMac.toLowerCase() !== c.mac.toLowerCase()) ||
    hasIssueCode(view.item, 'hostProfileMismatch')
  )
    return 'Container addresses do not match the configured host profile. Resolve the mismatch before import.';
  if (!activityLoaded)
    return 'Current activity history is required. Refresh Activity before reviewing an import.';
  if (jobAttemptState || busyJob(jobs, c.key))
    return 'An update is active or needs outcome inspection in Activity before import.';
  if (unfinishedOperation(data, c.key))
    return 'An incomplete network operation needs recovery in Activity before import.';
  if (
    !view.item?.importAvailable ||
    view.item.owned ||
    view.item.state === 'unknown' ||
    !view.item.existingRoute ||
    !view.item.existingResources?.length ||
    view.item.importIssues?.length
  )
    return (
      view.item?.importIssues?.join(' ') ||
      'No unambiguous matching configuration is available for import. Inspect the existing records and refresh.'
    );
  return '';
}

function acceptReason(view: ContainerView, data: Reconciliation): string {
  if (!view.item?.canAcceptChanges)
    return (
      view.item?.acceptBlocked ||
      'Accepting observed changes is unavailable. Resolve unfinished operations, refresh and try again.'
    );
  return networkReason(view, data);
}

function updateReason(
  view: ContainerView,
  ctx: EligibilityContext,
  kind: Job['kind'],
): string {
  const { jobs, activityLoaded, jobAttemptState, now } = ctx;
  if (jobAttemptState)
    return 'Inspect the previous update request in container details before another review.';
  if (!activityLoaded)
    return 'Current job state is unavailable. Refresh Activity before starting an update.';
  const ready = currentReadiness(ctx.readiness, view.container, now);
  // Fresh SSH inspection replaces the inventory's distribution guess.
  const c = ready?.connected
    ? {
        ...view.container,
        distribution: ready.distribution,
        version: ready.version,
        distroSource: 'ssh',
      }
    : view.container;
  if (view.stale)
    return 'Host inventory is stale. Refresh before requesting an update.';
  if (c.template) return 'Templates cannot be updated.';
  if (c.status !== 'running')
    return 'Updates require a running container. Start it in Proxmox, then refresh.';
  if (!c.identity)
    return 'Container generation is unverified. Add the required generation marker in Proxmox, then refresh.';
  if (!view.host?.ssh.enabled)
    return 'SSH updates are not enabled for this host. Configure server-side SSH in Settings.';
  if (busyJob(jobs, c.key))
    return 'An update is active or needs acknowledgement in Activity.';
  if (
    kind === 'osUpdate' &&
    !['debian', 'ubuntu', 'alpine'].includes(c.distribution.toLowerCase())
  )
    return 'OS update support is unknown. The server supports Debian, Ubuntu and Alpine after SSH inspection.';
  if (!ready) return '';
  return (
    ready.reason || (kind === 'appUpdate' ? ready.appReason : ready.osReason)
  );
}

function readinessCheckReason(view: ContainerView): string {
  if (view.stale)
    return 'Host inventory is stale. Refresh before checking maintenance readiness.';
  if (view.container.status !== 'running')
    return 'Readiness checks require a running container. Start it in Proxmox, then refresh.';
  if (!view.container.identity)
    return 'Container generation is unverified. Add the required generation marker in Proxmox, then refresh.';
  return '';
}

// Cleanup targets owned records whose container is gone, so it is evaluated
// from the reconciled item and works for orphans that have no container view.
export function cleanupEligibility(
  item: ReconcileItem,
  data: Reconciliation,
): Eligibility {
  if (!item.owned || !(item.state === 'ownedOrphan' || item.cleanupSelectable))
    return verdict(
      'Cleanup requires a confirmed owned orphan. Unknown, conflicting and ambiguous records are protected. Eligible unowned records have a separate acknowledged review below.',
    );
  if (!data.inventory.complete)
    return verdict(
      'Cleanup requires complete fresh inventory. Resolve host errors and refresh.',
    );
  if (unfinishedOperation(data, item.key))
    return verdict(
      'Cleanup is unavailable while a network operation is incomplete. Inspect it in Activity.',
    );
  return verdict('');
}

export function eligibility(
  view: ContainerView,
  ctx: EligibilityContext,
): ActionEligibility {
  const { data } = ctx;
  return {
    networkChange: verdict(networkReason(view, data)),
    addReservation: verdict(reservationReason(view, data)),
    import: verdict(importReason(view, ctx)),
    accept: verdict(acceptReason(view, data)),
    appUpdate: verdict(updateReason(view, ctx, 'appUpdate')),
    osUpdate: verdict(updateReason(view, ctx, 'osUpdate')),
    readinessCheck: verdict(readinessCheckReason(view)),
    cleanup: view.item
      ? cleanupEligibility(view.item, data)
      : verdict('No provider record is available for cleanup.'),
  };
}
