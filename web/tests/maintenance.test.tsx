import { render, screen, within, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, expect, it, vi } from 'vitest';
import { App } from '../src/App';
import { Mismatches } from '../src/Mismatches';
import { Api } from '../src/api';
import { reconciliationSchema } from '../src/schemas';
import {
  auth,
  container,
  partial,
  plan,
  reconciliation,
  settings,
} from './fixtures';

const resourceId = 'a1111111-1111-4111-8111-111111111609';
function fixtureApi(
  override: (path: string, init?: RequestInit) => Response | undefined,
) {
  const mock = vi.fn<typeof fetch>(async (input, init) => {
    const path = String(input).replace('/api/v1', '');
    const result = override(path, init);
    if (result) return result;
    const body =
      path === '/auth/status'
        ? auth
        : path === '/settings'
          ? settings
          : path === '/reconciliation'
            ? reconciliation
            : path === '/jobs' || path === '/operations'
              ? []
              : null;
    return Response.json(body, { status: body === null ? 404 : 200 });
  });
  vi.stubGlobal('fetch', mock);
  return mock;
}
afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
});

it('explains the actual disabled SSH flow and opens actionable settings', async () => {
  fixtureApi((path) =>
    path === '/settings'
      ? Response.json({
          ...settings,
          hosts: settings.hosts.map((h) => ({
            ...h,
            ssh: { ...h.ssh, enabled: false },
          })),
        })
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
  expect(
    within(inspector).getByRole('button', { name: 'App update' }),
  ).toBeDisabled();
  await user.click(
    within(inspector).getByRole('button', {
      name: 'Open maintenance settings',
    }),
  );
  expect(screen.getByRole('heading', { name: 'Settings' })).toBeVisible();
  expect(screen.getAllByText('Maintenance SSH setup').length).toBeGreaterThan(
    0,
  );
});

it.each(['ssh', 'demo'])(
  'labels %s capability provenance and disables a missing app command',
  async (source) => {
    fixtureApi((path) =>
      path.endsWith('/maintenance')
        ? Response.json({
            key: container.key,
            identity: container.identity,
            fingerprint: container.fingerprint,
            observedAt: new Date().toISOString(),
            connected: true,
            distribution: 'debian',
            version: '13',
            source,
            appUpdate: false,
            osUpdate: true,
            reason: '',
            appReason:
              'No executable /usr/bin/update exists in this container.',
            osReason: '',
          })
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
    await user.click(
      within(inspector).getByRole('button', {
        name: 'Check maintenance readiness',
      }),
    );
    expect(
      await within(inspector).findByText(
        source === 'ssh' ? /SSH connected/ : /Simulated maintenance/,
      ),
    ).toHaveTextContent(source === 'ssh' ? 'source: SSH' : 'source: demo');
    expect(
      within(inspector).getByRole('button', { name: 'App update' }),
    ).toBeDisabled();
    expect(
      within(inspector).getByRole('button', { name: 'OS update' }),
    ).toBeEnabled();
  },
);

it('reviews precise hostname drift and consumes a metadata-only acceptance after a lost response', async () => {
  const mock = fixtureApi((path, init) => {
    if (path === '/reconciliation')
      return Response.json({
        ...reconciliation,
        items: reconciliation.items.map((item) =>
          item.key === container.key
            ? {
                ...item,
                state: 'mismatch',
                owned: true,
                canAcceptChanges: true,
                issues: ['owned resources changed or are missing'],
                drift: [
                  {
                    id: resourceId,
                    kind: 'reservation',
                    status: 'changed',
                    detail: 'Reviewed hostname changed.',
                    changes: [
                      {
                        field: 'hostname',
                        before: 'speedtest-tracker',
                        after: 'immich',
                      },
                    ],
                  },
                ],
              }
            : item,
        ),
      });
    if (path === '/plans' && init?.method === 'POST')
      return Response.json({
        ...plan,
        action: 'accept',
        effects: [
          {
            kind: 'reservation',
            action: 'acknowledge',
            resourceId,
            name: 'Accept observed hostname',
            changes: [
              {
                field: 'hostname',
                before: 'speedtest-tracker',
                after: 'immich',
              },
            ],
          },
        ],
      });
    if (path.endsWith('/apply')) return new Response('', { status: 502 });
  });
  const user = userEvent.setup();
  render(<App />);
  await user.click(
    await screen.findByRole('button', { name: 'Configure immich, VMID 604' }),
  );
  expect(
    within(
      screen.getByRole('complementary', { name: 'Details for immich' }),
    ).getByLabelText('Owned resource changes'),
  ).toHaveTextContent('speedtest-tracker');
  await user.click(
    screen.getByRole('button', { name: 'Review observed hostname change' }),
  );
  await user.click(
    await screen.findByRole('button', { name: 'Accept reviewed hostname' }),
  );
  expect(await screen.findByText(/response may have been lost/)).toBeVisible();
  expect(
    screen.getByRole('button', { name: 'Accept reviewed hostname' }),
  ).toBeDisabled();
  expect(
    mock.mock.calls.filter((call) => String(call[0]).endsWith('/apply')),
  ).toHaveLength(1);
});

it('requires selected orphan UUIDs and reviews only those effects', async () => {
  const mock = fixtureApi((path, init) => {
    if (path === '/reconciliation')
      return Response.json({
        ...reconciliation,
        items: [
          {
            key: 'main:609',
            state: 'ownedOrphan',
            owned: true,
            issues: ['Container absent.'],
            managedResources: [
              {
                id: resourceId,
                kind: 'reservation',
                name: 'old app',
                summary: 'Owned reservation',
              },
            ],
          },
        ],
      });
    if (path === '/plans' && init?.method === 'POST')
      return Response.json({
        ...plan,
        key: 'main:609',
        vmid: 609,
        action: 'cleanup',
        effects: [
          {
            kind: 'reservation',
            action: 'delete',
            resourceId,
            name: 'Delete selected reservation',
            changes: [],
          },
        ],
      });
    if (path.endsWith('/apply'))
      return Response.json({
        ...partial,
        key: 'main:609',
        action: 'cleanup',
        status: 'succeeded',
      });
  });
  const user = userEvent.setup();
  render(<App />);
  await screen.findByRole('heading', { name: 'Containers' });
  const nav = screen.getAllByRole('navigation', { name: 'Main navigation' })[0];
  if (!nav) throw new Error('missing navigation');
  await user.click(within(nav).getByRole('button', { name: /Mismatches/ }));
  expect(
    screen.getByRole('button', { name: 'Review owned resource cleanup' }),
  ).toBeDisabled();
  await user.click(
    screen.getByRole('checkbox', { name: /Remove stale reservation/ }),
  );
  await user.click(
    screen.getByRole('button', { name: 'Review owned resource cleanup' }),
  );
  expect(
    await screen.findByRole('heading', {
      name: 'Review owned resource cleanup',
    }),
  ).toBeVisible();
  const request = mock.mock.calls.find(
    (call) => String(call[0]) === '/api/v1/plans',
  );
  expect(request?.[1]?.body).toContain(`"resourceIds":["${resourceId}"]`);
  expect(screen.queryByText(/schedule/i)).not.toBeInTheDocument();
});

it('adds a reservation with an empty app port and no proxy effects', async () => {
  fixtureApi((path, init) => {
    if (path === '/reconciliation')
      return Response.json({
        ...reconciliation,
        inventory: {
          ...reconciliation.inventory,
          containers: [{ ...container, desiredRoute: undefined }],
        },
        items: [
          { key: container.key, state: 'missing', owned: false, issues: [] },
        ],
      });
    if (path === '/plans' && init?.method === 'POST')
      return Response.json({
        ...plan,
        action: 'reservation',
        desiredRoute: { scheme: '', port: 0, public: false },
        effects: [
          {
            kind: 'reservation',
            action: 'create',
            resourceId: '',
            name: 'Add reservation only',
            changes: [],
          },
        ],
      });
  });
  const user = userEvent.setup();
  render(<App />);
  await user.click(
    await screen.findByRole('button', { name: 'Configure immich, VMID 604' }),
  );
  expect(screen.getByLabelText('Upstream port')).toHaveValue(null);
  expect(
    screen.getByRole('button', {
      name: 'Add missing proxy / repair configuration',
    }),
  ).toBeDisabled();
  await user.click(
    screen.getByRole('button', { name: 'Add missing reservation' }),
  );
  expect(
    await screen.findByRole('heading', { name: 'Review missing reservation' }),
  ).toBeVisible();
});

it('withdraws cleanup confirmation when current observations change', async () => {
  fixtureApi((path, init) =>
    path === '/plans' && init?.method === 'POST'
      ? Response.json({
          ...plan,
          action: 'cleanup',
          key: 'main:609',
          vmid: 609,
          effects: [
            {
              kind: 'reservation',
              action: 'delete',
              resourceId,
              name: 'Delete selected reservation',
              changes: [],
            },
          ],
        })
      : undefined,
  );
  const data = reconciliationSchema.parse({
    ...reconciliation,
    items: [
      {
        key: 'main:609',
        state: 'ownedOrphan',
        owned: true,
        issues: [],
        managedResources: [
          {
            id: resourceId,
            kind: 'reservation',
            name: 'old app',
            summary: 'Owned',
          },
        ],
      },
    ],
  });
  const api = new Api('', vi.fn());
  const user = userEvent.setup();
  const props = {
    api,
    data,
    settings,
    onInspect: vi.fn(),
    onRefresh: async () => {},
    onActivity: vi.fn(),
  };
  const view = render(<Mismatches {...props} />);
  await user.click(
    screen.getByRole('checkbox', { name: /Remove stale reservation/ }),
  );
  await user.click(
    screen.getByRole('button', { name: 'Review owned resource cleanup' }),
  );
  expect(
    await screen.findByRole('button', {
      name: 'Delete listed owned resources',
    }),
  ).toBeEnabled();
  view.rerender(
    <Mismatches
      {...props}
      data={{ ...data, inventory: { ...data.inventory, complete: false } }}
    />,
  );
  expect(
    screen.getByRole('button', { name: 'Delete listed owned resources' }),
  ).toBeDisabled();
  expect(screen.getByText(/Observations changed after review/)).toBeVisible();
});

it('review regression: allows reservation-only review beside an existing unowned proxy', async () => {
  const requests = fixtureApi((path) =>
    path === '/reconciliation'
      ? Response.json({
          ...reconciliation,
          items: [
            {
              key: container.key,
              state: 'conflict',
              owned: false,
              issues: ['unowned Caddy domain matches this app'],
              canAddReservation: true,
            },
          ],
        })
      : path === '/plans'
        ? Response.json({
            ...plan,
            action: 'reservation',
            desiredRoute: { scheme: '', port: 0, public: false },
          })
        : undefined,
  );
  const user = userEvent.setup();
  render(<App />);
  await user.click(
    await screen.findByRole('button', { name: 'Configure immich, VMID 604' }),
  );
  const button = screen.getByRole('button', {
    name: 'Add missing reservation',
  });
  expect(button).toBeEnabled();
  await user.click(button);
  await waitFor(() =>
    expect(requests).toHaveBeenCalledWith(
      '/api/v1/plans',
      expect.objectContaining({
        method: 'POST',
        body: expect.stringContaining('"action":"reservation"'),
      }),
    ),
  );
});

it('review regression: exposes independent orphan selection despite a whole-app dependency', async () => {
  fixtureApi(() => undefined);
  const data = reconciliationSchema.parse({
    ...reconciliation,
    items: [
      {
        key: 'main:609',
        state: 'conflict',
        owned: true,
        cleanupSelectable: true,
        issues: ['a retained foreign proxy references the reservation'],
        managedResources: [
          {
            id: resourceId,
            kind: 'handle',
            name: 'independent proxy',
            summary: 'Verified owned proxy',
          },
        ],
      },
    ],
  });
  render(
    <Mismatches
      api={new Api('', vi.fn())}
      data={data}
      settings={settings}
      onInspect={vi.fn()}
      onRefresh={async () => {}}
      onActivity={vi.fn()}
    />,
  );
  expect(
    screen.getByRole('checkbox', { name: /Remove stale proxy/ }),
  ).toBeEnabled();
});

it('review regression: removes expired maintenance readiness without an unrelated render', async () => {
  fixtureApi((path) =>
    path.endsWith('/maintenance')
      ? Response.json({
          key: container.key,
          identity: container.identity,
          fingerprint: container.fingerprint,
          observedAt: new Date(Date.now() - 59500).toISOString(),
          connected: true,
          distribution: 'debian',
          version: '13',
          source: 'ssh',
          appUpdate: true,
          osUpdate: true,
          reason: '',
          appReason: '',
          osReason: '',
        })
      : undefined,
  );
  const user = userEvent.setup();
  render(<App />);
  await user.click(
    await screen.findByRole('button', { name: 'Configure immich, VMID 604' }),
  );
  await user.click(
    screen.getByRole('button', { name: 'Check maintenance readiness' }),
  );
  expect(await screen.findByText(/SSH connected/)).toBeVisible();
  await waitFor(
    () => expect(screen.queryByText(/SSH connected/)).not.toBeInTheDocument(),
    { timeout: 1800 },
  );
});
