import type {
  Container,
  Firewall,
  Host,
  Job,
  ReconcileItem,
  Reconciliation,
  Settings,
} from './schemas';

export type ContainerView = {
  stale: boolean;
  container: Container;
  host?: Host;
  firewall?: Firewall;
  item?: ReconcileItem;
  distribution: string;
  domain: string;
  exposure: string;
  config: string;
  search: string;
};
// Which review group a reconciled record belongs to. Only needsReview is stale
// or drifted. notConfigured, reservationOnly and importAvailable are expected
// starting points; unverified means the provider state could not be read.
export type ReviewScope =
  | 'ok'
  | 'needsReview'
  | 'notConfigured'
  | 'reservationOnly'
  | 'importAvailable'
  | 'unverified';
export type ConfigurationFilter =
  '' | 'needsReview' | 'notConfigured' | 'reservationOnly' | 'importAvailable';
export function reviewScope(item: ReconcileItem): ReviewScope {
  if (item.state === 'healthy') return 'ok';
  if (item.state === 'unknown') return 'unverified';
  if (!item.owned && item.state === 'missing') return 'notConfigured';
  // The server reports these unowned records as a conflict; an unfinished
  // operation turns them into a mismatch, which stays a review item.
  if (!item.owned && item.state === 'conflict') {
    if (item.importAvailable) return 'importAvailable';
    // Hand-made reservation, nothing else found: a starting point, not drift.
    if (item.reservationOnly) return 'reservationOnly';
  }
  return 'needsReview';
}
export type MismatchCounts = Record<Exclude<ReviewScope, 'ok'>, number>;
// Only eligible protected candidates are stale records awaiting a decision. The
// rest are shown for information and never counted.
export const eligibleProtected = (data: Reconciliation) =>
  data.protectedCandidates?.filter((c) => c.eligible === true).length ?? 0;
export const informationalProtected = (data: Reconciliation) =>
  (data.protectedCandidates?.length ?? 0) - eligibleProtected(data);
// The one place that counts records, so the sidebar badge, the inventory
// button and the Mismatches page cannot disagree.
export function mismatchCounts(data: Reconciliation): MismatchCounts {
  const counts: MismatchCounts = {
    needsReview: eligibleProtected(data),
    notConfigured: 0,
    reservationOnly: 0,
    importAvailable: 0,
    unverified: 0,
  };
  for (const item of data.items) {
    const scope = reviewScope(item);
    if (scope !== 'ok') counts[scope] += 1;
  }
  return counts;
}
export type Filters = {
  query: string;
  host: string;
  state: string;
  distribution: string;
  exposure: string;
  configuration: ConfigurationFilter;
};
export const emptyFilters: Filters = {
  query: '',
  host: '',
  state: '',
  distribution: '',
  exposure: '',
  configuration: '',
};
export function containerViews(
  data: Reconciliation,
  settings: Settings,
): ContainerView[] {
  return data.inventory.containers.map((container) => {
    const host = settings.hosts.find((h) => h.id === container.hostId);
    const firewall = settings.opnsenses.find((o) => o.id === host?.opnsenseId);
    const item = data.items.find((i) => i.key === container.key);
    const route =
      !item?.owned && item?.existingRoute
        ? item.existingRoute
        : (container.desiredRoute ?? item?.desiredRoute ?? item?.existingRoute);
    const distribution =
      container.distribution && container.distribution !== 'unknown'
        ? `${container.distribution}${container.version ? ` ${container.version}` : ''}`
        : 'Distribution unknown';
    const domain = firewall ? `${container.name}.${firewall.domainBase}` : '';
    const exposure = route
      ? route.public
        ? 'Public'
        : 'LAN / VPN'
      : 'Not configured';
    const config = item?.importAvailable
      ? 'Existing configuration'
      : item
        ? stateLabel(item.state)
        : 'Unknown';
    const search = [
      container.key,
      container.name,
      container.vmid,
      container.ip,
      container.mac,
      container.expectedIp,
      container.expectedMac,
      container.node,
      container.status,
      container.template ? 'template' : '',
      container.distribution,
      container.version,
      container.distroSource,
      container.app,
      host?.id,
      host?.name,
      host?.baseUrl,
      firewall?.id,
      firewall?.name,
      firewall?.baseUrl,
      distribution,
      domain,
      exposure,
      route?.scheme,
      route?.port,
      config,
      ...container.warnings,
      ...(item?.issues ?? []),
      ...(item?.importIssues ?? []),
      ...(item?.existingResources ?? []).flatMap((resource) => [
        resource.kind,
        resource.id,
        resource.name,
        resource.summary,
      ]),
    ]
      .join(' ')
      .toLocaleLowerCase();
    return {
      stale: !data.inventory.hosts.find((h) => h.hostId === container.hostId)
        ?.complete,
      container,
      host,
      firewall,
      item,
      distribution,
      domain,
      exposure,
      config,
      search,
    };
  });
}
export function filterViews(
  views: ContainerView[],
  filters: Filters,
): ContainerView[] {
  const words = filters.query
    .trim()
    .toLocaleLowerCase()
    .split(/\s+/)
    .filter(Boolean);
  return views.filter(
    (v) =>
      words.every((word) => v.search.includes(word)) &&
      (!filters.host || v.container.hostId === filters.host) &&
      (!filters.state || v.container.status === filters.state) &&
      (!filters.distribution ||
        (filters.distribution === '__unknown__'
          ? !v.container.distribution || v.container.distribution === 'unknown'
          : v.container.distribution === filters.distribution)) &&
      (!filters.exposure || v.exposure === filters.exposure) &&
      (!filters.configuration ||
        (!!v.item && reviewScope(v.item) === filters.configuration)),
  );
}
export function stateLabel(state: string): string {
  const labels: Record<string, string> = {
    healthy: 'Configured',
    missing: 'Not configured',
    mismatch: 'Mismatch',
    conflict: 'Conflict',
    unknown: 'Unknown',
    ownedOrphan: 'Owned orphan',
    running: 'Running',
    stopped: 'Stopped',
    queued: 'Queued',
    succeeded: 'Succeeded',
    partial: 'Partial',
    interrupted: 'Interrupted',
    failed: 'Failed',
    timedOut: 'Timed out',
    rebootRequired: 'Reboot required',
    appUpdate: 'App update',
    osUpdate: 'OS update',
  };
  return labels[state] ?? state;
}
// Address profiles describe the intended addresses; the host's own range is the
// first profile and extra profiles cover other VLANs on the same node.
export function addressProfiles(view: ContainerView) {
  return view.host ? [view.host, ...(view.host.addressProfiles ?? [])] : [];
}
export function vmidOutsideProfiles(view: ContainerView): boolean {
  const profiles = addressProfiles(view);
  return (
    profiles.length > 0 &&
    !profiles.some(
      (profile) =>
        view.container.vmid >= profile.vmidMin &&
        view.container.vmid <= profile.vmidMax,
    )
  );
}
// Inventory older than this is display only; App invalidates it and the
// eligibility rules re-check it at evaluation time.
export const inventoryFreshnessMs = 5 * 60 * 1000;
export function inventoryExpired(observedAt: string, now: number): boolean {
  const observed = Date.parse(observedAt);
  return !Number.isFinite(observed) || now - observed > inventoryFreshnessMs;
}
export function hasIssueCode(
  item: ReconcileItem | undefined,
  code: string,
): boolean {
  return !!item?.issueDetails.some((detail) => detail.code === code);
}
export function activeJob(job: Job): boolean {
  return job.status === 'queued' || job.status === 'running';
}
export type JobAttemptState = 'requesting' | 'uncertain';
export function attemptKey(container: Container): string {
  return `${container.key}:${container.identity}`;
}
export function uncertainJob(job: Job): boolean {
  return (
    ['timedOut', 'interrupted', 'rebootRequired'].includes(job.status) &&
    !job.acknowledged
  );
}
export function formatTime(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.valueOf())
    ? 'Time unknown'
    : date.toLocaleString(undefined, {
        month: 'short',
        day: 'numeric',
        hour: '2-digit',
        minute: '2-digit',
      });
}
