import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { App } from '../src/App';
import { auth, job, partial, plan, reconciliation, settings } from './fixtures';

function json(body: string, status = 200) {
  return new Response(body, {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}
function mockApi(
  overrides?: (path: string, init?: RequestInit) => Response | undefined,
) {
  const mock = vi.fn<typeof fetch>(async (input, init) => {
    const path = String(input).replace('/api/v1', '');
    const custom = overrides?.(path, init);
    if (custom) return custom;
    if (path === '/auth/status') return json(JSON.stringify(auth));
    if (path === '/settings') return json(JSON.stringify(settings));
    if (path === '/reconciliation') return json(JSON.stringify(reconciliation));
    if (path === '/jobs' || path === '/operations') return json('[]');
    if (path === '/profiles') return json(JSON.stringify(settings.hosts));
    return json('{"error":{"code":"notFound","message":"Not found"}}', 404);
  });
  vi.stubGlobal('fetch', mock);
  return mock;
}
beforeEach(() => {
  localStorage.clear();
});
afterEach(() => {
  vi.unstubAllGlobals();
});

describe('authentication and inventory', () => {
  it('does not invent legacy app values when its inspector opens during an outage and the host recovers', async () => {
    let phase = 'healthy';
    let plans = 0;
    mockApi((path, init) => {
      if (path === '/plans' && init?.method === 'POST') plans++;
      if (path !== '/reconciliation') return undefined;
      const legacy = {
        ...reconciliation,
        inventory: {
          ...reconciliation.inventory,
          containers: reconciliation.inventory.containers.map((c) =>
            c.key === 'main:604' ? { ...c, desiredRoute: undefined } : c,
          ),
        },
        items: reconciliation.items.map((item) =>
          item.key === 'main:604'
            ? {
                ...item,
                state: 'mismatch',
                owned: true,
                desiredRoute: undefined,
                routeUnavailable: true,
              }
            : item,
        ),
      };
      if (phase !== 'failed') return json(JSON.stringify(legacy));
      return json(
        JSON.stringify({
          ...legacy,
          inventory: {
            ...legacy.inventory,
            complete: false,
            containers: legacy.inventory.containers.filter(
              (c) => c.hostId !== 'main',
            ),
            hosts: legacy.inventory.hosts.map((h) =>
              h.hostId === 'main'
                ? {
                    ...h,
                    complete: false,
                    error: 'Host read unavailable',
                    nextVmid: null,
                    guests: [],
                  }
                : h,
            ),
          },
          items: legacy.items.map((item) =>
            item.key === 'main:604'
              ? { ...item, state: 'unknown', routeUnavailable: undefined }
              : item,
          ),
        }),
      );
    });
    const user = userEvent.setup();
    render(<App />);
    await screen.findByRole('button', { name: 'Configure immich, VMID 604' });
    phase = 'failed';
    await user.click(screen.getByRole('button', { name: 'Refresh' }));
    await screen.findByText('Host read unavailable');
    await user.click(
      screen.getByRole('button', { name: 'Configure immich, VMID 604' }),
    );
    phase = 'recovered';
    await user.click(screen.getByRole('button', { name: 'Refresh' }));
    await screen.findByText('605');
    expect(screen.getByLabelText('Upstream scheme')).toHaveValue('');
    expect(screen.getByLabelText('Upstream port')).toHaveValue(null);
    const review = screen.getByRole('button', {
      name: 'Review network change',
    });
    expect(review).toBeDisabled();
    expect(plans).toBe(0);
    await user.selectOptions(screen.getByLabelText('Upstream scheme'), 'https');
    expect(review).toBeDisabled();
    await user.type(screen.getByLabelText('Upstream port'), '8443');
    expect(review).toBeEnabled();
  });
  it('withdraws allocation and updates after a failed refresh while keeping observed containers', async () => {
    let failed = false;
    mockApi((path) =>
      path === '/reconciliation' && failed
        ? json(
            '{"error":{"code":"providerUnavailable","message":"Inventory read failed"}}',
            502,
          )
        : undefined,
    );
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText('605');
    failed = true;
    await user.click(screen.getByRole('button', { name: 'Refresh' }));
    await screen.findByText('Inventory read failed');
    expect(screen.queryByText('605')).not.toBeInTheDocument();
    expect(screen.queryByText('703')).not.toBeInTheDocument();
    expect(screen.getAllByText('Unknown').length).toBeGreaterThan(0);
    await user.click(
      screen.getByRole('button', { name: 'Configure immich, VMID 604' }),
    );
    const inspector = screen.getByRole('complementary', {
      name: 'Details for immich',
    });
    expect(
      within(inspector).getByRole('button', {
        name: 'Review network change',
      }),
    ).toBeDisabled();
    expect(
      within(inspector).getByRole('button', {
        name: 'App update',
      }),
    ).toBeDisabled();
  });

  it('never advertises allocation from an expired observation', async () => {
    mockApi((path) =>
      path === '/reconciliation'
        ? json(
            JSON.stringify({
              ...reconciliation,
              inventory: {
                ...reconciliation.inventory,
                observedAt: new Date(Date.now() - 6 * 60 * 1000).toISOString(),
              },
            }),
          )
        : undefined,
    );
    render(<App />);
    await screen.findByText(/Inventory is more than five minutes old/);
    expect(screen.queryByText('605')).not.toBeInTheDocument();
    expect(screen.queryByText('703')).not.toBeInTheDocument();
  });

  it('retains the failed host and inspector, then removes them after a complete empty observation', async () => {
    let phase = 'healthy';
    mockApi((path) => {
      if (path !== '/reconciliation' || phase === 'healthy') return undefined;
      return json(
        JSON.stringify({
          ...reconciliation,
          inventory: {
            ...reconciliation.inventory,
            complete: phase === 'empty',
            containers: reconciliation.inventory.containers.filter(
              (c) => c.hostId !== 'main',
            ),
            hosts: reconciliation.inventory.hosts.map((h) =>
              h.hostId === 'main'
                ? {
                    ...h,
                    complete: phase === 'empty',
                    error: phase === 'empty' ? '' : 'Host read unavailable',
                    nextVmid: phase === 'empty' ? 601 : null,
                    guests: [],
                  }
                : h,
            ),
          },
          items: reconciliation.items.filter(
            (item) => !item.key.startsWith('main:'),
          ),
        }),
      );
    });
    const user = userEvent.setup();
    render(<App />);
    await user.click(
      await screen.findByRole('button', { name: 'Configure immich, VMID 604' }),
    );
    phase = 'failed';
    await user.click(screen.getByRole('button', { name: 'Refresh' }));
    await screen.findByText('Host read unavailable');
    const inspector = screen.getByRole('complementary', {
      name: 'Details for immich',
    });
    expect(
      within(inspector).getByText(
        /Host inventory is stale. Last observed values:/,
      ),
    ).toBeVisible();
    expect(screen.queryByText('605')).not.toBeInTheDocument();
    expect(screen.getByText('703')).toBeInTheDocument();
    expect(
      within(inspector).getByRole('button', {
        name: 'App update',
      }),
    ).toBeDisabled();
    phase = 'empty';
    await user.click(screen.getByRole('button', { name: 'Refresh' }));
    await screen.findByText('601');
    expect(
      screen.queryByRole('button', { name: 'Configure immich, VMID 604' }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole('complementary', { name: 'Details for immich' }),
    ).not.toBeInTheDocument();
  });

  it('requires deliberate app values when a legacy upstream is unavailable', async () => {
    mockApi((path) =>
      path === '/reconciliation'
        ? json(
            JSON.stringify({
              ...reconciliation,
              inventory: {
                ...reconciliation.inventory,
                containers: reconciliation.inventory.containers.map((c) =>
                  c.key === 'main:604' ? { ...c, desiredRoute: undefined } : c,
                ),
              },
              items: reconciliation.items.map((item) =>
                item.key === 'main:604'
                  ? {
                      ...item,
                      state: 'mismatch',
                      desiredRoute: undefined,
                      routeUnavailable: true,
                    }
                  : item,
              ),
            }),
          )
        : undefined,
    );
    const user = userEvent.setup();
    render(<App />);
    await user.click(
      await screen.findByRole('button', { name: 'Configure immich, VMID 604' }),
    );
    expect(screen.getByLabelText('Upstream scheme')).toHaveValue('');
    expect(screen.getByLabelText('Upstream port')).toHaveValue(null);
    const review = screen.getByRole('button', {
      name: 'Review network change',
    });
    expect(review).toBeDisabled();
    await user.selectOptions(screen.getByLabelText('Upstream scheme'), 'https');
    expect(review).toBeDisabled();
    await user.type(screen.getByLabelText('Upstream port'), '8443');
    expect(review).toBeEnabled();
  });
  it('consumes a lost update attempt even when refreshed history already reports success', async () => {
    let requests = 0;
    mockApi((path, init) => {
      if (path === '/jobs' && init?.method === 'POST') {
        requests++;
        throw new TypeError('response lost');
      }
      if (path === '/jobs')
        return json(
          JSON.stringify(requests ? [{ ...job, status: 'succeeded' }] : []),
        );
      return undefined;
    });
    const user = userEvent.setup();
    render(<App />);
    const open = await screen.findByRole('button', {
      name: 'Review app update immich, VMID 604',
    });
    await waitFor(() => expect(open).toBeEnabled());
    await user.click(open);
    await user.click(screen.getByRole('button', { name: 'Request update' }));
    await within(
      screen.getByRole('complementary', { name: 'Details for immich' }),
    ).findByText('Succeeded');
    const reused = screen.queryByRole('button', { name: 'Request update' });
    if (reused && !reused.hasAttribute('disabled')) await user.click(reused);
    expect(requests).toBe(1);
    expect(
      screen.queryByRole('button', { name: 'Request update' }),
    ).not.toBeInTheDocument();
    let inspector = screen.getByRole('complementary', {
      name: 'Details for immich',
    });
    await user.click(
      within(inspector).getByRole('button', {
        name: 'Close container details',
      }),
    );
    await user.click(
      screen.getByRole('button', { name: 'Configure immich, VMID 604' }),
    );
    inspector = screen.getByRole('complementary', {
      name: 'Details for immich',
    });
    expect(
      within(inspector).getByRole('button', { name: 'App update' }),
    ).toBeDisabled();
    await user.click(
      within(inspector).getByRole('button', {
        name: 'I inspected the update outcome',
      }),
    );
    expect(
      within(inspector).getByRole('button', {
        name: 'App update',
      }),
    ).toBeEnabled();
    // Another execution requires acknowledgement and a new review.
    await user.click(
      within(inspector).getByRole('button', { name: 'App update' }),
    );
    expect(
      screen.getByRole('button', { name: 'Request update' }),
    ).toBeEnabled();
    await user.click(screen.getByRole('button', { name: 'Request update' }));
    await waitFor(() => expect(requests).toBe(2));
  });
  it('disables a prepared update when activity refresh becomes unavailable', async () => {
    let unavailable = false;
    const mock = mockApi((path) =>
      path === '/jobs' && unavailable
        ? json(
            '{"error":{"code":"providerUnavailable","message":"History unavailable"}}',
            503,
          )
        : undefined,
    );
    const user = userEvent.setup();
    render(<App />);
    const open = await screen.findByRole('button', {
      name: 'Review app update immich, VMID 604',
    });
    await waitFor(() => expect(open).toBeEnabled());
    await user.click(open);
    unavailable = true;
    await user.click(screen.getByRole('button', { name: 'Refresh' }));
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Refresh' })).toBeEnabled(),
    );
    expect(
      screen.getByRole('button', { name: 'Request update' }),
    ).toBeDisabled();
    expect(mock.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(
      false,
    );
  });
  it('shows the expected addresses beside conflicting observations while planning is blocked', async () => {
    mockApi((path) =>
      path === '/reconciliation'
        ? json(
            JSON.stringify({
              ...reconciliation,
              inventory: {
                ...reconciliation.inventory,
                containers: reconciliation.inventory.containers.map((c) =>
                  c.key === 'main:604'
                    ? {
                        ...c,
                        ip: '192.168.6.99',
                        mac: 'd0:cc:37:00:06:99',
                        expectedIp: '192.168.6.4',
                        expectedMac: 'd0:cc:37:00:06:04',
                      }
                    : c,
                ),
              },
              items: reconciliation.items.map((item) =>
                item.key === 'main:604'
                  ? {
                      ...item,
                      state: 'conflict',
                      issues: [
                        'Container address differs from the host profile',
                      ],
                    }
                  : item,
              ),
            }),
          )
        : undefined,
    );
    const user = userEvent.setup();
    render(<App />);
    await user.click(
      await screen.findByRole('button', { name: 'Configure immich, VMID 604' }),
    );
    const inspector = screen.getByRole('complementary', {
      name: 'Details for immich',
    });
    expect(within(inspector).getByText('Expected IP address')).toBeVisible();
    expect(within(inspector).getByText('192.168.6.4')).toBeVisible();
    expect(within(inspector).getByText('Expected MAC address')).toBeVisible();
    expect(within(inspector).getByText('d0:cc:37:00:06:04')).toBeVisible();
    expect(
      within(inspector).getByRole('button', { name: 'Review network change' }),
    ).toBeDisabled();
  });
  it('card maintenance opens the shared review before requesting the exact container job', async () => {
    const mock = mockApi((path, init) =>
      path === '/jobs' && init?.method === 'POST'
        ? json(JSON.stringify(job), 202)
        : undefined,
    );
    const user = userEvent.setup();
    render(<App />);
    const action = await screen.findByRole('button', {
      name: 'Review app update immich, VMID 604',
    });
    await waitFor(() => expect(action).toBeEnabled());
    expect(
      screen.getByRole('button', {
        name: 'Review app update vaultwarden, VMID 606',
      }),
    ).toBeDisabled();
    await user.click(action);
    expect(
      await screen.findByRole('heading', { name: 'Run app update on immich?' }),
    ).toBeVisible();
    expect(mock.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(
      false,
    );
    await user.click(screen.getByRole('button', { name: 'Request update' }));
    await waitFor(() =>
      expect(
        mock.mock.calls.some(
          ([input, init]) =>
            String(input).endsWith('/jobs') &&
            init?.method === 'POST' &&
            init.body ===
              JSON.stringify({
                hostId: 'main',
                vmid: 604,
                kind: 'appUpdate',
                fingerprint: 'fingerprint-604',
              }),
        ),
      ).toBe(true),
    );
  });
  it('presents both configured sign-in capabilities without reading anonymous inventory', async () => {
    const mock = mockApi((path) =>
      path === '/auth/status'
        ? json(
            JSON.stringify({
              ...auth,
              demo: false,
              authenticated: false,
              bearerEnabled: true,
              oidcEnabled: true,
            }),
          )
        : undefined,
    );
    render(<App />);
    expect(
      await screen.findByRole('heading', { name: 'Sign in to your homelab' }),
    ).toBeVisible();
    expect(
      screen.getByRole('link', { name: /Sign in with OIDC/ }),
    ).toHaveAttribute('href', '/api/v1/auth/login');
    expect(screen.getByLabelText('Administrator token')).toBeVisible();
    expect(
      mock.mock.calls.some((call) =>
        String(call[0]).includes('/reconciliation'),
      ),
    ).toBe(false);
    expect(
      screen.queryByText('No LXC containers observed'),
    ).not.toBeInTheDocument();
  });
  it('keeps bearer credentials out of browser storage and URL', async () => {
    let authorized = false;
    mockApi((path, init) => {
      if (path !== '/auth/status') return;
      const headers = new Headers(init?.headers);
      authorized = headers.get('Authorization') === 'Bearer temporary-token';
      return json(
        JSON.stringify({
          ...auth,
          demo: false,
          authenticated: authorized,
          bearerEnabled: true,
          oidcEnabled: true,
        }),
      );
    });
    const user = userEvent.setup();
    render(<App />);
    await user.type(
      await screen.findByLabelText('Administrator token'),
      'temporary-token',
    );
    await user.click(
      screen.getByRole('button', { name: /Use administrator token/ }),
    );
    expect(
      await screen.findByRole('heading', { name: 'Containers' }),
    ).toBeVisible();
    expect(authorized).toBe(true);
    expect(JSON.stringify(localStorage)).not.toContain('temporary-token');
    expect(JSON.stringify(sessionStorage)).not.toContain('temporary-token');
    expect(window.location.href).not.toContain('temporary-token');
  });
  it('distinguishes a successfully empty inventory from missing authentication', async () => {
    mockApi((path) =>
      path === '/reconciliation'
        ? json(
            JSON.stringify({
              ...reconciliation,
              inventory: { ...reconciliation.inventory, containers: [] },
              items: [],
            }),
          )
        : undefined,
    );
    render(<App />);
    expect(
      await screen.findByRole('heading', {
        name: 'No LXC containers observed',
      }),
    ).toBeVisible();
    expect(
      screen.queryByRole('heading', { name: 'Sign in to your homelab' }),
    ).not.toBeInTheDocument();
  });
  it('keeps search, composed filters, selection and network draft across Cards/Rows and navigation', async () => {
    mockApi();
    const user = userEvent.setup();
    render(<App />);
    await screen.findByRole('heading', { name: 'Containers' });
    await user.type(
      screen.getByLabelText('Search all container values'),
      'immich 192.168.6.4',
    );
    await user.selectOptions(
      screen.getByLabelText('Host', { exact: true }),
      'main',
    );
    await user.selectOptions(
      screen.getByLabelText('State', { exact: true }),
      'running',
    );
    await user.selectOptions(
      screen.getByLabelText('Distribution', { exact: true }),
      'debian',
    );
    await user.selectOptions(
      screen.getByLabelText('Exposure', { exact: true }),
      'LAN / VPN',
    );
    await user.click(
      screen.getByRole('button', { name: 'Configure immich, VMID 604' }),
    );
    const port = screen.getByLabelText('Upstream port');
    await user.clear(port);
    await user.type(port, '2284');
    await user.click(screen.getByRole('button', { name: 'Rows' }));
    expect(screen.getByRole('button', { name: 'Rows' })).toHaveAttribute(
      'aria-pressed',
      'true',
    );
    expect(screen.getByLabelText('Search all container values')).toHaveValue(
      'immich 192.168.6.4',
    );
    expect(screen.getByLabelText('Host', { exact: true })).toHaveValue('main');
    expect(screen.getByLabelText('State', { exact: true })).toHaveValue(
      'running',
    );
    expect(screen.getByLabelText('Distribution', { exact: true })).toHaveValue(
      'debian',
    );
    expect(screen.getByLabelText('Exposure', { exact: true })).toHaveValue(
      'LAN / VPN',
    );
    expect(
      screen.getByRole('complementary', { name: 'Details for immich' }),
    ).toBeVisible();
    expect(screen.getByLabelText('Upstream port')).toHaveValue(2284);
    expect(localStorage.getItem('container-proxy-view')).toBe('rows');
    await user.click(screen.getByRole('button', { name: 'Refresh' }));
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Refresh' })).toBeEnabled(),
    );
    expect(screen.getByLabelText('Upstream port')).toHaveValue(2284);
    const nav = screen.getAllByRole('navigation', {
      name: 'Main navigation',
    })[0];
    if (!nav) throw new Error('Main navigation missing');
    await user.click(within(nav).getByRole('button', { name: /Settings/ }));
    await user.click(within(nav).getByRole('button', { name: /Containers/ }));
    expect(screen.getByLabelText('Upstream port')).toHaveValue(2284);
    expect(screen.getAllByText('605').length).toBeGreaterThan(0);
    expect(screen.getAllByText('703').length).toBeGreaterThan(0);
  });
});
describe('network plans and partial writes', () => {
  it('reviews exact effects, applies only the ID, then refreshes operations and reconciliation after a partial failure', async () => {
    let applied = false;
    const mock = mockApi((path, init) => {
      if (path === '/plans' && init?.method === 'POST')
        return json(JSON.stringify(plan), 201);
      if (path === '/plans/plan-1/apply') {
        applied = true;
        return json(
          '{"error":{"code":"providerUnavailable","message":"Caddy service apply failed"}}',
          502,
        );
      }
      if (path === '/operations' && applied)
        return json(JSON.stringify([partial]));
      if (path === '/reconciliation' && applied)
        return json(
          JSON.stringify({
            ...reconciliation,
            operations: [partial],
            items: reconciliation.items.map((item) =>
              item.key === 'main:604'
                ? {
                    ...item,
                    state: 'mismatch',
                    issues: [
                      'Provider operation is incomplete; recovery required.',
                    ],
                  }
                : item,
            ),
          }),
        );
      return;
    });
    const user = userEvent.setup();
    render(<App />);
    await user.click(
      await screen.findByRole('button', { name: 'Configure immich, VMID 604' }),
    );
    await user.click(
      screen.getByRole('button', { name: 'Review network change' }),
    );
    expect(
      await screen.findByRole('heading', { name: 'Review network change' }),
    ).toBeVisible();
    expect(screen.getByText('upstreamPort')).toBeVisible();
    expect(screen.getByText('2284', { exact: true })).toBeVisible();
    await user.click(
      screen.getByRole('button', { name: 'Apply reviewed change' }),
    );
    await waitFor(() =>
      expect(
        mock.mock.calls.filter(
          (call) => String(call[0]) === '/api/v1/reconciliation',
        ),
      ).toHaveLength(2),
    );
    expect(
      mock.mock.calls.filter(
        (call) => String(call[0]) === '/api/v1/operations',
      ),
    ).toHaveLength(2);
    const applyCall = mock.mock.calls.find((call) =>
      String(call[0]).endsWith('/plans/plan-1/apply'),
    );
    expect(applyCall?.[1]?.body).toBe('{}');
    expect(await screen.findByText(/Apply was attempted/)).toBeVisible();
    expect(
      screen.getByRole('button', { name: 'Apply reviewed change' }),
    ).toBeDisabled();
    const nav = screen.getAllByRole('navigation', {
      name: 'Main navigation',
    })[0];
    if (!nav) throw new Error('Main navigation missing');
    await user.click(within(nav).getByRole('button', { name: /Activity/ }));
    expect(screen.getByText('Partial', { exact: true })).toBeVisible();
    expect(
      screen.getByRole('button', { name: /Review service recovery/ }),
    ).toBeEnabled();
  });
});

describe('settings form revisions and credentials', () => {
  it('uses server shared-policy observations and app values in the common inspector', async () => {
    const id = 'aaaaaaaa-1111-4111-8111-111111111111';
    mockApi((path) => {
      if (path === '/settings')
        return json(
          JSON.stringify({
            ...settings,
            opnsenses: settings.opnsenses.map((o) => ({
              ...o,
              privateAccessListId: id,
            })),
          }),
        );
      if (path === '/reconciliation')
        return json(
          JSON.stringify({
            ...reconciliation,
            items: reconciliation.items.map((item) => ({
              ...item,
              privateAccessList: {
                id,
                name: 'Operator shared VPN',
                description: '',
                cidrs: ['10.99.0.0/24'],
                matcher: 'remote_ip',
                inversion: '0',
                suitable: true,
                reason: '',
              },
            })),
          }),
        );
      return undefined;
    });
    const user = userEvent.setup();
    render(<App />);
    await user.click(
      await screen.findByRole('button', { name: 'Configure immich, VMID 604' }),
    );
    expect(screen.getByLabelText('Upstream port')).toHaveValue(2283);
    expect(screen.getByLabelText('Upstream scheme')).toHaveValue('http');
    expect(screen.getByText(/Default shared private policy/)).toHaveTextContent(
      'Operator shared VPN',
    );
    expect(screen.getByText(/Default shared private policy/)).toHaveTextContent(
      '10.99.0.0/24',
    );
    expect(
      screen.getByText(/Default shared private policy/),
    ).not.toHaveTextContent('192.168.6.0/24');
    await user.click(screen.getByRole('button', { name: /^Rows$/ }));
    expect(screen.getByLabelText('Upstream port')).toHaveValue(2283);
    expect(screen.getByText(/Default shared private policy/)).toHaveTextContent(
      '10.99.0.0/24',
    );
    await user.click(
      screen.getByRole('button', { name: 'Close container details' }),
    );
    await user.click(
      screen.getByRole('button', { name: 'Configure jellyfin, VMID 701' }),
    );
    expect(screen.getByLabelText('Upstream port')).toHaveValue(null);
    expect(screen.getByLabelText('Upstream scheme')).toHaveValue('http');
  });
  it('saves blank credentials by omission and keeps read-only flags off the wire', async () => {
    let body: BodyInit | null | undefined;
    mockApi((path, init) => {
      if (path === '/settings' && init?.method === 'PUT') {
        body = init.body;
        return json(
          JSON.stringify({
            ...settings,
            revision: 8,
            hosts: settings.hosts.map((host) =>
              host.id === 'main' ? { ...host, name: 'Updated name' } : host,
            ),
          }),
        );
      }
      return;
    });
    const user = userEvent.setup();
    render(<App />);
    await screen.findByRole('heading', { name: 'Containers' });
    const nav = screen.getAllByRole('navigation', {
      name: 'Main navigation',
    })[0];
    if (!nav) throw new Error('Main navigation missing');
    await user.click(within(nav).getByRole('button', { name: /Settings/ }));
    const editor = screen.getByRole('group', {
      name: 'Edit Proxmox host main proxmox',
    });
    await user.click(within(editor).getByText('main proxmox'));
    const name = within(editor).getByLabelText('Display name');
    await user.clear(name);
    await user.type(name, 'Updated name');
    expect(
      within(editor).getByRole('button', { name: 'Test saved Proxmox' }),
    ).toBeDisabled();
    await user.click(
      screen.getByRole('button', { name: 'Save configuration' }),
    );
    await screen.findByText(
      'Configuration saved. Connection tests now use this revision.',
    );
    expect(typeof body).toBe('string');
    expect(body).not.toContain('hasTokenSecret');
    expect(body).not.toContain('hasApiKey');
    expect(body).not.toContain('hasApiSecret');
    expect(body).not.toContain('tokenSecret');
    expect(body).not.toContain('apiSecret');
    expect(body).not.toContain('apiKey');
    expect(body).toContain('Updated name');
    expect(body).toContain('"revision":7');
  });
  it('preserves a conflicting settings draft and offers an explicit discard/reload', async () => {
    mockApi((path, init) =>
      path === '/settings' && init?.method === 'PUT'
        ? json(
            '{"error":{"code":"conflict","message":"Configuration revision changed"}}',
            409,
          )
        : undefined,
    );
    const user = userEvent.setup();
    render(<App />);
    await screen.findByRole('heading', { name: 'Containers' });
    const nav = screen.getAllByRole('navigation', {
      name: 'Main navigation',
    })[0];
    if (!nav) throw new Error('Main navigation missing');
    await user.click(within(nav).getByRole('button', { name: /Settings/ }));
    const editor = screen.getByRole('group', {
      name: 'Edit Proxmox host main proxmox',
    });
    await user.click(within(editor).getByText('main proxmox'));
    const name = within(editor).getByLabelText('Display name');
    await user.clear(name);
    await user.type(name, 'Keep this draft');
    await user.click(
      screen.getByRole('button', { name: 'Save configuration' }),
    );
    expect(
      await screen.findByText('Configuration revision changed'),
    ).toBeVisible();
    expect(name).toHaveValue('Keep this draft');
    await user.click(
      screen.getByRole('button', { name: 'Reload saved revision' }),
    );
    expect(name).toHaveValue('Keep this draft');
    expect(
      screen.getByRole('button', { name: 'Discard draft and reload' }),
    ).toBeVisible();
  });
});

it('keeps Settings available when the first inventory read fails', async () => {
  mockApi((path) =>
    path === '/reconciliation'
      ? json(
          '{"error":{"code":"providerUnavailable","message":"Provider read failed"}}',
          502,
        )
      : undefined,
  );
  const user = userEvent.setup();
  render(<App />);
  await screen.findByText('Provider read failed');
  const nav = screen.getAllByRole('navigation', { name: 'Main navigation' })[0];
  if (!nav) throw new Error('Main navigation missing');
  await user.click(within(nav).getByRole('button', { name: /Settings/ }));
  expect(screen.getByRole('heading', { name: 'Settings' })).toBeVisible();
  expect(
    screen.queryByRole('heading', { name: 'Dashboard data unavailable' }),
  ).not.toBeInTheDocument();
});

it('refuses a fresh plan for a different container generation and refreshes the observed state', async () => {
  const mock = mockApi((path, init) =>
    path === '/plans' && init?.method === 'POST'
      ? json(
          JSON.stringify({ ...plan, identity: 'replacement-generation' }),
          201,
        )
      : undefined,
  );
  const user = userEvent.setup();
  render(<App />);
  await user.click(
    await screen.findByRole('button', { name: 'Configure immich, VMID 604' }),
  );
  await user.click(
    screen.getByRole('button', { name: 'Review network change' }),
  );
  expect(
    await screen.findByText(
      /Container identity changed while preparing the plan/,
    ),
  ).toBeVisible();
  expect(
    screen.queryByRole('button', { name: 'Apply reviewed change' }),
  ).not.toBeInTheDocument();
  await waitFor(() =>
    expect(
      mock.mock.calls.filter(
        (call) => String(call[0]) === '/api/v1/reconciliation',
      ),
    ).toHaveLength(2),
  );
});
