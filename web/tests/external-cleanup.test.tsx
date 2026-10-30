import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, expect, it, vi } from 'vitest';
import { z } from 'zod';
import { Mismatches } from '../src/Mismatches';
import { Activity } from '../src/Activity';
import { Api } from '../src/api';
import {
  operationSchema,
  planSchema,
  reconciliationSchema,
} from '../src/schemas';
import { partial, plan, reconciliation, settings } from './fixtures';

const reservation = 'a1111111-1111-4111-8111-111111111609';
const child = 'a4444444-4444-4444-8444-444444444609';
const handle = 'a5555555-5555-4555-8555-555555555609';
const parent = 'a3333333-3333-4333-8333-333333333000';
const targets = [
  {
    id: reservation,
    kind: 'reservation',
    fingerprint: 'reservation-fingerprint',
  },
  { id: child, kind: 'subdomain', fingerprint: 'child-fingerprint' },
  { id: handle, kind: 'handle', fingerprint: 'handle-fingerprint' },
];
const review = {
  address: {
    hostId: 'main',
    vmid: 609,
    ip: '192.168.6.9',
    mac: 'd0:cc:37:00:06:09',
  },
  targets,
  contextHash: 'full-context',
  externalUseAcknowledged: true,
};
const data = reconciliationSchema.parse({
  ...reconciliation,
  items: [],
  inventory: { ...reconciliation.inventory, containers: [] },
  protectedCandidates: [
    ...targets.map((t) => ({
      opnsenseId: 'edge',
      resource: {
        id: t.id,
        kind: t.kind,
        name: 'Legacy app',
        summary: 'Synthetic unused record',
      },
      observations: [],
      reason: 'Unique profile mapping and complete absent occupancy.',
      eligible: true,
      hostId: 'main',
      vmid: 609,
      fingerprint: t.fingerprint,
    })),
    {
      opnsenseId: 'edge',
      resource: {
        id: parent,
        kind: 'domain',
        name: '*.example.test',
        summary: 'Shared wildcard parent',
      },
      observations: [],
      reason:
        'Protected: wildcard parent and sibling consumers stay protected.',
      eligible: false,
      fingerprint: 'parent-fingerprint',
    },
  ],
});
const externalPlan = planSchema.parse({
  ...plan,
  id: 'external-plan',
  action: 'externalCleanup',
  key: 'main:609',
  vmid: 609,
  public: false,
  desiredRoute: { scheme: '', port: 0, public: false },
  externalCleanup: review,
  effects: targets.map((t) => ({
    resourceId: t.id,
    kind: t.kind,
    action: 'delete',
    name: `Delete selected unowned ${t.kind}`,
    changes: [],
  })),
});
const operation = operationSchema.parse({
  ...partial,
  action: 'externalCleanup',
  key: 'main:609',
  planId: externalPlan.id,
  externalCleanup: review,
  cleanupTargets: targets,
  resources: targets,
  step: 'delete:handle',
  manualRecovery: true,
});
const requestSchema = z.object({
  action: z.literal('externalCleanup'),
  hostId: z.literal('main'),
  vmid: z.literal(609),
  externalUseAcknowledged: z.literal(true),
  selectedResources: z.array(
    z.object({ id: z.string(), kind: z.string(), fingerprint: z.string() }),
  ),
  recoveryId: z.string().optional(),
});
function mockApi(lost = false, badEffect = false) {
  const mock = vi.fn<typeof fetch>(async (input, init) => {
    const path = String(input).replace('/api/v1', '');
    if (path === '/plans') {
      const request = requestSchema.parse(
        JSON.parse(z.string().parse(init?.body)),
      );
      return Response.json({
        ...externalPlan,
        externalCleanup: {
          ...review,
          ...(request.recoveryId ? { recoveryId: request.recoveryId } : {}),
        },
        effects: badEffect
          ? [
              {
                kind: 'acl',
                resourceId: parent,
                action: 'delete',
                name: 'Unexpected',
                changes: [],
              },
            ]
          : externalPlan.effects,
      });
    }
    if (path.endsWith('/apply')) {
      if (lost) throw new Error('Response lost');
      return Response.json({
        ...operation,
        status: 'succeeded',
        resources: [],
        manualRecovery: false,
        step: 'complete',
      });
    }
    return Response.json([]);
  });
  vi.stubGlobal('fetch', mock);
  return mock;
}
afterEach(() => vi.unstubAllGlobals());
const api = new Api('', () => {});
function view(onRefresh = vi.fn(async () => {})) {
  return (
    <Mismatches
      api={api}
      data={data}
      settings={settings}
      onInspect={() => {}}
      onRefresh={onRefresh}
      onActivity={() => {}}
    />
  );
}
async function select(user: ReturnType<typeof userEvent.setup>) {
  for (const target of targets)
    await user.click(
      screen.getByRole('checkbox', { name: new RegExp(target.id) }),
    );
}
it('requires exact selection and external acknowledgement; wildcard parent has no delete selection', async () => {
  const mock = mockApi();
  const user = userEvent.setup();
  render(view());
  expect(
    screen.queryByRole('checkbox', { name: new RegExp(parent) }),
  ).not.toBeInTheDocument();
  const prepare = screen.getByRole('button', {
    name: 'Review selected external cleanup',
  });
  await select(user);
  expect(prepare).toBeDisabled();
  await user.click(
    screen.getByRole('checkbox', {
      name: /I verified these selected UUIDs are unused/,
    }),
  );
  await user.click(prepare);
  expect(mock).toHaveBeenCalledTimes(1);
  const reviewSection = screen.getByRole('region', {
    name: 'Review network plan',
  });
  const apply = within(reviewSection).getByRole('button', {
    name: 'Delete listed external resources',
  });
  expect(apply).toBeDisabled();
  await user.click(
    within(reviewSection).getByRole('checkbox', {
      name: /I confirm these exact UUIDs remain unused/,
    }),
  );
  await user.click(apply);
  expect(mock).toHaveBeenCalledTimes(2);
});
it('withdraws external acknowledgement when selected UUIDs change', async () => {
  mockApi();
  const user = userEvent.setup();
  render(view());
  await select(user);
  const ack = screen.getByRole('checkbox', {
    name: /I verified these selected UUIDs are unused/,
  });
  await user.click(ack);
  await user.click(screen.getByRole('checkbox', { name: new RegExp(handle) }));
  expect(ack).not.toBeChecked();
  expect(
    screen.getByRole('button', { name: 'Review selected external cleanup' }),
  ).toBeDisabled();
});
it('never retries a lost external apply response and withdraws a plan on refreshed observations', async () => {
  const mock = mockApi(true);
  const user = userEvent.setup();
  const refresh = vi.fn(async () => {});
  const rendered = render(view(refresh));
  await select(user);
  await user.click(
    screen.getByRole('checkbox', {
      name: /I verified these selected UUIDs are unused/,
    }),
  );
  await user.click(
    screen.getByRole('button', { name: 'Review selected external cleanup' }),
  );
  await user.click(
    screen.getByRole('checkbox', {
      name: /I confirm these exact UUIDs remain unused/,
    }),
  );
  const apply = screen.getByRole('button', {
    name: 'Delete listed external resources',
  });
  await user.click(apply);
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'No automatic retry was sent',
  );
  await user.click(apply);
  expect(mock).toHaveBeenCalledTimes(2);
  expect(refresh).toHaveBeenCalledTimes(1);
  rendered.rerender(
    <Mismatches
      api={api}
      data={{
        ...data,
        protectedCandidates: [],
        inventory: { ...data.inventory, complete: false },
      }}
      settings={settings}
      onInspect={() => {}}
      onRefresh={refresh}
      onActivity={() => {}}
    />,
  );
  expect(apply).toBeDisabled();
  expect(screen.getByRole('alert')).toHaveTextContent(
    'No automatic retry was sent',
  );
  expect(screen.getByText(/Observations changed after review/)).toBeVisible();
});
it('blocks mismatched external effects and incomplete inventory', async () => {
  mockApi(false, true);
  const user = userEvent.setup();
  const rendered = render(view());
  await select(user);
  await user.click(
    screen.getByRole('checkbox', {
      name: /I verified these selected UUIDs are unused/,
    }),
  );
  await user.click(
    screen.getByRole('button', { name: 'Review selected external cleanup' }),
  );
  expect(
    screen.getByRole('button', { name: 'Delete listed external resources' }),
  ).toBeDisabled();
  expect(
    screen.getByText(/External cleanup effects do not match/),
  ).toBeVisible();
  rendered.rerender(
    <Mismatches
      api={api}
      data={{ ...data, inventory: { ...data.inventory, complete: false } }}
      settings={settings}
      onInspect={() => {}}
      onRefresh={async () => {}}
      onActivity={() => {}}
    />,
  );
  expect(
    screen.queryByRole('button', { name: 'Review selected external cleanup' }),
  ).not.toBeInTheDocument();
});
it('offers deliberately reviewed external recovery and preserves all original deletion UUIDs', async () => {
  const mock = mockApi();
  const user = userEvent.setup();
  render(
    <Activity
      api={api}
      operations={[operation]}
      jobs={[]}
      loaded
      error=""
      onRefresh={async () => {}}
    />,
  );
  expect(
    screen.queryByRole('button', { name: 'Review service recovery' }),
  ).not.toBeInTheDocument();
  expect(screen.getByText(/freshly inspect all original UUIDs/)).toBeVisible();
  await user.click(
    screen.getByRole('checkbox', {
      name: /I verified these selected UUIDs are unused/,
    }),
  );
  await user.click(
    screen.getByRole('button', {
      name: 'Review fresh external cleanup recovery',
    }),
  );
  const init = mock.mock.calls[0]?.[1];
  const request = requestSchema.parse(JSON.parse(z.string().parse(init?.body)));
  expect(request.recoveryId).toBe(operation.id);
  expect(request.selectedResources).toEqual(targets);
  expect(
    screen.getByRole('button', { name: 'Delete listed external resources' }),
  ).toBeDisabled();
});

it('retains precise preparation refusal when refreshed candidates disappear', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn<typeof fetch>(async () =>
      Response.json(
        {
          error: {
            code: 'conflict',
            message:
              'Retained handle on another configured firewall still references this reservation.',
          },
        },
        { status: 409 },
      ),
    ),
  );
  const user = userEvent.setup();
  const rendered = render(view());
  await select(user);
  await user.click(
    screen.getByRole('checkbox', {
      name: /I verified these selected UUIDs are unused/,
    }),
  );
  await user.click(
    screen.getByRole('button', { name: 'Review selected external cleanup' }),
  );
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'another configured firewall',
  );
  rendered.rerender(
    <Mismatches
      api={api}
      data={{
        ...data,
        protectedCandidates: [],
        inventory: { ...data.inventory, complete: false },
      }}
      settings={settings}
      onInspect={() => {}}
      onRefresh={async () => {}}
      onActivity={() => {}}
    />,
  );
  expect(screen.getByRole('alert')).toHaveTextContent(
    'another configured firewall',
  );
});
