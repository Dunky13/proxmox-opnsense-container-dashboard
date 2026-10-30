import { expect, test } from '@playwright/test';
import { auth, container, plan, reconciliation, settings } from '../fixtures';

const baseline = process.env.DASHBOARD_MAINTENANCE_BASELINE === '1';
const evidence = process.env.DASHBOARD_EVIDENCE_DIR || '../.local/evidence';
const resourceId = 'a1111111-1111-4111-8111-111111111609';

test('synthetic Hikyo blocked-maintenance flow at matching sizes and system themes', async ({
  page,
}) => {
  const ct = {
    ...container,
    key: 'main:609',
    vmid: 609,
    name: 'hikyo',
    identity: 'synthetic-609',
    fingerprint: 'synthetic-fingerprint-609',
    ip: '192.168.6.9',
    mac: 'd0:cc:37:00:06:09',
    desiredRoute: { scheme: 'http', port: 8080, public: true },
  };
  let sshEnabled = false;
  await page.route('**/api/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname.replace('/api/v1', '');
    const body =
      path === '/auth/status'
        ? auth
        : path === '/settings'
          ? {
              ...settings,
              hosts: settings.hosts.map((h) => ({
                ...h,
                ssh: { ...h.ssh, enabled: sshEnabled },
              })),
            }
          : path === '/reconciliation'
            ? {
                ...reconciliation,
                inventory: {
                  ...reconciliation.inventory,
                  observedAt: new Date().toISOString(),
                  containers: [ct],
                },
                items: [
                  {
                    key: ct.key,
                    state: 'mismatch',
                    owned: true,
                    issues: ['owned resources changed or are missing'],
                    reservationBlocked: 'owned reservation already exists',
                    desiredRoute: ct.desiredRoute,
                    canAcceptChanges: true,
                    drift: [
                      {
                        kind: 'reservation',
                        id: resourceId,
                        status: 'changed',
                        detail:
                          'Only the recorded reservation hostname changed.',
                        changes: [
                          {
                            field: 'hostname',
                            before: 'speedtest-tracker',
                            after: 'hikyo',
                          },
                        ],
                      },
                    ],
                  },
                ],
              }
            : path === '/jobs' || path === '/operations'
              ? []
              : path.endsWith('/maintenance')
                ? {
                    key: ct.key,
                    identity: ct.identity,
                    fingerprint: ct.fingerprint,
                    observedAt: new Date().toISOString(),
                    connected: sshEnabled,
                    distribution: sshEnabled ? 'debian' : '',
                    version: sshEnabled ? '13' : '',
                    source: sshEnabled ? 'ssh' : '',
                    appUpdate: sshEnabled,
                    osUpdate: sshEnabled,
                    reason: sshEnabled
                      ? ''
                      : 'SSH is disabled. Configure dedicated restricted SSH in Settings.',
                    appReason: '',
                    osReason: '',
                  }
                : null;
    await route.fulfill({ json: body, status: body === null ? 404 : 200 });
  });
  for (const size of [
    { name: 'desktop', width: 1600, height: 1000 },
    { name: 'mobile', width: 390, height: 844 },
  ]) {
    for (const colorScheme of ['light', 'dark'] satisfies (
      'light' | 'dark'
    )[]) {
      await page.setViewportSize(size);
      await page.emulateMedia({ colorScheme });
      await page.goto('/');
      await page
        .getByRole('button', { name: 'Configure hikyo, VMID 609' })
        .click();
      const detail = page.getByRole('complementary', {
        name: 'Details for hikyo',
      });
      await expect(
        detail.getByRole('button', { name: 'App update', exact: true }),
      ).toBeDisabled();
      await expect(
        detail.getByRole('button', { name: 'OS update', exact: true }),
      ).toBeDisabled();
      if (!baseline) {
        await expect(
          detail.getByRole('button', { name: 'Add missing reservation' }),
        ).toBeDisabled();
        await expect(
          detail.getByText('owned reservation already exists', { exact: true }),
        ).toBeVisible();
        await expect(detail.getByLabel('Owned resource changes')).toContainText(
          'speedtest-tracker',
        );
        await expect(detail.getByLabel('Owned resource changes')).toContainText(
          'hikyo',
        );
        await expect(
          detail.getByRole('button', { name: 'Open maintenance settings' }),
        ).toBeVisible();
      }
      const toast = page.getByRole('button', {
        name: 'Dismiss loading status',
      });
      if (await toast.isVisible()) await toast.click();
      await page.screenshot({
        path: `${evidence}/${baseline ? 'before' : 'after'}-synthetic-hikyo-${size.name}-${colorScheme}.png`,
        fullPage: true,
      });
      await detail
        .getByRole('heading', { name: 'Maintenance', exact: true })
        .scrollIntoViewIfNeeded();
      await page.screenshot({
        path: `${evidence}/${baseline ? 'before' : 'after'}-maintenance-${size.name}-${colorScheme}.png`,
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
  if (!baseline) {
    const detail = page.getByRole('complementary', {
      name: 'Details for hikyo',
    });
    await detail
      .getByRole('button', { name: 'Check maintenance readiness' })
      .click();
    await expect(
      detail.getByText(
        'SSH is disabled. Configure dedicated restricted SSH in Settings.',
      ),
    ).toBeVisible();
    await detail
      .getByRole('button', { name: 'Open maintenance settings' })
      .click();
    await expect(
      page.getByRole('heading', { name: 'Settings', exact: true }),
    ).toBeVisible();
    sshEnabled = true;
    await page.goto('/');
    await page
      .getByRole('button', { name: 'Configure hikyo, VMID 609' })
      .click();
    await page
      .getByRole('button', { name: 'Check maintenance readiness' })
      .click();
    await expect(page.getByText(/SSH connected/)).toContainText('debian 13');
    await expect(
      page
        .getByRole('complementary', { name: 'Details for hikyo' })
        .getByRole('button', { name: 'App update', exact: true }),
    ).toBeEnabled();
  }
});

test('manual orphan selection reviews only selected UUIDs and never retries a lost response', async ({
  page,
}) => {
  test.skip(baseline, 'Selection is absent in the pinned base.');
  let applies = 0;
  let selectedBody = '';
  await page.route('**/api/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname.replace('/api/v1', '');
    if (path.endsWith('/apply')) {
      applies++;
      await route.abort('failed');
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
                },
                items: [
                  {
                    key: 'main:609',
                    owned: true,
                    state: 'ownedOrphan',
                    issues: ['Container is absent.'],
                    managedResources: [
                      {
                        kind: 'reservation',
                        id: resourceId,
                        name: 'old app',
                        summary: 'Owned reservation',
                      },
                      {
                        kind: 'handle',
                        id: 'a5555555-5555-4555-8555-555555555609',
                        name: 'old proxy',
                        summary: 'Owned proxy',
                      },
                    ],
                  },
                ],
              }
            : path === '/jobs' || path === '/operations'
              ? []
              : path === '/plans'
                ? {
                    ...plan,
                    action: 'cleanup',
                    key: 'main:609',
                    vmid: 609,
                    effects: [
                      {
                        kind: 'reservation',
                        resourceId,
                        action: 'delete',
                        name: 'Delete selected reservation',
                        changes: [],
                      },
                    ],
                  }
                : null;
    if (path === '/plans') selectedBody = route.request().postData() || '';
    await route.fulfill({ json: body, status: body === null ? 404 : 200 });
  });
  await page.goto('/');
  await page
    .getByRole('navigation', { name: 'Main navigation' })
    .filter({ visible: true })
    .getByRole('button', { name: /Mismatches/ })
    .click();
  await expect(
    page.getByRole('button', { name: 'Review owned resource cleanup' }),
  ).toBeDisabled();
  await page
    .getByRole('checkbox', { name: /Remove stale reservation/ })
    .check();
  await page
    .getByRole('button', { name: 'Review owned resource cleanup' })
    .click();
  await expect(
    page.getByRole('heading', { name: 'Review owned resource cleanup' }),
  ).toBeVisible();
  expect(selectedBody).toContain(`"resourceIds":["${resourceId}"]`);
  await page.screenshot({
    path: `${evidence}/after-selected-cleanup-review.png`,
    fullPage: true,
  });
  await page
    .getByRole('button', { name: 'Delete listed owned resources' })
    .click();
  await expect(page.getByText(/response may have been lost/)).toBeVisible();
  await expect(
    page.getByRole('button', { name: 'Delete listed owned resources' }),
  ).toBeDisabled();
  expect(applies).toBe(1);
});
