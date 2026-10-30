import { expect, test, type Page } from '@playwright/test';
import { z } from 'zod';
import { auth, reconciliation, settings, plan, partial } from '../fixtures';

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

const baseline = process.env.DASHBOARD_EXTERNAL_BASELINE === '1';
const evidence = process.env.DASHBOARD_EXTERNAL_EVIDENCE_DIR;
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
const candidates = [
  ...targets.map((r) => ({
    opnsenseId: 'demo-edge',
    resource: {
      id: r.id,
      kind: r.kind,
      name: 'Legacy app',
      summary: 'Synthetic unused legacy app record',
    },
    observations: [
      { field: 'profile address', before: '192.168.6.9', after: '192.168.6.9' },
    ],
    reason:
      'Unique absent profile VMID. External-use acknowledgement is required.',
    eligible: true,
    hostId: 'main',
    vmid: 609,
    fingerprint: r.fingerprint,
  })),
  {
    opnsenseId: 'demo-edge',
    resource: {
      id: parent,
      kind: 'domain',
      name: '*.went.io',
      summary: 'Shared wildcard parent',
    },
    observations: [],
    reason: 'Protected: wildcard/shared parent stays protected.',
    eligible: false,
    fingerprint: 'parent-fingerprint',
  },
];
const cleanupPlan = {
  ...plan,
  id: 'external-cleanup-plan',
  action: 'externalCleanup',
  key: 'main:609',
  vmid: 609,
  public: false,
  desiredRoute: { scheme: '', port: 0, public: false },
  externalCleanup: review,
  effects: targets.map((r) => ({
    kind: r.kind,
    action: 'delete',
    resourceId: r.id,
    name: `Delete selected unowned ${r.kind}`,
    changes: [],
  })),
  applyNotes: [
    'No ownership is adopted. Caddy is applied before releasing the reservation.',
  ],
  expiresAt: new Date(Date.now() + 300000).toISOString(),
};
async function fixtures(page: Page, lost = false) {
  const state = { applies: 0, plans: 0, acknowledged: false, changed: false };
  await page.route('**/api/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname.replace('/api/v1', '');
    if (path === '/plans') {
      state.plans++;
      const request = requestSchema.parse(route.request().postDataJSON());
      state.acknowledged = request.externalUseAcknowledged === true;
      await route.fulfill({ json: cleanupPlan, status: 201 });
      return;
    }
    if (path.endsWith('/apply')) {
      state.applies++;
      if (lost) {
        await route.abort();
        return;
      }
      await route.fulfill({
        json: {
          ...partial,
          id: 'external-operation',
          key: 'main:609',
          planId: cleanupPlan.id,
          action: 'externalCleanup',
          externalCleanup: review,
          status: 'succeeded',
          manualRecovery: false,
          step: 'complete',
          error: '',
          resources: [],
        },
      });
      return;
    }
    const body =
      path === '/auth/status'
        ? auth
        : path === '/settings'
          ? settings
          : path === '/reconciliation'
            ? {
                ...reconciliation,
                inventory: {
                  ...reconciliation.inventory,
                  observedAt: new Date().toISOString(),
                  containers: [],
                  complete: !state.changed,
                },
                items: [],
                protectedCandidates: candidates,
              }
            : path === '/operations' || path === '/jobs'
              ? []
              : null;
    await route.fulfill({ json: body, status: body === null ? 404 : 200 });
  });
  return state;
}
async function openMismatches(page: Page) {
  await page.goto('/');
  const menu = page.getByRole('button', { name: 'Open navigation' });
  if (await menu.isVisible()) await menu.click();
  await page.getByRole('button', { name: 'Mismatches', exact: true }).click();
}

test('external cleanup at matching desktop/mobile sizes and themes', async ({
  page,
}) => {
  await fixtures(page);
  for (const size of [
    { name: 'desktop', width: 1600, height: 1000 },
    { name: 'mobile', width: 390, height: 844 },
  ]) {
    for (const colorScheme of ['light', 'dark'] satisfies (
      'light' | 'dark'
    )[]) {
      await page.setViewportSize(size);
      await page.emulateMedia({ colorScheme });
      await openMismatches(page);
      if (!baseline) {
        await expect(
          page.getByText('Possible stale external records', { exact: true }),
        ).toBeVisible();
        await expect(
          page.getByRole('checkbox', { name: new RegExp(reservation) }),
        ).toBeVisible();
        await expect(
          page.getByRole('checkbox', { name: new RegExp(parent) }),
        ).toHaveCount(0);
      }
      const toast = page.getByRole('button', {
        name: 'Dismiss loading status',
      });
      if (await toast.isVisible()) await toast.click();
      if (evidence)
        await page.screenshot({
          path: `${evidence}/${baseline ? 'before' : 'after'}-external-cleanup-${size.name}-${colorScheme}.png`,
          fullPage: true,
        });
      expect(
        await page.evaluate(
          () =>
            document.documentElement.scrollWidth >
            document.documentElement.clientWidth,
        ),
      ).toBe(false);
    }
  }
});

test('external acknowledgement, exact selection, lost response and changed observation', async ({
  page,
}) => {
  test.skip(baseline, 'Previous candidate has no external cleanup controls');
  const state = await fixtures(page, true);
  await openMismatches(page);
  for (const target of targets)
    await page.getByRole('checkbox', { name: new RegExp(target.id) }).check();
  const prepare = page.getByRole('button', {
    name: 'Review selected external cleanup',
  });
  await expect(prepare).toBeDisabled();
  await page
    .getByRole('checkbox', {
      name: /I verified these selected UUIDs are unused/,
    })
    .check();
  await prepare.click();
  expect(state.acknowledged).toBe(true);
  expect(state.plans).toBe(1);
  const apply = page.getByRole('button', {
    name: 'Delete listed external resources',
  });
  await expect(apply).toBeDisabled();
  await page
    .getByRole('checkbox', {
      name: /I confirm these exact UUIDs remain unused/,
    })
    .check();
  await apply.click();
  await expect(page.getByRole('alert')).toContainText(
    'No automatic retry was sent',
  );
  await expect(apply).toBeDisabled();
  expect(state.applies).toBe(1);
  await page.getByRole('button', { name: 'Close plan' }).click();
  // A refreshed observation withdraws both selection acknowledgement and review.
  for (const target of targets)
    await page.getByRole('checkbox', { name: new RegExp(target.id) }).check();
  await page
    .getByRole('checkbox', {
      name: /I verified these selected UUIDs are unused/,
    })
    .check();
  await prepare.click();
  state.changed = true;
  await page.getByRole('button', { name: 'Refresh', exact: true }).click();
  await expect(apply).toBeDisabled();
  expect(state.applies).toBe(1);
});

test('partial external cleanup recovery rechecks address reuse and preserves failed operation evidence', async ({
  page,
}) => {
  test.skip(baseline, 'Previous candidate has no external cleanup recovery');
  let addressReused = false;
  let applies = 0;
  let plans = 0;
  const operation = {
    ...partial,
    id: 'external-recovery',
    key: 'main:609',
    planId: 'original-external-plan',
    action: 'externalCleanup',
    externalCleanup: review,
    cleanupTargets: targets,
    resources: [targets[0]],
    step: 'apply:caddy',
    manualRecovery: true,
    error: 'External cleanup incomplete; no automatic retry was sent.',
  };
  await page.route('**/api/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname.replace('/api/v1', '');
    if (path === '/plans') {
      plans++;
      const request = requestSchema.parse(route.request().postDataJSON());
      expect(request.recoveryId).toBe(operation.id);
      expect(request.selectedResources).toEqual(targets);
      if (addressReused) {
        await route.fulfill({
          status: 409,
          json: {
            error: {
              code: 'conflict',
              message:
                'VMID now exists in complete cluster occupancy; address reuse blocks cleanup.',
            },
          },
        });
        return;
      }
      await route.fulfill({
        status: 201,
        json: {
          ...cleanupPlan,
          externalCleanup: { ...review, recoveryId: operation.id },
        },
      });
      return;
    }
    if (path.endsWith('/apply')) {
      applies++;
      await route.fulfill({
        status: 503,
        json: {
          error: {
            code: 'unavailable',
            message:
              'Caddy application failed; partial external cleanup evidence retained.',
          },
        },
      });
      return;
    }
    const body =
      path === '/auth/status'
        ? auth
        : path === '/settings'
          ? settings
          : path === '/operations'
            ? [operation]
            : path === '/jobs'
              ? []
              : path === '/reconciliation'
                ? {
                    ...reconciliation,
                    inventory: {
                      ...reconciliation.inventory,
                      observedAt: new Date().toISOString(),
                    },
                    operations: [operation],
                  }
                : null;
    await route.fulfill({ json: body, status: body === null ? 404 : 200 });
  });
  await page.goto('/');
  await page.getByRole('button', { name: 'Activity', exact: true }).click();
  await expect(
    page.getByText('External resource cleanup', { exact: false }),
  ).toBeVisible();
  await expect(
    page.getByRole('button', { name: 'Review service recovery' }),
  ).toHaveCount(0);
  await page
    .getByRole('checkbox', {
      name: /I verified these selected UUIDs are unused/,
    })
    .check();
  await page
    .getByRole('button', { name: 'Review fresh external cleanup recovery' })
    .click();
  await page
    .getByRole('checkbox', {
      name: /I confirm these exact UUIDs remain unused/,
    })
    .check();
  await page
    .getByRole('button', { name: 'Delete listed external resources' })
    .click();
  await expect(page.getByRole('alert')).toContainText(
    'partial external cleanup evidence retained',
  );
  expect(applies).toBe(1);
  await page.getByRole('button', { name: 'Close plan' }).click();
  addressReused = true;
  await page
    .getByRole('checkbox', {
      name: /I verified these selected UUIDs are unused/,
    })
    .check();
  await page
    .getByRole('button', { name: 'Review fresh external cleanup recovery' })
    .click();
  await expect(page.getByRole('alert')).toContainText(
    'address reuse blocks cleanup',
  );
  expect(applies).toBe(1);
  expect(plans).toBe(2);
  await expect(page.getByText(/Last recorded step/)).toContainText(
    'apply:caddy',
  );
});
