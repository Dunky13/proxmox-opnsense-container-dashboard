import { expect, test, type Page } from '@playwright/test';
import {
  auth,
  container,
  job,
  partial,
  plan,
  reconciliation,
  settings,
} from '../fixtures';

// Standalone: this file can run against the base UI using only its existing
// fixture module. Capture the reported state before any repaired assertion.
const evidenceDir = process.env.DASHBOARD_EVIDENCE_DIR || '../.local/evidence';
const config = {
  ...settings,
  opnsenses: settings.opnsenses.map((firewall) => ({
    ...firewall,
    domainBase: 'went.io',
  })),
};
const observedRoute = { scheme: 'http', port: 8080, public: true };
const resources = [
  {
    kind: 'reservation',
    id: 'a1111111-1111-4111-8111-111111111609',
    name: 'speedtest-tracker',
    summary:
      'Exact IP and MAC match. IP 192.168.6.9; MAC d0:cc:37:00:06:09; subnet a2222222-2222-4222-8222-222222222006 (192.168.6.0/24). Stale hostname speedtest-tracker is preserved; expected hikyo.',
  },
  {
    kind: 'subdomain',
    id: 'a4444444-4444-4444-8444-444444444609',
    name: 'hikyo.went.io',
    summary:
      'Enabled native child; reverse parent a3333333-3333-4333-8333-333333333000. Public access; no child ACL.',
  },
  {
    kind: 'handle',
    id: 'a5555555-5555-4555-8555-555555555609',
    name: 'hikyo upstream',
    summary:
      'Enabled reverse_proxy; child a4444444-4444-4444-8444-444444444609; http://192.168.6.9:8080. HttpTls 0; HttpTlsInsecureSkipVerify 0.',
  },
  {
    kind: 'reverse',
    id: 'a3333333-3333-4333-8333-333333333000',
    name: '*.went.io',
    summary:
      'Shared enabled wildcard parent, externally owned. No parent ACL. Existing automatic HTTPS and DNS challenge retained.',
  },
];

async function install(page: Page) {
  const planBodies: string[] = [];
  const control = {
    phase: 'existing',
    imported: false,
    lostResponse: false,
    historyFailed: false,
    oldGeneration: false,
    planPosts: 0,
    applyPosts: 0,
    unexpectedWrites: 0,
    planBodies,
  };
  // Keep no pending runtime helpers: every request is fulfilled or aborted.
  await page.route('**/api/v1/**', async (requestRoute) => {
    const request = requestRoute.request();
    const path = new URL(request.url()).pathname.replace('/api/v1', '');
    if (request.method() !== 'GET') {
      if (path === '/plans') {
        control.planPosts++;
        control.planBodies.push(request.postData() || '');
        await requestRoute.fulfill({
          json: {
            ...plan,
            id: `import-609-${control.planPosts}`,
            action: 'import',
            key: 'main:609',
            hostId: 'main',
            vmid: 609,
            identity: control.oldGeneration
              ? 'previous-generation'
              : 'generation-609',
            public: true,
            desiredRoute: observedRoute,
            expiresAt: new Date(Date.now() + 300_000).toISOString(),
            effects: resources.map((resource) => ({
              kind: resource.kind,
              action: resource.kind === 'reverse' ? 'retain' : 'adopt',
              resourceId: resource.id,
              name: resource.name,
              changes: [
                {
                  field: 'current configuration',
                  before: resource.summary,
                  after: resource.summary,
                },
              ],
            })),
            applyNotes: [
              'Metadata-only import: no provider create, set, delete or service apply.',
              'Preserve stale DHCP hostname speedtest-tracker. A later reviewed network plan may align it to hikyo.',
              'Wildcard parent and shared LocalIPandVPN/client_ip ACL remain externally owned and read-only. Existing public access and HTTP port 8080 remain unchanged.',
            ],
          },
        });
        return;
      }
      if (path === `/plans/import-609-${control.planPosts}/apply`) {
        control.applyPosts++;
        expect(request.postData()).toBe('{}');
        control.imported = true;
        if (control.lostResponse) await requestRoute.abort('connectionreset');
        else
          await requestRoute.fulfill({
            json: {
              ...partial,
              id: 'import-operation',
              planId: `import-609-${control.planPosts}`,
              key: 'main:609',
              action: 'import',
              status: 'succeeded',
              error: '',
              step: 'ownership recorded',
              desiredRoute: observedRoute,
              resources: resources
                .filter((resource) => resource.kind !== 'reverse')
                .map((resource) => ({
                  kind: resource.kind,
                  id: resource.id,
                  fingerprint: 'observed-fingerprint',
                })),
            },
          });
        return;
      }
      control.unexpectedWrites++;
      await requestRoute.fulfill({
        status: 500,
        json: {
          error: {
            code: 'unexpectedWrite',
            message:
              'No provider or unrelated writes are permitted by this fixture.',
          },
        },
      });
      return;
    }
    if ((path === '/jobs' || path === '/operations') && control.historyFailed) {
      await requestRoute.fulfill({
        status: 503,
        json: {
          error: {
            code: 'historyUnavailable',
            message: 'Activity history unavailable',
          },
        },
      });
      return;
    }
    if (path === '/reconciliation') {
      const issues =
        control.phase === 'ambiguous'
          ? [
              'Multiple related handles found for child a4444444-4444-4444-8444-444444444609. Import requires one unambiguous handle.',
            ]
          : control.phase === 'wrong-target'
            ? [
                'Handle a5555555-5555-4555-8555-555555555609 targets 192.168.3.240:443, not this container at 192.168.6.9.',
              ]
            : [];
      await requestRoute.fulfill({
        json: {
          ...reconciliation,
          inventory: {
            ...reconciliation.inventory,
            observedAt: new Date(
              Date.now() - (control.phase === 'stale' ? 360_000 : 0),
            ).toISOString(),
            complete: control.phase !== 'incomplete',
            hosts: reconciliation.inventory.hosts.map((host) => ({
              ...host,
              complete: control.phase !== 'incomplete',
              error: control.phase === 'incomplete' ? 'Host read failed' : '',
            })),
            containers: [
              {
                ...container,
                key: 'main:609',
                vmid: 609,
                name: 'hikyo',
                ip: '192.168.6.9',
                expectedIp:
                  control.phase === 'profile' ? '192.168.6.10' : '192.168.6.9',
                mac: 'd0:cc:37:00:06:09',
                expectedMac: 'd0:cc:37:00:06:09',
                identity: control.phase === 'identity' ? '' : 'generation-609',
                desiredRoute: control.imported ? observedRoute : undefined,
              },
            ],
          },
          items: [
            {
              key: 'main:609',
              state: control.imported ? 'mismatch' : 'conflict',
              owned: control.imported,
              issues: control.imported
                ? [
                    'DHCP hostname speedtest-tracker differs from hikyo. Review a network change to align it.',
                  ]
                : ['unowned reservation or domain conflicts'],
              desiredRoute: control.imported ? observedRoute : undefined,
              existingRoute: observedRoute,
              existingResources: resources,
              importAvailable: !control.imported && issues.length === 0,
              importIssues: issues,
            },
          ],
          // Independent endpoint observations must be unioned with this read.
          operations:
            control.phase === 'inventory-operation'
              ? [{ ...partial, key: 'main:609' }]
              : [],
        },
      });
      return;
    }
    await requestRoute.fulfill({
      json:
        path === '/auth/status'
          ? auth
          : path === '/settings'
            ? config
            : path === '/profiles'
              ? config.hosts
              : path === '/jobs' && control.phase === 'job'
                ? [{ ...job, key: 'main:609' }]
                : path === '/operations' && control.phase === 'operation'
                  ? [{ ...partial, key: 'main:609' }]
                  : path === '/operations' && control.imported
                    ? [
                        {
                          ...partial,
                          planId: `import-609-${control.planPosts}`,
                          key: 'main:609',
                          action: 'import',
                          status: 'succeeded',
                          error: '',
                        },
                      ]
                    : [],
    });
  });
  return control;
}

for (const viewport of [
  { name: 'desktop', width: 1440, height: 1000 },
  { name: 'mobile', width: 390, height: 844 },
])
  for (const colorScheme of ['light', 'dark'] satisfies ('light' | 'dark')[])
    for (const mode of ['Cards', 'Rows']) {
      test(`${viewport.name} ${colorScheme} ${mode}: review exact Hikyo resources and preserve existing HTTP 8080 public access`, async ({
        browser,
        baseURL,
      }, testInfo) => {
        const context = await browser.newContext({
          baseURL,
          viewport,
          colorScheme,
          deviceScaleFactor: 2,
        });
        const page = await context.newPage();
        const control = await install(page);
        async function capture(state: string) {
          const path = `${evidenceDir}/import-${viewport.name}-${colorScheme}-${mode.toLowerCase()}-${state}.png`;
          await page.screenshot({ path, fullPage: true });
          await testInfo.attach(
            `${viewport.name} ${colorScheme} ${mode} ${state}`,
            { path, contentType: 'image/png' },
          );
        }
        try {
          await page.goto('/');
          await page.getByRole('button', { name: mode, exact: true }).click();
          await page
            .getByRole('button', { name: 'Refresh', exact: true })
            .waitFor();
          await page
            .getByRole('button', { name: 'Configure hikyo, VMID 609' })
            .waitFor();
          await capture('overview');
          await page
            .getByRole('button', { name: 'Configure hikyo, VMID 609' })
            .click();
          const inspector = page.getByRole('complementary', {
            name: 'Details for hikyo',
          });
          await inspector.waitFor();
          // This screenshot succeeds against the base UI before the first
          // assertion requiring the repaired behavior. Preserve its failing run.
          await capture('inspector');
          await expect(inspector.getByLabel('Upstream port')).toHaveValue(
            '8080',
          );
          await expect(inspector.getByLabel('Access policy')).toHaveValue(
            'public',
          );
          for (const resource of resources)
            await expect(
              inspector.getByText(resource.id, { exact: true }),
            ).toBeVisible();
          await expect(inspector).toContainText('Exact IP and MAC match');
          await expect(inspector).toContainText('speedtest-tracker');
          await expect(inspector).toContainText('192.168.6.0/24');
          await expect(inspector).toContainText('http://192.168.6.9:8080');
          await expect(inspector).not.toContainText(
            'unowned reservation or domain conflicts',
          );
          await expect(inspector).not.toContainText('hikyo.dbugit.io');
          await expect(
            inspector.getByRole('button', {
              name: 'Review network change',
              exact: true,
            }),
          ).toBeDisabled();
          expect(control.planPosts).toBe(0);
          await inspector
            .getByRole('button', {
              name: 'Review existing configuration',
              exact: true,
            })
            .click();
          const review = inspector.getByRole('region', {
            name: 'Review existing configuration plan',
          });
          await expect(review).toBeVisible();
          await capture('review');
          await expect(review).toContainText('HTTP upstream, port 8080.');
          await expect(review).toContainText('no provider');
          await expect(review).toContainText(
            'Shared wildcard parents and shared access lists remain externally owned',
          );
          await expect(review).toContainText('LocalIPandVPN/client_ip');
          await expect(review.getByText('adopt', { exact: true })).toHaveCount(
            3,
          );
          await expect(review.getByText('retain', { exact: true })).toHaveCount(
            1,
          );
          const apply = review.getByRole('button', {
            name: 'Import reviewed configuration',
          });
          await expect(apply).toBeDisabled();
          await review.getByLabel(/accept managed ownership/).check();
          await expect(apply).toBeDisabled();
          await review
            .getByLabel(
              /I acknowledge this import preserves existing public access/,
            )
            .check();
          await expect(apply).toBeEnabled();
          expect(control.planBodies).toEqual([
            '{"action":"import","hostId":"main","vmid":609}',
          ]);
          expect(
            await page.evaluate(
              () =>
                document.documentElement.scrollWidth >
                document.documentElement.clientWidth,
            ),
          ).toBe(false);
          await apply.click();
          await expect(review).toHaveCount(0);
          await expect(inspector).toContainText(
            'DHCP hostname speedtest-tracker differs from hikyo',
          );
          await expect(inspector.getByLabel('Upstream port')).toHaveValue(
            '8080',
          );
          await expect(inspector.getByLabel('Access policy')).toHaveValue(
            'public',
          );
          await expect(
            inspector.getByRole('button', {
              name: 'Review network change',
              exact: true,
            }),
          ).toBeEnabled();
          await capture('imported-mismatch');
          expect(control.applyPosts).toBe(1);
          expect(control.unexpectedWrites).toBe(0);
        } finally {
          await context.close();
        }
      });
    }

for (const scenario of [
  { phase: 'ambiguous', reason: 'Multiple related handles found' },
  { phase: 'wrong-target', reason: 'targets 192.168.3.240:443' },
  { phase: 'identity', reason: 'Container generation is unverified' },
  { phase: 'profile', reason: 'Container addresses do not match' },
  { phase: 'stale', reason: 'Current complete inventory is required' },
  { phase: 'incomplete', reason: 'Current complete inventory is required' },
  { phase: 'job', reason: 'An update is active' },
  { phase: 'operation', reason: 'An incomplete network operation' },
  { phase: 'inventory-operation', reason: 'An incomplete network operation' },
  { phase: 'history', reason: 'Current activity history is required' },
]) {
  test(`import stays blocked for ${scenario.phase} even when cached import metadata exists`, async ({
    page,
  }, testInfo) => {
    const control = await install(page);
    control.phase = scenario.phase;
    control.historyFailed = scenario.phase === 'history';
    await page.goto('/');
    await page.getByRole('button', { name: 'Refresh', exact: true }).waitFor();
    await page
      .getByRole('button', { name: 'Configure hikyo, VMID 609' })
      .click();
    const inspector = page.getByRole('complementary', {
      name: 'Details for hikyo',
    });
    const path = `${evidenceDir}/import-blocked-${scenario.phase}.png`;
    await page.screenshot({ path, fullPage: true });
    await testInfo.attach(`blocked ${scenario.phase}`, {
      path,
      contentType: 'image/png',
    });
    await expect(inspector).toContainText(scenario.reason);
    await expect(
      inspector.getByRole('button', {
        name: 'Review existing configuration',
        exact: true,
      }),
    ).toBeDisabled();
    expect(control.planPosts).toBe(0);
    expect(control.applyPosts).toBe(0);
    expect(control.unexpectedWrites).toBe(0);
  });
}

test('a lost import response consumes confirmation and observes durable ownership without POST retry', async ({
  page,
}) => {
  const control = await install(page);
  control.lostResponse = true;
  await page.goto('/');
  await page.getByRole('button', { name: 'Configure hikyo, VMID 609' }).click();
  await page
    .getByRole('button', { name: 'Review existing configuration', exact: true })
    .click();
  const review = page.getByRole('region', {
    name: 'Review existing configuration plan',
  });
  const ownership = review.getByLabel(/accept managed ownership/);
  const publicAccess = review.getByLabel(/I acknowledge this import preserves/);
  await ownership.check();
  await publicAccess.check();
  await review
    .getByRole('button', { name: 'Import reviewed configuration' })
    .click();
  await expect(review).toContainText('Apply was attempted');
  await expect(review).toContainText('lost after ownership was recorded');
  await expect(
    review.getByRole('button', { name: 'Import reviewed configuration' }),
  ).toBeDisabled();
  await expect(ownership).not.toBeChecked();
  await expect(publicAccess).not.toBeChecked();
  expect(control.applyPosts).toBe(1);
  await review.getByRole('button', { name: 'Close plan' }).click();
  const inspector = page.getByRole('complementary', {
    name: 'Details for hikyo',
  });
  await expect(
    inspector.getByRole('button', {
      name: 'Review existing configuration',
      exact: true,
    }),
  ).toHaveCount(0);
  await expect(
    inspector.getByRole('button', {
      name: 'Review network change',
      exact: true,
    }),
  ).toBeEnabled();
  expect(control.planPosts).toBe(1);
  expect(control.unexpectedWrites).toBe(0);
});

test('a plan for an older generation never opens review or applies', async ({
  page,
}) => {
  const control = await install(page);
  control.oldGeneration = true;
  await page.goto('/');
  await page.getByRole('button', { name: 'Configure hikyo, VMID 609' }).click();
  await page
    .getByRole('button', { name: 'Review existing configuration', exact: true })
    .click();
  await expect(page.getByRole('alert')).toContainText(
    'Container identity changed while preparing the plan',
  );
  await expect(
    page.getByRole('region', { name: 'Review existing configuration plan' }),
  ).toHaveCount(0);
  expect(control.applyPosts).toBe(0);
});

test('a refresh invalidates the reviewed snapshot and requires fresh acknowledgements on a new plan', async ({
  page,
}) => {
  const control = await install(page);
  await page.goto('/');
  await page.getByRole('button', { name: 'Configure hikyo, VMID 609' }).click();
  await page
    .getByRole('button', { name: 'Review existing configuration', exact: true })
    .click();
  const review = page.getByRole('region', {
    name: 'Review existing configuration plan',
  });
  await review.getByLabel(/accept managed ownership/).check();
  await review.getByLabel(/I acknowledge this import preserves/).check();
  control.historyFailed = true;
  await page.getByRole('button', { name: 'Refresh', exact: true }).click();
  await expect(review).toContainText('Current activity history is required');
  await expect(
    review.getByRole('button', { name: 'Import reviewed configuration' }),
  ).toBeDisabled();
  control.historyFailed = false;
  await page.getByRole('button', { name: 'Refresh', exact: true }).click();
  await expect(review).toContainText('Observations changed after review');
  await expect(
    review.getByRole('button', { name: 'Import reviewed configuration' }),
  ).toBeDisabled();
  await review.getByRole('button', { name: 'Close plan' }).click();
  await page
    .getByRole('button', { name: 'Review existing configuration', exact: true })
    .click();
  await expect(review.getByLabel(/accept managed ownership/)).not.toBeChecked();
  await expect(
    review.getByLabel(/I acknowledge this import preserves/),
  ).not.toBeChecked();
  expect(control.planPosts).toBe(2);
  expect(control.applyPosts).toBe(0);
});
