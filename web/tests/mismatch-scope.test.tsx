import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { App } from '../src/App';
import {
  issueDetailSchema,
  reconciliationSchema,
  type Job,
  type Reconciliation,
  type ReconcileItem,
} from '../src/schemas';
import { mismatchCounts, reviewScope } from '../src/viewmodel';
import {
  auth,
  job,
  partial,
  reconciliation,
  settings,
  withIssues,
} from './fixtures';

const item = (patch: Partial<ReconcileItem>): ReconcileItem => ({
  key: 'main:604',
  state: 'healthy',
  owned: true,
  ...withIssues('', []),
  ...patch,
});
const mixed: Reconciliation = {
  ...reconciliation,
  protectedCandidates: [
    {
      opnsenseId: 'edge',
      resource: { id: 'r1', kind: 'reservation', name: 'old', summary: '' },
      observations: [],
      reason: 'protected',
    },
  ],
  items: [
    // Drift: counted.
    item({
      key: 'main:604',
      state: 'mismatch',
      ...withIssues('routeChanged', ['Route changed.']),
    }),
    // Importable existing configuration: its own group, not counted.
    item({
      key: 'main:606',
      state: 'conflict',
      owned: false,
      importAvailable: true,
      ...withIssues('unownedConflict', ['Existing configuration found.']),
    }),
    // Never configured: its own group, not counted.
    item({
      key: 'pvedell:701',
      state: 'missing',
      owned: false,
      ...withIssues('notConfigured', ['Nothing configured yet.']),
    }),
    // Owned orphan: counted.
    item({
      key: 'main:700',
      state: 'ownedOrphan',
      ...withIssues('ownedOrphan', ['No matching VMID.']),
    }),
    // Unreadable provider: neither confirmed nor counted.
    item({
      key: 'main:701',
      state: 'unknown',
      owned: false,
      ...withIssues('providerUnknown', ['Read failed.']),
    }),
    // A real conflict is drift.
    item({
      key: 'main:702',
      state: 'conflict',
      owned: false,
      ...withIssues('unownedConflict', ['Reservation collides.']),
    }),
  ],
};

describe('review scope and counts', () => {
  it.each<[string, Partial<ReconcileItem>, string]>([
    ['healthy', {}, 'ok'],
    ['unknown', { state: 'unknown' }, 'unverified'],
    ['unowned missing', { state: 'missing', owned: false }, 'notConfigured'],
    [
      'unowned importable conflict',
      { state: 'conflict', owned: false, importAvailable: true },
      'importAvailable',
    ],
    [
      'unowned plain conflict',
      { state: 'conflict', owned: false },
      'needsReview',
    ],
    [
      'unowned importable record with an unfinished operation',
      { state: 'mismatch', owned: false, importAvailable: true },
      'needsReview',
    ],
    ['owned missing resources', { state: 'missing' }, 'needsReview'],
    ['owned mismatch', { state: 'mismatch' }, 'needsReview'],
    ['owned orphan', { state: 'ownedOrphan' }, 'needsReview'],
  ])('%s is %s', (_name, patch, scope) => {
    expect(reviewScope(item(patch))).toBe(scope);
  });

  it('counts stale and drifted records and protected candidates, never setup or unverified ones', () => {
    expect(mismatchCounts(mixed)).toEqual({
      needsReview: 4, // mismatch, orphan, conflict, one protected candidate
      notConfigured: 1,
      importAvailable: 1,
      unverified: 1,
    });
  });
});

describe('issue details schema', () => {
  it('requires a code for every issue and keeps them aligned with the prose', () => {
    const parse = (patch: Partial<ReconcileItem>) =>
      reconciliationSchema.safeParse({
        ...reconciliation,
        items: [item(patch)],
      });
    expect(parse({}).success).toBe(true);
    expect(
      parse({
        issues: ['One'],
        issueDetails: [{ code: 'routeChanged', message: 'Other' }],
      }).success,
    ).toBe(false);
    expect(parse({ issues: ['One'], issueDetails: [] }).success).toBe(false);
    expect(issueDetailSchema.safeParse({ message: 'No code' }).success).toBe(
      false,
    );
  });
});

function mockApi(data: Reconciliation, jobs: Job[] = []) {
  vi.stubGlobal(
    'fetch',
    vi.fn<typeof fetch>(async (input) => {
      const path = String(input).replace('/api/v1', '');
      const body =
        path === '/auth/status'
          ? auth
          : path === '/settings'
            ? settings
            : path === '/reconciliation'
              ? data
              : path === '/jobs'
                ? jobs
                : path === '/operations'
                  ? data.operations
                  : null;
      return Response.json(body, { status: body === null ? 404 : 200 });
    }),
  );
}
beforeEach(() => localStorage.clear());
afterEach(() => vi.unstubAllGlobals());

describe('mismatch counts across the app', () => {
  it('shows the same number in the sidebar, the inventory button and the Mismatches page', async () => {
    mockApi(mixed);
    const user = userEvent.setup();
    render(<App />);
    const review = await screen.findByRole('button', { name: /need review/ });
    expect(review).toHaveTextContent('4 need review');
    const nav = screen.getByRole('navigation', { name: 'Main navigation' });
    const link = within(nav).getByRole('button', { name: /Mismatches/ });
    expect(within(link).getByText('4')).toBeVisible();

    await user.click(link);
    await screen.findByRole('heading', { name: 'Mismatches' });
    expect(
      screen.getByText('4 need review', { selector: '.badge' }),
    ).toBeVisible();
    // Setup records are grouped apart and not described as drift.
    expect(
      screen.getByRole('heading', { name: 'Not configured' }),
    ).toBeVisible();
    expect(
      screen.getByRole('heading', { name: 'Import available' }),
    ).toBeVisible();
    expect(screen.getByRole('heading', { name: 'Unverified' })).toBeVisible();
  });

  it('filters the inventory by configuration scope', async () => {
    mockApi(mixed);
    const user = userEvent.setup();
    render(<App />);
    await screen.findByRole('heading', { name: 'Containers' });
    const select = screen.getByLabelText('Configuration');
    await user.selectOptions(select, 'notConfigured');
    expect(
      screen.getByRole('button', { name: /Inspect jellyfin/ }),
    ).toBeVisible();
    expect(
      screen.queryByRole('button', { name: /Inspect vaultwarden/ }),
    ).toBeNull();
    await user.selectOptions(select, 'importAvailable');
    expect(
      screen.getByRole('button', { name: /Inspect vaultwarden/ }),
    ).toBeVisible();
    expect(
      screen.queryByRole('button', { name: /Inspect jellyfin/ }),
    ).toBeNull();
    await user.selectOptions(select, 'needsReview');
    expect(
      screen.getByRole('button', { name: /Inspect immich/ }),
    ).toBeVisible();
    expect(
      screen.queryByRole('button', { name: /Inspect jellyfin/ }),
    ).toBeNull();
  });

  it('hides the review count when nothing is stale or drifted', async () => {
    mockApi({
      ...reconciliation,
      items: [
        item({
          key: 'pvedell:701',
          state: 'missing',
          owned: false,
          ...withIssues('notConfigured', ['Nothing configured yet.']),
        }),
      ],
    });
    render(<App />);
    await screen.findByRole('heading', { name: 'Containers' });
    expect(screen.queryByRole('button', { name: /need review/ })).toBeNull();
    const nav = screen.getByRole('navigation', { name: 'Main navigation' });
    expect(
      within(
        within(nav).getByRole('button', { name: /Mismatches/ }),
      ).queryByText(/^\d+$/),
    ).toBeNull();
  });
});

describe('activity count', () => {
  const activityBadge = async () => {
    const nav = await screen.findByRole('navigation', {
      name: 'Main navigation',
    });
    return within(
      within(nav).getByRole('button', { name: /Activity/ }),
    ).queryByText(/^\d+$/);
  };
  it('does not count failed jobs', async () => {
    mockApi(reconciliation, [{ ...job, status: 'failed' }]);
    render(<App />);
    await screen.findByRole('heading', { name: 'Containers' });
    expect(await activityBadge()).toBeNull();
  });
  it('counts uncertain jobs and unfinished operations only', async () => {
    mockApi({ ...reconciliation, operations: [partial] }, [
      { ...job, id: 'a', status: 'timedOut' },
      { ...job, id: 'b', status: 'timedOut', acknowledged: true },
      { ...job, id: 'c', status: 'failed' },
    ]);
    render(<App />);
    await screen.findByRole('heading', { name: 'Containers' });
    expect(
      await screen.findByText('2', { selector: '.nav-count' }),
    ).toBeVisible();
  });
});
