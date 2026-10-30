import { expect, test } from '@playwright/test';
import { auth, container, reconciliation, settings } from '../fixtures';

const evidenceDir = process.env.DASHBOARD_EVIDENCE_DIR || '../.local/evidence';
const legacy = [
  {
    vmid: 200,
    name: 'dbug-git',
    ip: '192.168.6.120',
    mac: 'd0:cc:37:00:02:20',
  },
  {
    vmid: 201,
    name: 'forgejo-docker',
    ip: '192.168.6.121',
    mac: 'd0:cc:37:00:02:21',
  },
];
const config = {
  ...settings,
  hosts: settings.hosts.map((h) => ({ ...h, vmidMin: 603 })),
};
const data = {
  ...reconciliation,
  inventory: {
    ...reconciliation.inventory,
    containers: [
      ...legacy.map((ct) => ({
        ...container,
        ...ct,
        key: `main:${ct.vmid}`,
        identity: '',
        expectedIp: undefined,
        expectedMac: undefined,
        warnings: [
          'creation identity unavailable; add a unique container-dashboard-generation UUID marker before mutations',
        ],
      })),
      container,
    ],
  },
  items: [
    ...legacy.map((ct) => ({
      key: `main:${ct.vmid}`,
      state: 'conflict',
      owned: false,
      issues: ['container does not match host profile'],
    })),
    {
      key: container.key,
      state: 'conflict',
      owned: false,
      issues: ['unowned reservation or domain conflicts'],
    },
  ],
};

for (const viewport of [
  { name: 'desktop', width: 1440, height: 1000 },
  { name: 'mobile', width: 390, height: 844 },
])
  for (const colorScheme of ['light', 'dark'] satisfies ('light' | 'dark')[])
    for (const mode of ['Cards', 'Rows']) {
      test(`${viewport.name} ${colorScheme} ${mode}: explain legacy conflicts and manual verification without authorizing mutations`, async ({
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
        let writes = 0;
        await page.route('**/api/v1/**', async (route) => {
          if (route.request().method() !== 'GET') writes++;
          const path = new URL(route.request().url()).pathname;
          await route.fulfill({
            json: path.endsWith('/auth/status')
              ? auth
              : path.endsWith('/settings')
                ? config
                : path.endsWith('/reconciliation')
                  ? data
                  : path.endsWith('/profiles')
                    ? config.hosts
                    : [],
          });
        });
        async function capture(state: string) {
          const path = `${evidenceDir}/conflicts-${viewport.name}-${colorScheme}-${mode.toLowerCase()}-${state}.png`;
          await page.screenshot({ path, fullPage: true });
          await testInfo.attach(
            `${viewport.name} ${colorScheme} ${mode} ${state}`,
            { path, contentType: 'image/png' },
          );
        }
        try {
          await page.clock.install();
          await page.goto('/');
          await page.getByRole('button', { name: mode, exact: true }).click();
          await page
            .getByRole('button', { name: 'Configure dbug-git, VMID 200' })
            .waitFor();
          await page
            .getByRole('button', { name: 'Refresh', exact: true })
            .waitFor();
          await page.clock.fastForward(1200);
          // Before screenshots also work on the base UI, before repaired assertions.
          await capture('overview');
          await page
            .getByRole('button', { name: 'Configure dbug-git, VMID 200' })
            .click();
          await capture('inspector');
          const inspector = page.getByRole('complementary', {
            name: 'Details for dbug-git',
          });
          for (const ct of legacy) {
            const record = page.locator('.container-record').filter({
              has: page.getByRole('button', {
                name: `Inspect ${ct.name}, VMID ${ct.vmid}`,
                exact: true,
              }),
            });
            await expect(
              record.getByText('container does not match host profile', {
                exact: true,
              }),
            ).toBeVisible();
            await expect(
              record.getByText(/creation identity unavailable/),
            ).toBeVisible();
            await record
              .getByText('Why blocked? Address rules', { exact: true })
              .click();
            const explanation = record.locator('details').filter({
              has: page.getByText('Why blocked? Address rules', {
                exact: true,
              }),
            });
            await expect(explanation).toContainText(
              `VMID ${ct.vmid} is outside every configured address-profile range.`,
            );
            await expect(explanation).toContainText(ct.ip);
            await expect(explanation).toContainText(ct.mac);
            await expect(explanation).toContainText('603 to 699');
            await expect(explanation).toContainText('192.168.6.0/24');
            await expect(explanation).toContainText('d0:cc:37:00:06');
          }
          const foreign = page.locator('.container-record').filter({
            has: page.getByRole('button', {
              name: 'Inspect immich, VMID 604',
              exact: true,
            }),
          });
          await expect(
            foreign.getByText('unowned reservation or domain conflicts', {
              exact: true,
            }),
          ).toBeVisible();
          await expect(
            foreign.getByText('Why blocked? Address rules', { exact: true }),
          ).toHaveCount(0);
          await capture('address-explanation');
          await inspector
            .getByText('Verification help', { exact: true })
            .click();
          await expect(inspector.getByLabel('Linux UUID command')).toHaveValue(
            'cat /proc/sys/kernel/random/uuid',
          );
          await expect(inspector).toContainText('Summary → Notes → Edit');
          await expect(inspector).toContainText(
            'Preserve existing notes and append a new line',
          );
          await expect(inspector).toContainText(
            'container-dashboard-generation=<UUID>',
          );
          await expect(inspector).toContainText('Never reuse markers');
          await expect(inspector).toContainText(
            'does not fix an address-rule mismatch',
          );
          await expect(
            inspector.getByRole('button', { name: 'App update', exact: true }),
          ).toBeDisabled();
          await expect(
            inspector.getByRole('button', {
              name: 'Review network change',
              exact: true,
            }),
          ).toBeDisabled();
          await expect(
            inspector.getByText('Expected IP address', { exact: true }),
          ).toHaveCount(0);
          await capture('verification-help');
          expect(
            await page.evaluate(
              () =>
                document.documentElement.scrollWidth >
                document.documentElement.clientWidth,
            ),
          ).toBe(false);
          expect(writes).toBe(0);
        } finally {
          await context.close();
        }
      });
    }
