import {
  act,
  fireEvent,
  render,
  renderHook,
  screen,
  waitFor,
  within,
} from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { Api } from '../src/api';
import { App } from '../src/App';
import { useDashboard } from '../src/use-dashboard';
import { auth, job, reconciliation, settings } from './fixtures';

function deferred<T>() {
  let resolve: (value: T) => void = () => {};
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}
function json(value: object) {
  return new Response(JSON.stringify(value));
}
function response(path: string) {
  if (path === '/auth/status') return json(auth);
  if (path === '/settings') return json(settings);
  if (path === '/reconciliation') return json(reconciliation);
  if (path === '/profiles') return json(settings.hosts);
  return json([]);
}
function stub(
  override: (
    path: string,
    init?: RequestInit,
  ) => Promise<Response> | Response | undefined,
) {
  const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
    const path = String(input).replace('/api/v1', '');
    return override(path, init) ?? response(path);
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  localStorage.clear();
});

it.each(['/settings', '/reconciliation', '/jobs', '/operations'])(
  'keeps independent successful reads available while %s is indefinitely pending',
  async (heldPath) => {
    const held = deferred<Response>();
    let signal: AbortSignal | null | undefined;
    stub((path, init) => {
      if (path !== heldPath) return undefined;
      signal = init?.signal;
      return held.promise;
    });
    const unauthorized = vi.fn();
    const api = new Api('', unauthorized);
    const { result } = renderHook(() => useDashboard(api));
    await waitFor(() => expect(result.current.pending).toHaveLength(1));
    expect(result.current.loading).toBe(true);
    expect(result.current.settings).toEqual(
      heldPath === '/settings' ? null : settings,
    );
    expect(result.current.inventoryValid).toBe(
      heldPath === '/jobs' || heldPath === '/operations',
    );
    expect(result.current.activityLoaded).toBe(
      heldPath === '/settings' || heldPath === '/reconciliation',
    );
    act(() => result.current.cancelRefresh());
    expect(signal?.aborted).toBe(true);
    expect(result.current.loading).toBe(false);
    expect(result.current.pending).toEqual([]);
    const state = result.current;
    await act(async () => held.resolve(new Response('{}', { status: 401 })));
    expect(unauthorized).not.toHaveBeenCalled();
    expect(result.current).toBe(state);
  },
);

it('keeps old observations bound to their settings until this refresh inventory arrives, then ignores an older retry', async () => {
  let phase = 0;
  const second = deferred<Response>();
  let oldSignal: AbortSignal | null | undefined;
  const revised = {
    ...settings,
    revision: 8,
    hosts: settings.hosts.map((h) => ({
      ...h,
      name: `Revised ${h.name}`,
      opnsenseId: 'another-firewall',
    })),
  };
  stub((path, init) => {
    if (phase && path === '/settings')
      return json({ ...revised, revision: phase === 1 ? 8 : 9 });
    if (phase === 1 && path === '/reconciliation') {
      oldSignal = init?.signal;
      return second.promise;
    }
    if (phase === 2 && path === '/reconciliation')
      return json({
        ...reconciliation,
        inventory: {
          ...reconciliation.inventory,
          hosts: reconciliation.inventory.hosts.map((h) => ({
            ...h,
            nextVmid: 609,
          })),
        },
      });
    return undefined;
  });
  const api = new Api('', vi.fn());
  const { result } = renderHook(() => useDashboard(api));
  await waitFor(() => expect(result.current.loading).toBe(false));
  phase = 1;
  let firstRefresh: Promise<void> | undefined;
  act(() => {
    firstRefresh = result.current.refresh();
  });
  await waitFor(() => expect(result.current.settings?.revision).toBe(8));
  expect(result.current.inventoryValid).toBe(false);
  expect(result.current.inventorySettings).toEqual(settings);
  phase = 2;
  await act(async () => {
    await result.current.refresh();
  });
  expect(oldSignal?.aborted).toBe(true);
  expect(result.current.settings?.revision).toBe(9);
  expect(result.current.inventorySettings?.revision).toBe(9);
  expect(result.current.data?.inventory.hosts[0]?.nextVmid).toBe(609);
  expect(result.current.inventoryValid).toBe(true);
  await act(async () => {
    second.resolve(json(reconciliation));
    await firstRefresh;
  });
  expect(result.current.settings?.revision).toBe(9);
  expect(result.current.data?.inventory.hosts[0]?.nextVmid).toBe(609);
});

it('does not retain failed-host observations across a settings revision', async () => {
  let next = false;
  stub((path) => {
    if (!next) return undefined;
    if (path === '/settings') return json({ ...settings, revision: 8 });
    if (path === '/reconciliation')
      return json({
        ...reconciliation,
        inventory: {
          ...reconciliation.inventory,
          complete: false,
          containers: [],
          hosts: reconciliation.inventory.hosts.map((h) => ({
            ...h,
            complete: false,
            nextVmid: null,
            error: 'Provider unavailable',
          })),
        },
        items: [],
      });
    return undefined;
  });
  const api = new Api('', vi.fn());
  const { result } = renderHook(() => useDashboard(api));
  await waitFor(() => expect(result.current.loading).toBe(false));
  next = true;
  await act(async () => {
    await result.current.refresh();
  });
  expect(result.current.data?.inventory.containers).toEqual([]);
  expect(result.current.inventorySettings?.revision).toBe(8);
});

it('invalidates an in-flight read when locally saved settings supersede it', async () => {
  const config = deferred<Response>();
  const inventory = deferred<Response>();
  stub((path) =>
    path === '/settings'
      ? config.promise
      : path === '/reconciliation'
        ? inventory.promise
        : undefined,
  );
  const api = new Api('', vi.fn());
  const { result } = renderHook(() => useDashboard(api));
  await waitFor(() => expect(result.current.activityLoaded).toBe(true));
  act(() => result.current.setSettings({ ...settings, revision: 9 }));
  await act(async () => {
    config.resolve(json(settings));
    inventory.resolve(json(reconciliation));
  });
  expect(result.current.settings?.revision).toBe(9);
  expect(result.current.inventoryValid).toBe(false);
  expect(result.current.activityLoaded).toBe(false);
  expect(result.current.data).toBeNull();
});

it('never promotes incomplete activity from an earlier epoch after a new job is observed', async () => {
  const operations = deferred<Response>();
  stub((path) => (path === '/operations' ? operations.promise : undefined));
  const api = new Api('', vi.fn());
  const { result } = renderHook(() => useDashboard(api));
  await waitFor(() => expect(result.current.inventoryValid).toBe(true));
  act(() => result.current.addJob(job));
  await act(async () => operations.resolve(json([])));
  expect(result.current.jobs).toEqual([job]);
  expect(result.current.activityLoaded).toBe(false);
});

it('aborts all owned reads and frees deadlines on unmount, ignoring late results', async () => {
  vi.useFakeTimers();
  const held = deferred<Response>();
  const signals: AbortSignal[] = [];
  const fetchMock = stub((_path, init) => {
    if (init?.signal) signals.push(init.signal);
    return held.promise;
  });
  const unauthorized = vi.fn();
  const api = new Api('', unauthorized);
  const { result, unmount } = renderHook(() => useDashboard(api));
  const refresh = result.current.refresh;
  unmount();
  expect(signals).toHaveLength(4);
  expect(signals.every((signal) => signal.aborted)).toBe(true);
  await act(async () => held.resolve(new Response('{}', { status: 401 })));
  expect(unauthorized).not.toHaveBeenCalled();
  expect(vi.getTimerCount()).toBe(0);
  await refresh();
  expect(fetchMock).toHaveBeenCalledTimes(4);
});

it('withdraws maintenance readiness when an activity poll times out', async () => {
  let hold = false;
  const held = deferred<Response>();
  stub((path) =>
    path === '/jobs' ? (hold ? held.promise : json([job])) : undefined,
  );
  const api = new Api('', vi.fn());
  const { result, unmount } = renderHook(() => useDashboard(api));
  await waitFor(() => expect(result.current.activityLoaded).toBe(true));
  // Restart the polling effect under a fake clock by first observing no active
  // job, then a newly created one. No real waiting or timeout inflation.
  stub((path) => (path === '/jobs' ? json([]) : undefined));
  await act(async () => {
    await result.current.refresh();
  });
  vi.useFakeTimers();
  hold = true;
  stub((path) => (path === '/jobs' && hold ? held.promise : undefined));
  act(() => result.current.addJob(job));
  await act(async () => {
    await vi.advanceTimersByTimeAsync(107_500);
  });
  expect(result.current.inventoryValid).toBe(true);
  expect(result.current.activityLoaded).toBe(false);
  expect(result.current.activityError).toContain('timed out');
  unmount();
  expect(vi.getTimerCount()).toBe(0);
});

it('does not show dashboard loading toasts for routine background job polling', async () => {
  vi.useFakeTimers();
  let hold = false;
  const polled = deferred<Response>();
  stub((path) =>
    path === '/jobs' ? (hold ? polled.promise : json([job])) : undefined,
  );
  const view = render(<App />);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(screen.getByRole('button', { name: 'Refresh' })).toBeEnabled();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1200);
  });
  expect(
    screen.queryByRole('complementary', { name: 'Dashboard loading status' }),
  ).not.toBeInTheDocument();
  hold = true;
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1300);
  });
  expect(screen.getByRole('button', { name: 'Refresh' })).toBeEnabled();
  expect(
    screen.queryByRole('complementary', { name: 'Dashboard loading status' }),
  ).not.toBeInTheDocument();
  view.unmount();
  await act(async () => polled.resolve(json([])));
  expect(vi.getTimerCount()).toBe(0);
});

it('renders inventory before hung activity, blocks updates and recovers via cancel and retry', async () => {
  let hold = true;
  const held = deferred<Response>();
  stub((path) => (path === '/jobs' && hold ? held.promise : undefined));
  render(<App />);
  await screen.findByRole('button', { name: 'Configure immich, VMID 604' });
  expect(screen.getByText('605', { exact: true })).toBeVisible();
  const update = screen.getByRole('button', {
    name: 'Review app update immich, VMID 604',
  });
  expect(update).toBeDisabled();
  expect(screen.getByRole('button', { name: 'Cancel refresh' })).toBeEnabled();
  fireEvent.click(screen.getByRole('button', { name: 'Cancel refresh' }));
  expect(screen.getByRole('button', { name: 'Refresh' })).toBeEnabled();
  hold = false;
  fireEvent.click(screen.getByRole('button', { name: 'Refresh' }));
  await waitFor(() => expect(update).toBeEnabled());
  expect(screen.queryByText(/Refresh cancelled/)).not.toBeInTheDocument();
});

it('shows an actionable read timeout, retains explicitly stale observations and withdraws VMIDs and mutations', async () => {
  let hold = false;
  const held = deferred<Response>();
  stub((path) =>
    path === '/reconciliation' && hold ? held.promise : undefined,
  );
  const view = render(<App />);
  await screen.findByText('605', { exact: true });
  vi.useFakeTimers();
  hold = true;
  fireEvent.click(screen.getByRole('button', { name: 'Refresh' }));
  await act(async () => {
    await vi.advanceTimersByTimeAsync(105_000);
  });
  expect(screen.getByRole('alert')).toHaveTextContent(
    'provider inventory: The read timed out',
  );
  expect(screen.getByRole('alert')).toHaveTextContent('Treat values as stale');
  expect(screen.queryByText('605', { exact: true })).not.toBeInTheDocument();
  expect(
    screen.getByRole('button', { name: 'Configure immich, VMID 604' }),
  ).toBeEnabled();
  expect(
    screen.getByRole('button', { name: 'Review app update immich, VMID 604' }),
  ).toBeDisabled();
  expect(screen.getByRole('button', { name: 'Retry refresh' })).toBeEnabled();
  view.unmount();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(vi.getTimerCount()).toBe(0);
});

it('consumes a timed-out update confirmation and keeps its unknown-outcome latch across close and reopen', async () => {
  const held = deferred<Response>();
  let writes = 0;
  stub((path, init) => {
    if (path === '/jobs' && init?.method === 'POST') {
      writes++;
      return held.promise;
    }
    return undefined;
  });
  const view = render(<App />);
  const review = await screen.findByRole('button', {
    name: 'Review app update immich, VMID 604',
  });
  await waitFor(() => expect(review).toBeEnabled());
  fireEvent.click(review);
  vi.useFakeTimers();
  fireEvent.click(screen.getByRole('button', { name: 'Request update' }));
  await act(async () => {
    await vi.advanceTimersByTimeAsync(120_000);
  });
  expect(
    screen.getByText(/The update request outcome is unknown/),
  ).toBeVisible();
  expect(
    screen.queryByRole('button', { name: 'Request update' }),
  ).not.toBeInTheDocument();
  fireEvent.click(
    screen.getByRole('button', { name: 'Close container details' }),
  );
  fireEvent.click(
    screen.getByRole('button', { name: 'Configure immich, VMID 604' }),
  );
  const inspector = screen.getByRole('complementary', {
    name: 'Details for immich',
  });
  expect(
    within(inspector).getByRole('button', { name: 'App update' }),
  ).toBeDisabled();
  expect(writes).toBe(1);
  view.unmount();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(vi.getTimerCount()).toBe(0);
});

it('ignores a late unauthorized write response after sign-out and a newer successful sign-in', async () => {
  const oldWrite = deferred<Response>();
  let signedOut = false;
  stub((path, init) => {
    if (path === '/jobs' && init?.method === 'POST') return oldWrite.promise;
    if (path === '/auth/logout') {
      signedOut = true;
      return json({ authenticated: false });
    }
    if (path === '/auth/status') {
      return json({
        ...auth,
        demo: false,
        authenticated: !signedOut,
        bearerEnabled: true,
      });
    }
    return undefined;
  });
  render(<App />);
  const review = await screen.findByRole('button', {
    name: 'Review app update immich, VMID 604',
  });
  await waitFor(() => expect(review).toBeEnabled());
  fireEvent.click(review);
  fireEvent.click(screen.getByRole('button', { name: 'Request update' }));
  await act(async () => {
    fireEvent.click(screen.getByRole('button', { name: 'Sign out' }));
  });
  await screen.findByLabelText('Administrator token');
  fireEvent.change(screen.getByLabelText('Administrator token'), {
    target: { value: 'fixture-token-one' },
  });
  signedOut = false;
  fireEvent.click(
    screen.getByRole('button', { name: 'Use administrator token' }),
  );
  await screen.findByRole('button', { name: 'Configure immich, VMID 604' });
  await act(async () => oldWrite.resolve(new Response('{}', { status: 401 })));
  expect(
    screen.queryByRole('heading', { name: 'Sign in to your homelab' }),
  ).not.toBeInTheDocument();
});
