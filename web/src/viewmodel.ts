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
export type Filters = {
  query: string;
  host: string;
  state: string;
  distribution: string;
  exposure: string;
};
export const emptyFilters: Filters = {
  query: '',
  host: '',
  state: '',
  distribution: '',
  exposure: '',
};
export function containerViews(
  data: Reconciliation,
  settings: Settings,
): ContainerView[] {
  return data.inventory.containers.map((container) => {
    const host = settings.hosts.find((h) => h.id === container.hostId);
    const firewall = settings.opnsenses.find((o) => o.id === host?.opnsenseId);
    const item = data.items.find((i) => i.key === container.key);
    const route = container.desiredRoute ?? item?.desiredRoute;
    const distribution =
      container.distribution && container.distribution !== 'unknown'
        ? `${container.distribution}${container.version ? ` ${container.version}` : ' · version unknown'}`
        : 'Distribution unknown';
    const domain = firewall ? `${container.name}.${firewall.domainBase}` : '';
    const exposure = route
      ? route.public
        ? 'Public'
        : 'LAN / VPN'
      : 'Not configured';
    const config = item ? stateLabel(item.state) : 'Unknown';
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
      (!filters.exposure || v.exposure === filters.exposure),
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
export function activeJob(job: Job): boolean {
  return job.status === 'queued' || job.status === 'running';
}
export type JobAttemptState = 'requesting' | 'uncertain';
export function uncertainJob(job: Job): boolean {
  return (
    ['timedOut', 'interrupted', 'rebootRequired'].includes(job.status) &&
    !job.acknowledged
  );
}
export function updateBlocked(
  view: ContainerView,
  jobs: Job[],
  kind: Job['kind'],
): string {
  const c = view.container;
  if (view.stale)
    return 'Host inventory is stale. Refresh before requesting an update.';
  if (c.template) return 'Templates cannot be updated.';
  if (c.status !== 'running')
    return 'Updates require a running container. Start it in Proxmox, then refresh.';
  if (!c.identity)
    return 'Container generation is unverified. Add the required generation marker in Proxmox, then refresh.';
  if (!view.host?.ssh.enabled)
    return 'SSH updates are not enabled for this host. Configure server-side SSH in Settings.';
  if (jobs.some((j) => j.key === c.key && (activeJob(j) || uncertainJob(j))))
    return 'An update is active or needs acknowledgement in Activity.';
  if (
    kind === 'osUpdate' &&
    !['debian', 'ubuntu', 'alpine'].includes(c.distribution.toLowerCase())
  )
    return 'OS update support is unknown. The server supports Debian, Ubuntu and Alpine after SSH inspection.';
  return '';
}
export function ensureBlocked(
  view: ContainerView,
  data: Reconciliation,
): string {
  if (!data.inventory.complete)
    return 'Complete inventory is required. Resolve host errors and refresh.';
  if (!view.item || view.item.state === 'unknown')
    return 'Provider state is unknown. Resolve the inventory or firewall read failure and refresh.';
  if (view.item.state === 'conflict')
    return 'Resolve the conflicting identity or unowned provider resources before planning.';
  if (!view.container.identity)
    return 'Container generation is unverified. Add a unique container-dashboard-generation UUID marker in its Proxmox description.';
  if (
    data.operations.some(
      (o) => o.key === view.container.key && o.status !== 'succeeded',
    )
  )
    return 'An incomplete network operation needs recovery in Activity.';
  return '';
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
