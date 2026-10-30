import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, expect, it, vi } from 'vitest';
import { App } from '../src/App';
import { Api } from '../src/api';
import { ContainerDetail, PlanReview } from '../src/ContainerDetail';
import { itemSchema, planSchema, reconciliationSchema } from '../src/schemas';
import { containerViews, importBlocked } from '../src/viewmodel';
import {
  auth,
  container,
  job,
  partial,
  plan,
  reconciliation,
  settings,
} from './fixtures';

const route = { scheme: 'http', port: 8080, public: true };
const existing = itemSchema.parse({
  key: 'main:609',
  state: 'conflict',
  owned: false,
  issues: ['unowned reservation or domain conflicts'],
  importAvailable: true,
  importIssues: [],
  existingRoute: route,
  existingResources: [
    {
      kind: 'reservation',
      id: 'a1111111-1111-4111-8111-111111111609',
      name: 'speedtest-tracker',
      summary:
        'Exact IP and MAC match: 192.168.6.9; d0:cc:37:00:06:09; subnet a2222222-2222-4222-8222-222222222006 (192.168.6.0/24). Hostname speedtest-tracker is preserved; expected hikyo.',
    },
    {
      kind: 'subdomain',
      id: 'a4444444-4444-4444-8444-444444444609',
      name: 'hikyo.went.io',
      summary:
        'Enabled native child of *.went.io. Public access, no child ACL.',
    },
    {
      kind: 'handle',
      id: 'a5555555-5555-4555-8555-555555555609',
      name: 'hikyo upstream',
      summary:
        'Enabled reverse_proxy to http://192.168.6.9:8080. TLS and insecure-skip-verify disabled.',
    },
    {
      kind: 'reverse',
      id: 'a3333333-3333-4333-8333-333333333000',
      name: '*.went.io',
      summary:
        'Shared parent retained, externally owned. Automatic HTTPS and DNS challenge preserved.',
    },
  ],
});
function observation() {
  return reconciliationSchema.parse({
    ...reconciliation,
    inventory: {
      ...reconciliation.inventory,
      observedAt: new Date().toISOString(),
      containers: [
        {
          ...container,
          key: existing.key,
          vmid: 609,
          name: 'hikyo',
          ip: '192.168.6.9',
          expectedIp: '192.168.6.9',
          mac: 'd0:cc:37:00:06:09',
          expectedMac: 'd0:cc:37:00:06:09',
          identity: 'generation-609',
          desiredRoute: undefined,
        },
      ],
    },
    items: [existing],
  });
}
function reviewedPlan() {
  return planSchema.parse({
    ...plan,
    id: 'import-609',
    action: 'import',
    key: existing.key,
    vmid: 609,
    identity: 'generation-609',
    public: true,
    desiredRoute: route,
    effects: existing.existingResources?.map((resource) => ({
      kind: resource.kind,
      resourceId: resource.id,
      name: resource.name,
      action: resource.kind === 'reverse' ? 'retain' : 'adopt',
      changes: [
        {
          field: 'current configuration',
          before: resource.summary,
          after: resource.summary,
        },
      ],
    })),
    applyNotes: [
      'No provider writes. Stale DHCP hostname speedtest-tracker is preserved. Shared wildcard and LocalIPandVPN/client_ip ACL remain externally owned.',
    ],
  });
}
afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
});

it('parses only the allowlisted existing-resource shape and preserves the metadata-only plan contract', () => {
  const resource = existing.existingResources?.[0];
  const parsed = itemSchema.parse({
    ...existing,
    existingResources: [{ ...resource, fields: { secret: 'must not escape' } }],
  });
  expect(parsed.existingResources?.[0]).not.toHaveProperty('fields');
  expect(reviewedPlan().effects.map((effect) => effect.action)).toEqual([
    'adopt',
    'adopt',
    'adopt',
    'retain',
  ]);
});

it('requires current identity, profile, complete observations and the union of operation/job guards for import', () => {
  const data = observation();
  const [view] = containerViews(data, settings);
  if (!view) throw new Error('Missing fixture view');
  expect(importBlocked(view, data, [], true)).toBe('');
  expect(importBlocked(view, data, [], false)).toMatch(/activity history/);
  expect(
    importBlocked(view, data, [{ ...job, key: view.container.key }], true),
  ).toMatch(/update is active/);
  expect(importBlocked(view, data, [], true, 'uncertain')).toMatch(
    /outcome inspection/,
  );
  expect(
    importBlocked(
      view,
      { ...data, operations: [{ ...partial, key: view.container.key }] },
      [],
      true,
    ),
  ).toMatch(/incomplete network operation/);
  expect(
    importBlocked(
      { ...view, container: { ...view.container, identity: '' } },
      data,
      [],
      true,
    ),
  ).toMatch(/generation is unverified/);
  expect(
    importBlocked(
      { ...view, container: { ...view.container, expectedIp: '192.168.6.10' } },
      data,
      [],
      true,
    ),
  ).toMatch(/host profile/);
  expect(
    importBlocked(
      view,
      { ...data, inventory: { ...data.inventory, complete: false } },
      [],
      true,
    ),
  ).toMatch(/complete inventory/);
  expect(
    importBlocked(
      view,
      {
        ...data,
        inventory: {
          ...data.inventory,
          observedAt: new Date(Date.now() - 360_000).toISOString(),
        },
      },
      [],
      true,
    ),
  ).toMatch(/complete inventory/);
  expect(
    importBlocked(
      {
        ...view,
        item: {
          ...existing,
          importAvailable: false,
          importIssues: ['Two handles match; select no route.'],
        },
      },
      data,
      [],
      true,
    ),
  ).toBe('Two handles match; select no route.');
});

it('requires ownership and existing-public acknowledgements, consumes both on a lost response, and never retries', async () => {
  const fetchMock = vi.fn<typeof fetch>(async () => {
    throw new TypeError('Lost response');
  });
  vi.stubGlobal('fetch', fetchMock);
  const user = userEvent.setup();
  const refresh = vi.fn(async () => {});
  const reviewed = reviewedPlan();
  render(
    <PlanReview
      api={new Api('', vi.fn())}
      plan={reviewed}
      onDone={refresh}
      onClose={vi.fn()}
    />,
  );
  const apply = screen.getByRole('button', {
    name: 'Import reviewed configuration',
  });
  const ownership = screen.getByLabelText(/accept managed ownership/);
  const exposure = screen.getByLabelText(
    /I acknowledge this import preserves existing public access/,
  );
  expect(apply).toBeDisabled();
  expect(
    screen.queryByText(/removes the private allowlist attachment/),
  ).not.toBeInTheDocument();
  await user.click(ownership);
  expect(apply).toBeDisabled();
  await user.click(exposure);
  expect(apply).toBeEnabled();
  await user.dblClick(apply);
  await screen.findByText(/Apply was attempted/);
  expect(apply).toBeDisabled();
  expect(ownership).not.toBeChecked();
  expect(exposure).not.toBeChecked();
  expect(fetchMock).toHaveBeenCalledTimes(1);
  expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/v1/plans/import-609/apply');
  expect(fetchMock.mock.calls[0]?.[1]?.body).toBe('{}');
  expect(refresh).toHaveBeenCalledTimes(1);
  expect(screen.getByRole('alert')).toHaveTextContent(
    'lost after ownership was recorded',
  );
});

it('updates an untouched inspector when existing-route observations arrive, while preserving an edited network draft', async () => {
  const observed = observation();
  const initial = {
    ...observed,
    items: observed.items.map((item) => ({
      ...item,
      existingRoute: undefined,
    })),
  };
  const [first] = containerViews(initial, settings);
  const [current] = containerViews(observed, settings);
  if (!first || !current) throw new Error('Missing fixture views');
  const props = {
    api: new Api('', vi.fn()),
    jobs: [],
    activityLoaded: true,
    onRefresh: vi.fn(async () => {}),
    onJob: vi.fn(),
    onClose: vi.fn(),
    onJobReviewRequest: vi.fn(),
    onJobAttemptState: vi.fn(),
  };
  const { rerender } = render(
    <ContainerDetail {...props} view={first} data={initial} />,
  );
  expect(screen.getByLabelText('Upstream scheme')).toHaveValue('');
  expect(screen.getByLabelText('Upstream port')).toHaveValue(null);
  rerender(<ContainerDetail {...props} view={current} data={observed} />);
  expect(screen.getByLabelText('Upstream port')).toHaveValue(8080);
  expect(screen.getByLabelText('Access policy')).toHaveValue('public');
  const user = userEvent.setup();
  await user.clear(screen.getByLabelText('Upstream port'));
  await user.type(screen.getByLabelText('Upstream port'), '9090');
  const refreshed = observation();
  const [next] = containerViews(refreshed, settings);
  if (!next) throw new Error('Missing refreshed view');
  rerender(<ContainerDetail {...props} view={next} data={refreshed} />);
  expect(screen.getByLabelText('Upstream port')).toHaveValue(9090);
});

it('rejects provider-changing import effects even after both acknowledgements', async () => {
  const user = userEvent.setup();
  const reviewed = reviewedPlan();
  render(
    <PlanReview
      api={new Api('', vi.fn())}
      plan={{
        ...reviewed,
        effects: [
          ...reviewed.effects,
          {
            kind: 'handle',
            action: 'update',
            name: 'unexpected write',
            resourceId: 'handle-id',
            changes: [],
          },
        ],
      }}
      onDone={vi.fn(async () => {})}
      onClose={vi.fn()}
    />,
  );
  await user.click(screen.getByLabelText(/accept managed ownership/));
  await user.click(
    screen.getByLabelText(/I acknowledge this import preserves/),
  );
  expect(
    screen.getByRole('button', { name: 'Import reviewed configuration' }),
  ).toBeDisabled();
  expect(
    screen.getByText(/Import returned a provider-changing effect/),
  ).toBeVisible();
});

it('keeps HTTP 8080 and hostname drift after importing observed native child resources', async () => {
  let imported = false;
  const requests: string[] = [];
  const reviewed = reviewedPlan();
  vi.stubGlobal(
    'fetch',
    vi.fn<typeof fetch>(async (input, options) => {
      const path = String(input).replace('/api/v1', '');
      let value: object = [];
      if (path === '/auth/status') value = auth;
      if (path === '/settings') value = settings;
      if (path === '/reconciliation') {
        const data = observation();
        value = imported
          ? {
              ...data,
              inventory: {
                ...data.inventory,
                containers: data.inventory.containers.map((ct) => ({
                  ...ct,
                  desiredRoute: route,
                })),
              },
              items: [
                {
                  ...existing,
                  owned: true,
                  state: 'mismatch',
                  importAvailable: false,
                  desiredRoute: route,
                  issues: [
                    'DHCP hostname speedtest-tracker differs from hikyo; review a network change to align it.',
                  ],
                },
              ],
            }
          : data;
      }
      if (options?.method === 'POST') {
        requests.push(`${path} ${options.body}`);
        if (path === '/plans') value = reviewed;
        else if (path === '/plans/import-609/apply') {
          imported = true;
          value = {
            ...partial,
            planId: reviewed.id,
            key: reviewed.key,
            action: 'import',
            status: 'succeeded',
            error: '',
            step: 'ownership recorded',
            resources: [],
          };
        } else throw new Error('Unexpected mutation');
      }
      return new Response(JSON.stringify(value));
    }),
  );
  const user = userEvent.setup();
  render(<App />);
  await user.click(
    await screen.findByRole('button', { name: 'Configure hikyo, VMID 609' }),
  );
  const inspector = screen.getByRole('complementary', {
    name: 'Details for hikyo',
  });
  const detail = within(inspector);
  expect(detail.getByLabelText('Upstream port')).toHaveValue(8080);
  expect(
    detail.getByText('a1111111-1111-4111-8111-111111111609'),
  ).toBeVisible();
  expect(
    detail.queryByText('unowned reservation or domain conflicts'),
  ).not.toBeInTheDocument();
  await user.click(
    detail.getByRole('button', { name: 'Review existing configuration' }),
  );
  const review = within(
    screen.getByRole('region', { name: 'Review existing configuration plan' }),
  );
  expect(
    review.getByText('HTTP upstream, port 8080.', { exact: false }),
  ).toBeVisible();
  await user.click(review.getByLabelText(/accept managed ownership/));
  await user.click(
    review.getByLabelText(/I acknowledge this import preserves/),
  );
  await user.click(
    review.getByRole('button', { name: 'Import reviewed configuration' }),
  );
  await waitFor(() =>
    expect(
      screen.queryByRole('region', {
        name: 'Review existing configuration plan',
      }),
    ).not.toBeInTheDocument(),
  );
  expect(detail.getByLabelText('Upstream port')).toHaveValue(8080);
  expect(
    detail.getByText(/DHCP hostname speedtest-tracker differs/),
  ).toBeVisible();
  expect(
    detail.getByRole('button', { name: 'Review network change' }),
  ).toBeEnabled();
  expect(requests).toEqual([
    '/plans {"action":"import","hostId":"main","vmid":609}',
    '/plans/import-609/apply {}',
  ]);
});
