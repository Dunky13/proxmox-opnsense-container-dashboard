import { expect, test, type Page } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import {
  authSchema,
  jobSchema,
  jobsSchema,
  reconciliationSchema,
} from '../../src/schemas';

const evidenceDir = process.env.DASHBOARD_EVIDENCE_DIR || '../.local/evidence';

async function assertDemo(page: Page) {
  const response = await page.request.get('/api/v1/auth/status');
  const status = authSchema.parse(await response.json());
  expect(
    status.demo,
    'This suite only runs against the isolated demo provider',
  ).toBe(true);
}
async function nav(page: Page, name: string) {
  const mobile = page.getByRole('button', { name: 'Open navigation' });
  if (await mobile.isVisible()) await mobile.click();
  await page
    .getByRole('navigation', { name: 'Main navigation' })
    .filter({ visible: true })
    .getByRole('button', { name, exact: false })
    .click();
}
test.describe('real isolated demo API', () => {
  test.skip(
    process.env.DASHBOARD_DEMO_TEST !== '1',
    'Set DASHBOARD_DEMO_TEST=1 with an isolated demo backend.',
  );
  test('two configured hosts, allocation holes, both view modes and composed filtering across sizes/themes', async ({
    page,
  }) => {
    await assertDemo(page);
    const response = await page.request.get('/api/v1/reconciliation');
    const data = reconciliationSchema.parse(await response.json());
    expect(data.inventory.containers).toHaveLength(8);
    expect(data.inventory.hosts.map((h) => h.nextVmid)).toEqual([605, 703]);
    for (const viewport of [
      { name: 'desktop', width: 1600, height: 1000 },
      { name: 'laptop', width: 1024, height: 850 },
      { name: 'mobile', width: 390, height: 844 },
    ]) {
      for (const colorScheme of ['light', 'dark'] satisfies (
        'light' | 'dark'
      )[]) {
        await page.setViewportSize(viewport);
        await page.emulateMedia({ colorScheme });
        await page.goto('/');
        await expect(
          page.getByRole('heading', { name: 'Containers', exact: true }),
        ).toBeVisible();
        await expect(
          page.getByLabel('Smallest available VMID per host'),
        ).toContainText('605');
        await expect(
          page.getByLabel('Smallest available VMID per host'),
        ).toContainText('703');
        await page.getByRole('button', { name: 'Cards', exact: true }).click();
        await expect(page.locator('.container-card')).toHaveCount(8);
        await page.screenshot({
          path: `${evidenceDir}/demo-${viewport.name}-${colorScheme}-cards.png`,
          fullPage: true,
        });
        await page
          .getByRole('button', { name: 'Configure immich, VMID 604' })
          .click();
        await page.getByRole('button', { name: 'Rows', exact: true }).click();
        await expect(page.locator('.container-row')).toHaveCount(8);
        await page.screenshot({
          path: `${evidenceDir}/demo-${viewport.name}-${colorScheme}-rows-inspector.png`,
          fullPage: true,
        });
        expect(
          await page.evaluate(
            () =>
              document.documentElement.scrollWidth >
              document.documentElement.clientWidth,
          ),
        ).toBe(false);
        const audit = await new AxeBuilder({ page })
          .withTags(['wcag2a', 'wcag2aa', 'wcag21aa'])
          .analyze();
        expect(audit.violations.map((violation) => violation.id)).toEqual([]);
        await page
          .getByLabel('Search all container values')
          .fill('192.168.7 OPNsense debian');
        await page.getByLabel('Host', { exact: true }).selectOption('pvedell');
        await page.getByLabel('State', { exact: true }).selectOption('running');
        await page
          .getByLabel('Distribution', { exact: true })
          .selectOption('debian');
        await page
          .getByLabel('Exposure', { exact: true })
          .selectOption('Not configured');
        await expect(page.locator('.container-row')).toHaveCount(2);
        await page.getByRole('button', { name: 'Cards', exact: true }).click();
        await expect(page.locator('.container-card')).toHaveCount(2);
        await expect(
          page.getByRole('complementary', { name: 'Details for immich' }),
        ).toBeVisible();
        await page.screenshot({
          path: `${evidenceDir}/demo-${viewport.name}-${colorScheme}-filtered-cards.png`,
          fullPage: true,
        });
      }
    }
  });
  test('private plan/apply, persisted upstream override, public transition and mock update job', async ({
    page,
  }) => {
    await assertDemo(page);
    await page.goto('/');
    await page
      .getByRole('button', { name: 'Configure immich, VMID 604' })
      .click();
    await page.getByLabel('Upstream port', { exact: true }).fill('2283');
    await page.getByLabel('Access policy').selectOption('private');
    await page
      .getByRole('button', { name: 'Review network change', exact: true })
      .click();
    await expect(
      page.getByRole('heading', { name: 'Review network change' }),
    ).toBeVisible();
    await page.screenshot({
      path: `${evidenceDir}/demo-reviewed-private-plan.png`,
      fullPage: true,
    });
    await page.getByRole('button', { name: 'Apply reviewed change' }).click();
    await expect(
      page.getByRole('heading', { name: 'Review network change' }),
    ).not.toBeVisible();
    let response = await page.request.get('/api/v1/reconciliation');
    let data = reconciliationSchema.parse(await response.json());
    expect(
      data.inventory.containers.find((c) => c.key === 'main:604')?.desiredRoute,
    ).toEqual({ scheme: 'http', port: 2283, public: false });
    expect(data.items.find((item) => item.key === 'main:604')?.state).toBe(
      'healthy',
    );
    await page.getByLabel('Access policy').selectOption('public');
    await page
      .getByRole('button', { name: 'Review network change', exact: true })
      .click();
    await expect(
      page.getByRole('button', { name: 'Apply reviewed change' }),
    ).toBeDisabled();
    await page.getByLabel(/I reviewed the public exposure/).check();
    await page.getByRole('button', { name: 'Apply reviewed change' }).click();
    await expect(
      page.getByRole('heading', { name: 'Review network change' }),
    ).not.toBeVisible();
    response = await page.request.get('/api/v1/reconciliation');
    data = reconciliationSchema.parse(await response.json());
    expect(
      data.inventory.containers.find((c) => c.key === 'main:604')?.desiredRoute
        ?.public,
    ).toBe(true);
    await page.getByRole('button', { name: 'App update', exact: true }).click();
    const createdResponse = page.waitForResponse(
      (response) =>
        new URL(response.url()).pathname === '/api/v1/jobs' &&
        response.request().method() === 'POST',
    );
    await page
      .getByRole('button', { name: 'Request update', exact: true })
      .click();
    const createdJob = jobSchema.parse(await (await createdResponse).json());
    await nav(page, 'Activity');
    await expect(
      page.getByRole('heading', { name: /App update main:604/ }).first(),
    ).toBeVisible();
    await expect(page.getByText(createdJob.id, { exact: true })).toBeVisible();
    await expect
      .poll(async () => {
        const response = await page.request.get('/api/v1/jobs');
        const jobs = jobsSchema.parse(await response.json());
        return jobs.find((job) => job.id === createdJob.id)?.status;
      })
      .toBe('succeeded');
    await page.getByRole('button', { name: 'Refresh history' }).click();
    await page.getByText('Execution phases', { exact: true }).first().click();
    await page.screenshot({
      path: `${evidenceDir}/demo-successful-operations-job.png`,
      fullPage: true,
    });
  });
});
