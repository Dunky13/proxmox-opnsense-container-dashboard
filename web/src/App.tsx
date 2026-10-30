import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Dialog } from '@base-ui/react/dialog';
import {
  Pulse as ActivityIcon,
  ArrowClockwise,
  ArrowRight,
  Cube,
  GearSix,
  GlobeHemisphereWest,
  List,
  ComputerTower,
  SignOut,
  Warning,
  X,
} from '@phosphor-icons/react';
import { Api, messageOf } from './api';
import { authSchema, type AuthStatus } from './schemas';
import { InventoryView, type ViewMode } from './Inventory';
import { Activity } from './Activity';
import { Mismatches } from './Mismatches';
import { SettingsEditor } from './SettingsEditor';
import { LoadingToast } from './LoadingToast';
import { useDashboard } from './use-dashboard';
import { invalidateObservations } from './inventory-observations';
import { Badge, Empty, Field, Notice, Skeleton } from './ui';
import {
  containerViews,
  emptyFilters,
  formatTime,
  type Filters,
} from './viewmodel';

export function App() {
  const [token, setToken] = useState('');
  const [auth, setAuth] = useState<AuthStatus | null>(null);
  const [error, setError] = useState('');
  const [checking, setChecking] = useState(true);
  const [attempt, setAttempt] = useState(0);
  const authEpoch = useRef(0);
  const retryAuthentication = useCallback(() => {
    authEpoch.current++;
    setAttempt((old) => old + 1);
  }, []);
  const unauthenticated = useCallback(() => {
    setAuth((old) => (old ? { ...old, authenticated: false } : old));
    setError(
      'Your session has expired or the token was rejected. Sign in to continue.',
    );
  }, []);
  const api = useMemo(() => {
    const epoch = authEpoch.current;
    return new Api(token, () => {
      if (epoch === authEpoch.current) unauthenticated();
    });
  }, [token, attempt, unauthenticated]);
  useEffect(() => {
    const controller = new AbortController();
    setChecking(true);
    api
      .request('/auth/status', authSchema, { signal: controller.signal })
      .then((status) => {
        if (controller.signal.aborted) return;
        setAuth(status);
        setChecking(false);
        setError(
          token && !status.authenticated
            ? 'The administrator token was not accepted. Check the token and retry.'
            : '',
        );
      })
      .catch((err) => {
        if (!controller.signal.aborted) {
          setChecking(false);
          setError(messageOf(err instanceof Error ? err : null));
        }
      });
    return () => controller.abort();
  }, [api, attempt, token]);
  async function signOut() {
    try {
      await api.logout();
      setToken('');
      setAuth((old) =>
        old ? { ...old, authenticated: false, principal: '' } : old,
      );
      setError('');
      retryAuthentication();
    } catch (err) {
      setError(messageOf(err instanceof Error ? err : null));
    }
  }
  if (checking)
    return (
      <div className="auth-shell">
        <div className="brand">
          <Cube size={28} />
          Container Proxy
        </div>
        <Skeleton />
      </div>
    );
  if (!auth?.authenticated && !auth?.demo)
    return (
      <SignIn
        auth={auth}
        error={error}
        onToken={(value) => {
          setToken(value);
          retryAuthentication();
        }}
        onRetry={retryAuthentication}
      />
    );
  return (
    <Dashboard
      api={api}
      auth={auth}
      authError={error}
      onSignOut={() => {
        void signOut();
      }}
    />
  );
}
function SignIn({
  auth,
  error,
  onToken,
  onRetry,
}: {
  auth: AuthStatus | null;
  error: string;
  onToken: (token: string) => void;
  onRetry: () => void;
}) {
  const [entry, setEntry] = useState('');
  return (
    <main className="auth-shell">
      <div className="brand">
        <Cube size={28} aria-hidden="true" />
        Container Proxy
      </div>
      <div className="auth-panel">
        <Badge>Administrator access</Badge>
        <h1>Sign in to your homelab</h1>
        <p>
          Connect to inventory, review network changes and run explicit
          maintenance.
        </p>
        {error && (
          <Notice tone="danger" role="alert">
            {error}
          </Notice>
        )}
        {auth?.oidcEnabled && (
          <a className="button primary" href="/api/v1/auth/login">
            Sign in with OIDC
            <ArrowRight size={16} />
          </a>
        )}
        {auth?.bearerEnabled && (
          <form
            onSubmit={(event) => {
              event.preventDefault();
              if (entry.trim()) {
                onToken(entry.trim());
                setEntry('');
              }
            }}
          >
            <Field
              label="Administrator token"
              help="Kept only in this page’s memory. Reloading or signing out clears it."
            >
              {(id, helpId) => (
                <input
                  id={id}
                  aria-describedby={helpId}
                  type="password"
                  autoComplete="off"
                  required
                  value={entry}
                  onChange={(event) => setEntry(event.target.value)}
                />
              )}
            </Field>
            <button className="button primary" type="submit">
              Use administrator token
              <ArrowRight size={16} />
            </button>
          </form>
        )}
        {auth && !auth.oidcEnabled && !auth.bearerEnabled && (
          <Notice>
            No sign-in method is configured on this server. Configure OIDC or an
            administrator token on the server, then retry.
          </Notice>
        )}
        <button className="button" onClick={onRetry}>
          <ArrowClockwise size={16} />
          Retry connection
        </button>
      </div>
    </main>
  );
}
type Page = 'containers' | 'mismatches' | 'activity' | 'settings';
function initialView(): ViewMode {
  try {
    return localStorage.getItem('container-proxy-view') === 'rows'
      ? 'rows'
      : 'cards';
  } catch {
    return 'cards';
  }
}
function Dashboard({
  api,
  auth,
  authError,
  onSignOut,
}: {
  api: Api;
  auth: AuthStatus;
  authError: string;
  onSignOut: () => void;
}) {
  const dashboard = useDashboard(api);
  const {
    settings,
    data: observedData,
    jobs,
    operations,
    loading,
    error,
    refresh,
  } = dashboard;
  const [page, setPage] = useState<Page>('containers');
  const [filters, setFilters] = useState<Filters>(emptyFilters);
  const [mode, setMode] = useState<ViewMode>(initialView);
  const [selected, setSelected] = useState<string | null>(null);
  const [menuOpen, setMenuOpen] = useState(false);
  const refreshButton = useRef<HTMLButtonElement | null>(null);
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 30000);
    return () => clearInterval(timer);
  }, []);
  const stale =
    !!observedData &&
    (!Number.isFinite(Date.parse(observedData.inventory.observedAt)) ||
      now - Date.parse(observedData.inventory.observedAt) > 5 * 60 * 1000);
  const data = useMemo(() => {
    if (!observedData || (dashboard.inventoryValid && !stale))
      return observedData;
    return invalidateObservations(
      observedData,
      stale
        ? 'Inventory expired. Refresh to verify current provider state.'
        : 'Inventory refresh is pending or failed. Previous observations are display only.',
    );
  }, [observedData, dashboard.inventoryValid, stale]);
  const views = useMemo(
    () =>
      dashboard.inventorySettings && data
        ? containerViews(data, dashboard.inventorySettings)
        : [],
    [data, dashboard.inventorySettings],
  );
  function navigate(next: Page) {
    setPage(next);
    setMenuOpen(false);
  }
  function scope(host: string) {
    setFilters((old) => ({ ...old, host }));
    navigate('containers');
  }
  function view(next: ViewMode) {
    setMode(next);
    try {
      localStorage.setItem('container-proxy-view', next);
    } catch {
      /* The in-memory view still works when storage is blocked. */
    }
  }
  const mismatchCount = data?.items.filter(
    (item) => item.state !== 'healthy',
  ).length;
  const attention =
    jobs.filter(
      (job) =>
        ['failed', 'timedOut', 'interrupted', 'rebootRequired'].includes(
          job.status,
        ) && !job.acknowledged,
    ).length +
    operations.filter(
      (op) => op.status === 'partial' || op.status === 'interrupted',
    ).length;
  const nav = (
    <>
      <nav aria-label="Main navigation" className="main-nav">
        {(
          [
            { id: 'containers', label: 'Containers', Icon: Cube },
            { id: 'mismatches', label: 'Mismatches', Icon: Warning },
            { id: 'activity', label: 'Activity', Icon: ActivityIcon },
            { id: 'settings', label: 'Settings', Icon: GearSix },
          ] satisfies { id: Page; label: string; Icon: typeof Cube }[]
        ).map(({ id, label, Icon }) => (
          <button
            key={id}
            aria-current={page === id ? 'page' : undefined}
            onClick={() => navigate(id)}
          >
            <Icon size={20} />
            <span>{label}</span>
            {id === 'mismatches' &&
              mismatchCount !== undefined &&
              mismatchCount > 0 && (
                <span className="nav-count">{mismatchCount}</span>
              )}
            {id === 'activity' && attention > 0 && (
              <span className="nav-count">{attention}</span>
            )}
          </button>
        ))}
      </nav>
      <div className="nav-section">
        <div className="nav-section-label">
          HOSTS
          <button
            className="icon-button"
            aria-label="Configure hosts"
            onClick={() => navigate('settings')}
          >
            <GearSix size={16} />
          </button>
        </div>
        <nav aria-label="Host scope" className="host-nav">
          <button
            aria-current={
              page === 'containers' && !filters.host ? 'true' : undefined
            }
            onClick={() => scope('')}
          >
            <GlobeHemisphereWest size={19} />
            <span>All hosts</span>
          </button>
          {settings?.hosts.map((host) => (
            <button
              key={host.id}
              aria-current={
                page === 'containers' && filters.host === host.id
                  ? 'true'
                  : undefined
              }
              onClick={() => scope(host.id)}
            >
              <ComputerTower size={19} />
              <span>{host.name}</span>
              <span className="host-count">
                {data
                  ? data.inventory.containers.filter(
                      (c) => c.hostId === host.id,
                    ).length
                  : '·'}
              </span>
            </button>
          ))}
        </nav>
      </div>
      <div className="sidebar-footer">
        {auth.demo ? (
          <>
            <Badge tone="good">Demo mode</Badge>
            <p>
              Isolated mock providers.
              <br />
              No live infrastructure is contacted.
            </p>
          </>
        ) : (
          <>
            <p className="principal">{auth.principal || 'Administrator'}</p>
            <button className="button" onClick={onSignOut}>
              <SignOut size={16} />
              Sign out
            </button>
          </>
        )}
      </div>
    </>
  );
  return (
    <div className="app-shell">
      <a className="skip-link" href="#main-content">
        Skip to content
      </a>
      <aside className="sidebar">
        <a
          className="brand"
          href="#main-content"
          onClick={() => navigate('containers')}
        >
          <Cube size={26} />
          Container Proxy
        </a>
        {nav}
      </aside>
      <div className="workspace">
        <header className="app-header">
          <div className="mobile-nav">
            <Dialog.Root open={menuOpen} onOpenChange={setMenuOpen}>
              <Dialog.Trigger
                className="icon-button"
                aria-label="Open navigation"
              >
                <List size={22} />
              </Dialog.Trigger>
              <Dialog.Portal>
                <Dialog.Backdrop className="nav-backdrop" />
                <Dialog.Popup className="mobile-drawer">
                  <div className="drawer-heading">
                    <Dialog.Title>Container Proxy</Dialog.Title>
                    <Dialog.Close
                      className="icon-button"
                      aria-label="Close navigation"
                    >
                      <X size={20} />
                    </Dialog.Close>
                  </div>
                  {nav}
                </Dialog.Popup>
              </Dialog.Portal>
            </Dialog.Root>
          </div>
          <div
            className="next-ids"
            aria-label="Smallest available VMID per host"
          >
            {settings ? (
              settings.hosts.length ? (
                settings.hosts.map((host) => {
                  const inventory = data?.inventory.hosts.find(
                    (item) => item.hostId === host.id,
                  );
                  return (
                    <div className="next-id" key={host.id}>
                      <ComputerTower size={20} aria-hidden="true" />
                      <div>
                        <span>Next VMID · {host.name}</span>
                        <strong>
                          {inventory?.complete
                            ? (inventory.nextVmid ?? 'Range full')
                            : 'Unknown'}
                        </strong>
                      </div>
                    </div>
                  );
                })
              ) : (
                <span className="muted">
                  Next VMID: configure a Proxmox host
                </span>
              )
            ) : (
              <span className="muted">
                {loading
                  ? 'Next VMID: loading configured hosts…'
                  : 'Next VMID: configuration unavailable'}
              </span>
            )}
          </div>
          <button
            className="button"
            ref={refreshButton}
            onClick={() => {
              if (loading) dashboard.cancelRefresh();
              else void refresh();
            }}
          >
            <ArrowClockwise className={loading ? 'refreshing' : ''} size={17} />
            {loading ? 'Cancel refresh' : 'Refresh'}
          </button>
        </header>
        {auth.demo && (
          <div className="demo-banner">
            <Badge tone="good">Demo mode</Badge>
            <span>
              Isolated mock data and persisted demo changes. No live provider
              operations.
            </span>
          </div>
        )}
        <main id="main-content" tabIndex={-1} className="main-content">
          {(error || authError) && (
            <Notice tone="danger" role="alert">
              {error || authError}
              {data && !dashboard.inventoryValid && (
                <p>
                  Showing previously observed data from{' '}
                  {formatTime(data.inventory.observedAt)}. Treat values as stale
                  until a successful refresh.
                </p>
              )}
              <button
                className="button"
                onClick={() => {
                  void refresh();
                }}
              >
                Retry refresh
              </button>
            </Notice>
          )}
          {dashboard.activityError && page !== 'activity' && (
            <Notice tone="danger">
              {dashboard.activityError}
              <button
                className="text-button"
                onClick={() => navigate('activity')}
              >
                Review Activity
              </button>
            </Notice>
          )}
          {stale && (
            <Notice>
              Inventory is more than five minutes old or its timestamp is
              unavailable. Refresh to check current provider state.
            </Notice>
          )}
          {loading &&
            ((!data && (page === 'containers' || page === 'mismatches')) ||
              (!settings && page === 'settings')) && <Skeleton />}
          {!loading &&
            (!settings || !data) &&
            (page === 'containers' ||
              page === 'mismatches' ||
              (!settings && page === 'settings')) && (
              <Empty title="Dashboard data unavailable">
                <p>
                  The server could not provide valid configuration and
                  inventory. Retry the connection to continue.
                </p>
                <button
                  className="button"
                  onClick={() => {
                    void refresh();
                  }}
                >
                  Retry dashboard
                </button>
              </Empty>
            )}
          {dashboard.inventorySettings && data && (
            <>
              <div hidden={page !== 'containers'}>
                <InventoryView
                  api={api}
                  settings={dashboard.inventorySettings}
                  data={{ ...data, operations }}
                  views={views}
                  filters={filters}
                  setFilters={setFilters}
                  mode={mode}
                  setMode={view}
                  selected={selected}
                  setSelected={setSelected}
                  jobs={jobs}
                  activityLoaded={
                    dashboard.activityLoaded && !dashboard.activityError
                  }
                  onJob={dashboard.addJob}
                  onRefresh={refresh}
                  onSettings={() => navigate('settings')}
                  onMismatches={() => navigate('mismatches')}
                />
              </div>
              <div hidden={page !== 'mismatches'}>
                <Mismatches
                  api={api}
                  settings={dashboard.inventorySettings}
                  data={{ ...data, operations }}
                  onInspect={(key) => {
                    setSelected(key);
                    navigate('containers');
                  }}
                  onRefresh={refresh}
                  onActivity={() => navigate('activity')}
                />
              </div>
            </>
          )}
          <div hidden={page !== 'activity'}>
            <Activity
              api={api}
              jobs={jobs}
              operations={operations}
              loaded={dashboard.activityLoaded}
              error={dashboard.activityError}
              onRefresh={refresh}
            />
          </div>
          {settings && (
            <div hidden={page !== 'settings'}>
              <SettingsEditor
                api={api}
                settings={settings}
                onSaved={async (result) => {
                  dashboard.setSettings(result);
                  await refresh();
                }}
              />
            </div>
          )}
        </main>
        <LoadingToast
          refreshId={dashboard.refreshId}
          reads={dashboard.reads}
          onDismiss={() =>
            refreshButton.current?.focus({ preventScroll: true })
          }
        />
      </div>
    </div>
  );
}
