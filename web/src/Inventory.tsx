import { useRef, useState } from 'react';
import {
  ArrowRight,
  ArrowSquareOut,
  ArrowUp,
  Cube,
  HardDrives,
  List,
  MagnifyingGlass,
  ShieldCheck,
  SquaresFour,
} from '@phosphor-icons/react';
import { Api } from './api';
import type { Job, Reconciliation, Settings } from './schemas';
import { ContainerDetail } from './ContainerDetail';
import { ContainerIssues } from './ContainerIssues';
import { AppIcon, Badge, Empty, Field, Notice, Status } from './ui';
import {
  filterViews,
  formatTime,
  updateBlocked,
  type ContainerView,
  type Filters,
  type JobAttemptState,
} from './viewmodel';

export type ViewMode = 'cards' | 'rows';
export function InventoryView({
  api,
  settings,
  data,
  views,
  filters,
  setFilters,
  mode,
  setMode,
  selected,
  setSelected,
  jobs,
  activityLoaded,
  onJob,
  onRefresh,
  onSettings,
  onMismatches,
}: {
  api: Api;
  settings: Settings;
  data: Reconciliation;
  views: ContainerView[];
  filters: Filters;
  setFilters: (filters: Filters) => void;
  mode: ViewMode;
  setMode: (mode: ViewMode) => void;
  selected: string | null;
  setSelected: (key: string | null) => void;
  jobs: Job[];
  activityLoaded: boolean;
  onJob: (job: Job) => void;
  onRefresh: () => Promise<void>;
  onSettings: () => void;
  onMismatches: () => void;
}) {
  const selectionTrigger = useRef<HTMLButtonElement | null>(null);
  const [requestedJob, setRequestedJob] = useState<{
    key: string;
    kind: Job['kind'];
  } | null>(null);
  const [jobAttempts, setJobAttempts] = useState<Map<string, JobAttemptState>>(
    () => new Map(),
  );
  const filtered = filterViews(views, filters);
  const current = views.find((view) => view.container.key === selected);
  const mismatches = data.items.filter((item) => item.state !== 'healthy');
  return (
    <div className="inventory-page">
      <div className="page-heading">
        <div>
          <div className="title-line">
            <h1>Containers</h1>
            <Badge>
              {filtered.length} of {views.length}
            </Badge>
          </div>
          <p>
            {filters.host
              ? settings.hosts.find((h) => h.id === filters.host)?.name ||
                filters.host
              : 'All hosts'}{' '}
            · Observed {formatTime(data.inventory.observedAt)}
          </p>
        </div>
        {mismatches.length > 0 && (
          <button className="button warning-text" onClick={onMismatches}>
            {mismatches.length} need review
            <ArrowRight size={16} />
          </button>
        )}
      </div>
      {!data.inventory.complete && (
        <Notice>
          Inventory is incomplete. Available records remain visible. Resolve
          host errors before network changes.
        </Notice>
      )}
      {settings.hosts.length === 0 ? (
        <Empty title="Connect your first host">
          <p>
            Add an OPNsense firewall, then link a Proxmox host to see containers
            and the smallest available VMID.
          </p>
          <button className="button primary" onClick={onSettings}>
            Configure providers
          </button>
        </Empty>
      ) : (
        <>
          <div className="inventory-toolbar">
            <div className="search-field">
              <MagnifyingGlass size={18} aria-hidden="true" />
              <label className="sr-only" htmlFor="container-search">
                Search all container values
              </label>
              <input
                id="container-search"
                type="search"
                placeholder="Search name, VMID, IP, MAC, domain…"
                value={filters.query}
                onChange={(event) =>
                  setFilters({ ...filters, query: event.target.value })
                }
              />
            </div>
            <div
              className="view-switch"
              role="group"
              aria-label="Inventory view"
            >
              <button
                aria-pressed={mode === 'cards'}
                onClick={() => setMode('cards')}
              >
                <SquaresFour size={17} />
                Cards
              </button>
              <button
                aria-pressed={mode === 'rows'}
                onClick={() => setMode('rows')}
              >
                <List size={17} />
                Rows
              </button>
            </div>
          </div>
          <div className="filters">
            <Field label="Host">
              {(id) => (
                <select
                  id={id}
                  value={filters.host}
                  onChange={(event) =>
                    setFilters({ ...filters, host: event.target.value })
                  }
                >
                  <option value="">All hosts</option>
                  {settings.hosts.map((host) => (
                    <option key={host.id} value={host.id}>
                      {host.name}
                    </option>
                  ))}
                </select>
              )}
            </Field>
            <Field label="State">
              {(id) => (
                <select
                  id={id}
                  value={filters.state}
                  onChange={(event) =>
                    setFilters({ ...filters, state: event.target.value })
                  }
                >
                  <option value="">All states</option>
                  {[...new Set(views.map((v) => v.container.status))]
                    .sort()
                    .map((state) => (
                      <option key={state} value={state}>
                        {state}
                      </option>
                    ))}
                </select>
              )}
            </Field>
            <Field label="Distribution">
              {(id) => (
                <select
                  id={id}
                  value={filters.distribution}
                  onChange={(event) =>
                    setFilters({ ...filters, distribution: event.target.value })
                  }
                >
                  <option value="">All distributions</option>
                  {[
                    ...new Set(
                      views.map((v) =>
                        v.container.distribution &&
                        v.container.distribution !== 'unknown'
                          ? v.container.distribution
                          : '__unknown__',
                      ),
                    ),
                  ]
                    .sort()
                    .map((distribution) => (
                      <option key={distribution} value={distribution}>
                        {distribution === '__unknown__'
                          ? 'Unknown'
                          : distribution}
                      </option>
                    ))}
                </select>
              )}
            </Field>
            <Field label="Exposure">
              {(id) => (
                <select
                  id={id}
                  value={filters.exposure}
                  onChange={(event) =>
                    setFilters({ ...filters, exposure: event.target.value })
                  }
                >
                  <option value="">All access policies</option>
                  <option>LAN / VPN</option>
                  <option>Public</option>
                  <option>Not configured</option>
                </select>
              )}
            </Field>
          </div>
          <div
            className={`inventory-layout ${current ? 'has-selection' : ''} ${mode}`}
          >
            <div className="host-groups">
              {filtered.length === 0 && (
                <Empty
                  title={
                    views.length === 0
                      ? 'No LXC containers observed'
                      : 'No matching containers'
                  }
                >
                  <p>
                    {views.length === 0
                      ? 'Configured hosts may contain only QEMU guests, or inventory may be incomplete. Check each host below.'
                      : 'Try another search or clear the filters. Your selected container remains available.'}
                  </p>
                  {views.length > 0 && (
                    <button
                      className="button"
                      onClick={() =>
                        setFilters({
                          query: '',
                          host: '',
                          state: '',
                          distribution: '',
                          exposure: '',
                        })
                      }
                    >
                      Clear search and filters
                    </button>
                  )}
                </Empty>
              )}
              {settings.hosts
                .filter((host) => !filters.host || host.id === filters.host)
                .map((host) => {
                  const containers = filtered.filter(
                    (v) => v.container.hostId === host.id,
                  );
                  const inventory = data.inventory.hosts.find(
                    (item) => item.hostId === host.id,
                  );
                  const firewall = settings.opnsenses.find(
                    (o) => o.id === host.opnsenseId,
                  );
                  return (
                    <section className="host-group" key={host.id}>
                      <div className="host-heading">
                        <div className="host-identity">
                          <h2>{host.name}</h2>
                          <Badge
                            tone={inventory?.complete ? 'good' : 'warning'}
                          >
                            {inventory?.complete
                              ? 'Inventory complete'
                              : 'Inventory unknown'}
                          </Badge>
                        </div>
                        <div className="host-links">
                          {firewall && (
                            <a
                              href={firewall.baseUrl}
                              target="_blank"
                              rel="noreferrer"
                            >
                              <ShieldCheck size={15} />
                              {firewall.name} · OPNsense
                              <ArrowSquareOut size={13} />
                            </a>
                          )}
                          <span className="muted">
                            {formatTime(
                              inventory?.observedAt ??
                                data.inventory.observedAt,
                            )}
                          </span>
                        </div>
                      </div>
                      {inventory?.error && (
                        <Notice tone="danger">{inventory.error}</Notice>
                      )}
                      {!inventory && (
                        <Notice>
                          No host inventory was returned. Free VMIDs are
                          unknown.
                        </Notice>
                      )}
                      {containers.length > 0 ? (
                        <div
                          className={
                            mode === 'cards'
                              ? 'container-grid'
                              : 'container-rows'
                          }
                        >
                          {containers.map((view) => (
                            <ContainerRecord
                              key={view.container.key}
                              view={view}
                              mode={mode}
                              selected={view.container.key === selected}
                              jobs={jobs}
                              activityLoaded={activityLoaded}
                              jobAttemptState={jobAttempts.get(
                                `${view.container.key}:${view.container.identity}`,
                              )}
                              onSelect={(trigger, kind) => {
                                selectionTrigger.current = trigger;
                                setRequestedJob(
                                  kind
                                    ? { key: view.container.key, kind }
                                    : null,
                                );
                                setSelected(view.container.key);
                              }}
                            />
                          ))}
                        </div>
                      ) : (
                        <p className="muted host-empty">
                          {views.some((v) => v.container.hostId === host.id)
                            ? 'No containers match the current filters on this host.'
                            : 'No LXC containers in the latest host inventory.'}
                        </p>
                      )}
                    </section>
                  );
                })}
            </div>
            {current && (
              <ContainerDetail
                key={`${current.container.key}:${current.container.identity}`}
                view={current}
                onSettings={onSettings}
                data={data}
                api={api}
                jobs={jobs}
                activityLoaded={activityLoaded}
                onRefresh={onRefresh}
                onJob={onJob}
                requestedJob={
                  requestedJob?.key === current.container.key
                    ? requestedJob.kind
                    : null
                }
                onJobRequestConsumed={() => setRequestedJob(null)}
                onJobReviewRequest={(kind) =>
                  setRequestedJob({ key: current.container.key, kind })
                }
                jobAttemptState={jobAttempts.get(
                  `${current.container.key}:${current.container.identity}`,
                )}
                onJobAttemptState={(state) =>
                  setJobAttempts((previous) => {
                    const next = new Map(previous);
                    const key = `${current.container.key}:${current.container.identity}`;
                    if (state) next.set(key, state);
                    else next.delete(key);
                    return next;
                  })
                }
                onClose={() => {
                  setSelected(null);
                  setRequestedJob(null);
                  if (selectionTrigger.current?.isConnected) {
                    selectionTrigger.current.focus();
                  } else {
                    document.getElementById('container-search')?.focus();
                  }
                }}
              />
            )}
          </div>
        </>
      )}
    </div>
  );
}
function ContainerRecord({
  view,
  mode,
  selected,
  onSelect,
  jobs,
  activityLoaded,
  jobAttemptState,
}: {
  view: ContainerView;
  mode: ViewMode;
  selected: boolean;
  onSelect: (trigger: HTMLButtonElement, kind?: Job['kind']) => void;
  jobs: Job[];
  activityLoaded: boolean;
  jobAttemptState?: JobAttemptState;
}) {
  const c = view.container;
  const maintenance = (['appUpdate', 'osUpdate'] satisfies Job['kind'][]).map(
    (kind) => ({
      kind,
      reason: jobAttemptState
        ? 'Inspect the previous update request in container details before another review.'
        : activityLoaded
          ? updateBlocked(view, jobs, kind)
          : 'Maintenance status is loading. Refresh Activity if it remains unavailable.',
      label: kind === 'appUpdate' ? 'App update' : 'OS update',
    }),
  );
  const maintenanceReasons = [
    ...new Set(maintenance.map((action) => action.reason).filter(Boolean)),
  ];
  return (
    <article
      className={`container-record ${mode === 'cards' ? 'container-card' : 'container-row'} ${selected ? 'selected' : ''}`}
    >
      <div className="record-identity">
        <AppIcon app={c.app} />
        <div>
          <button
            className="record-name"
            onClick={(event) => onSelect(event.currentTarget)}
            aria-pressed={selected}
            aria-label={`Inspect ${c.name}, VMID ${c.vmid}`}
          >
            {c.name}
          </button>
          <span className="record-id">
            VMID <code>{c.vmid}</code>
            {c.template ? ' · Template' : ''}
          </span>
        </div>
        <Status value={c.status} />
      </div>
      <div className="record-network">
        <code>{c.ip || 'IP unknown'}</code>
        <code className="record-mac">{c.mac || 'MAC unknown'}</code>
      </div>
      <div className="record-distro">{view.distribution}</div>
      {mode === 'cards' && (
        <p className="record-domain">{view.domain || 'Domain unavailable'}</p>
      )}
      <div className="record-state">
        {view.exposure !== 'Not configured' && (
          <Badge tone={view.exposure === 'Public' ? 'warning' : 'neutral'}>
            {view.exposure}
          </Badge>
        )}
        {view.item?.importAvailable ? (
          <Badge tone="warning">Existing configuration</Badge>
        ) : (
          <Status value={view.item?.state ?? 'unknown'} />
        )}
      </div>
      <ContainerIssues view={view} />
      {mode === 'cards' && (
        <div className="record-maintenance">
          {maintenance.map(({ kind, reason, label }) => {
            return (
              <div key={kind}>
                <button
                  className="button"
                  disabled={!!reason}
                  aria-describedby={
                    reason ? `maintenance-status-${c.key}` : undefined
                  }
                  aria-label={`Review ${label.toLowerCase()} ${c.name}, VMID ${c.vmid}`}
                  onClick={(event) => onSelect(event.currentTarget, kind)}
                >
                  {kind === 'appUpdate' ? (
                    <ArrowUp size={15} />
                  ) : (
                    <HardDrives size={15} />
                  )}
                  {label}
                </button>
              </div>
            );
          })}
          {maintenanceReasons.length > 0 && (
            <p id={`maintenance-status-${c.key}`} className="disabled-reason">
              {maintenanceReasons.join(' ')}
            </p>
          )}
        </div>
      )}
      <button
        className="button record-configure"
        onClick={(event) => onSelect(event.currentTarget)}
        aria-label={`Configure ${c.name}, VMID ${c.vmid}`}
      >
        <Cube size={15} />
        Configure
        <ArrowRight size={14} />
      </button>
    </article>
  );
}
