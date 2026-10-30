import { expect, test, type Page } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import {
  auth,
  job,
  partial,
  plan,
  reconciliation,
  settings,
} from '../fixtures';

const evidenceDir = process.env.DASHBOARD_EVIDENCE_DIR || '../.local/evidence';

async function fixtures(page: Page) {
  await page.route('**/api/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname.replace('/api/v1', '');
    const body =
      path === '/auth/status'
        ? auth
        : path === '/settings'
          ? settings
          : path === '/profiles'
            ? settings.hosts
            : path === '/reconciliation'
              ? reconciliation
              : path === '/jobs' || path === '/operations'
                ? []
                : undefined;
    if (body === undefined) {
      await route.fulfill({
        status: 404,
        json: {
          error: { code: 'notFound', message: 'Fixture route unavailable' },
        },
      });
      return;
    }
    await route.fulfill({ json: body });
  });
}
async function navigate(page: Page, name: string) {
  const mobile = page.getByRole('button', { name: 'Open navigation' });
  if (await mobile.isVisible()) await mobile.click();
  await page
    .getByRole('navigation', { name: 'Main navigation' })
    .filter({ visible: true })
    .getByRole('button', { name, exact: false })
    .click();
}
for (const viewport of [
  { name: 'desktop', width: 1600, height: 1000 },
  { name: 'mobile', width: 390, height: 844 },
]) {
  test(`${viewport.name}: failed inventory retains observations and withdraws allocation`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await fixtures(page);
    let phase = 'healthy';
    await page.route('**/api/v1/reconciliation', (route) => {
      if (phase === 'error')
        return route.fulfill({
          status: 502,
          json: {
            error: {
              code: 'providerUnavailable',
              message: 'Inventory read failed',
            },
          },
        });
      if (phase === 'healthy') return route.fulfill({ json: reconciliation });
      return route.fulfill({
        json: {
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
        },
      });
    });
    await page.goto('/');
    await page
      .getByRole('button', { name: 'Configure immich, VMID 604' })
      .click();
    await page.evaluate(() => {
      if (document.activeElement instanceof HTMLElement)
        document.activeElement.blur();
      window.scrollTo(0, 0);
    });
    await page.screenshot({
      path: `${evidenceDir}/inventory-${viewport.name}-before-failure.png`,
      fullPage: true,
    });
    phase = 'partial';
    await page.getByRole('button', { name: 'Refresh', exact: true }).click();
    await expect(page.getByText('Host read unavailable')).toBeVisible();
    await expect(page.getByText('605', { exact: true })).toHaveCount(0);
    await expect(page.getByText('703', { exact: true })).toBeVisible();
    const inspector = page.getByRole('complementary', {
      name: 'Details for immich',
    });
    await expect(inspector).toBeVisible();
    await expect(
      inspector.getByText(/Host inventory is stale. Last observed values:/),
    ).toBeVisible();
    await expect(
      inspector.getByRole('button', { name: 'App update', exact: true }),
    ).toBeDisabled();
    await expect(
      inspector.getByRole('button', {
        name: 'Review network change',
        exact: true,
      }),
    ).toBeDisabled();
    await page.evaluate(() => {
      if (document.activeElement instanceof HTMLElement)
        document.activeElement.blur();
      window.scrollTo(0, 0);
    });
    await page.screenshot({
      path: `${evidenceDir}/inventory-${viewport.name}-after-failure.png`,
      fullPage: true,
    });
    expect(
      (
        await new AxeBuilder({ page })
          .withTags(['wcag2a', 'wcag2aa', 'wcag21aa'])
          .analyze()
      ).violations.map((v) => v.id),
    ).toEqual([]);
    phase = 'empty';
    await page.getByRole('button', { name: 'Refresh', exact: true }).click();
    await expect(page.getByText('601', { exact: true })).toBeVisible();
    await expect(inspector).toHaveCount(0);
    phase = 'error';
    await page.getByRole('button', { name: 'Refresh', exact: true }).click();
    await expect(page.getByText('Inventory read failed')).toBeVisible();
    await expect(page.getByText('601', { exact: true })).toHaveCount(0);
    await expect(page.getByText('703', { exact: true })).toHaveCount(0);
    await expect(
      page.getByRole('button', { name: 'Configure jellyfin, VMID 701' }),
    ).toBeVisible();
  });
}
for (const viewport of [
  { name: 'desktop', width: 1600, height: 1000 },
  { name: 'laptop', width: 1024, height: 850 },
  { name: 'mobile', width: 390, height: 844 },
]) {
  for (const colorScheme of ['light', 'dark'] satisfies ('light' | 'dark')[]) {
    test(`${viewport.name} ${colorScheme}: inventory, selection, settings and accessible layouts`, async ({
      page,
    }) => {
      await page.setViewportSize(viewport);
      await page.emulateMedia({ colorScheme });
      const errors: string[] = [];
      page.on('pageerror', (error) => errors.push(error.message));
      await fixtures(page);
      await page.goto('/');
      await expect(
        page.getByRole('heading', { name: 'Containers', exact: true }),
      ).toBeVisible();
      await expect(page.locator('html')).toHaveAttribute(
        'data-theme',
        colorScheme,
      );
      await page.screenshot({
        path: `${evidenceDir}/fixture-${viewport.name}-${colorScheme}-cards.png`,
        fullPage: true,
      });
      await page.getByRole('button', { name: 'Rows', exact: true }).click();
      await page
        .getByRole('button', { name: 'Configure immich, VMID 604' })
        .click();
      await expect(
        page.getByRole('complementary', { name: 'Details for immich' }),
      ).toBeVisible();
      await page.screenshot({
        path: `${evidenceDir}/fixture-${viewport.name}-${colorScheme}-rows-inspector.png`,
        fullPage: true,
      });
      const overflow = await page.evaluate(
        () =>
          document.documentElement.scrollWidth >
          document.documentElement.clientWidth,
      );
      expect(overflow).toBe(false);
      const audit = await new AxeBuilder({ page })
        .withTags(['wcag2a', 'wcag2aa', 'wcag21aa'])
        .analyze();
      expect(
        audit.violations.map(
          (violation) => `${violation.id}: ${violation.description}`,
        ),
      ).toEqual([]);
      await navigate(page, 'Settings');
      await expect(
        page.getByRole('heading', { name: 'Settings', exact: true }),
      ).toBeVisible();
      await page
        .getByLabel('Color theme')
        .selectOption(colorScheme === 'dark' ? 'light' : 'dark');
      await expect(page.locator('html')).toHaveAttribute(
        'data-theme',
        colorScheme === 'dark' ? 'light' : 'dark',
      );
      await page.getByLabel('Color theme').selectOption('system');
      await expect(page.locator('html')).toHaveAttribute(
        'data-theme',
        colorScheme,
      );
      await page.screenshot({
        path: `${evidenceDir}/fixture-${viewport.name}-${colorScheme}-settings.png`,
        fullPage: true,
      });
      expect(
        await page.evaluate(
          () =>
            document.documentElement.scrollWidth >
            document.documentElement.clientWidth,
        ),
      ).toBe(false);
      expect(errors).toEqual([]);
    });
  }
}
test('keyboard navigation drawer, focus return and retained settings draft', async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await fixtures(page);
  await page.goto('/');
  const trigger = page.getByRole('button', { name: 'Open navigation' });
  await trigger.focus();
  await page.keyboard.press('Enter');
  await expect(page.getByRole('dialog')).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(page.getByRole('dialog')).not.toBeVisible();
  await expect(trigger).toBeFocused();
  await navigate(page, 'Settings');
  await page.getByText('main proxmox', { exact: true }).last().click();
  const name = page
    .getByLabel('Display name', { exact: true })
    .filter({ visible: true })
    .last();
  await name.fill('Renamed draft');
  await navigate(page, 'Containers');
  await navigate(page, 'Settings');
  await expect(
    page
      .getByLabel('Display name', { exact: true })
      .filter({ visible: true })
      .last(),
  ).toHaveValue('Renamed draft');
});
test('partial operations and uncertain jobs expose supported recovery without invented output', async ({
  page,
}) => {
  await fixtures(page);
  await page.route('**/api/v1/operations', (route) =>
    route.fulfill({
      json: [
        partial,
        {
          ...partial,
          id: 'op-manual',
          key: 'pvedell:701',
          manualRecovery: true,
          status: 'interrupted',
        },
      ],
    }),
  );
  await page.route('**/api/v1/jobs', (route) =>
    route.fulfill({
      json: [
        {
          ...job,
          status: 'interrupted',
          logs: ['Execution interrupted. Remote outcome unverified.'],
        },
      ],
    }),
  );
  await page.route('**/api/v1/jobs/job-1/logs', (route) =>
    route.fulfill({
      json: {
        jobId: job.id,
        logs: ['Execution interrupted. Remote outcome unverified.'],
        truncated: true,
      },
    }),
  );
  await page.goto('/');
  await navigate(page, 'Activity');
  await expect(
    page.getByText('Manual recovery required', { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole('button', { name: 'Acknowledge inspected outcome' }),
  ).toBeDisabled();
  await page
    .getByLabel('I checked the container and understand the outcome')
    .check();
  await expect(
    page.getByRole('button', { name: 'Acknowledge inspected outcome' }),
  ).toBeEnabled();
  await page.getByText('Execution phases', { exact: true }).click();
  await expect(
    page.getByText('Execution interrupted. Remote outcome unverified.', {
      exact: true,
    }),
  ).toBeVisible();
  await page.screenshot({
    path: `${evidenceDir}/fixture-activity-recovery.png`,
    fullPage: true,
  });
});
test('public plan needs explicit acknowledgement and lost apply response refreshes partial effects', async ({
  page,
}) => {
  let applied = false;
  let reconciliationReads = 0;
  let operationReads = 0;
  await fixtures(page);
  await page.route('**/api/v1/reconciliation', (route) => {
    reconciliationReads++;
    return route.fulfill({
      json: { ...reconciliation, operations: applied ? [partial] : [] },
    });
  });
  await page.route('**/api/v1/operations', (route) => {
    operationReads++;
    return route.fulfill({ json: applied ? [partial] : [] });
  });
  await page.route('**/api/v1/plans', (route) =>
    route.fulfill({
      json: {
        ...plan,
        public: true,
        desiredRoute: { ...plan.desiredRoute, public: true },
      },
      status: 201,
    }),
  );
  await page.route('**/api/v1/plans/plan-1/apply', (route) => {
    applied = true;
    return route.fulfill({
      status: 502,
      json: {
        error: {
          code: 'providerUnavailable',
          message: 'Caddy reconfigure failed.',
        },
      },
    });
  });
  await page.goto('/');
  await page
    .getByRole('button', { name: 'Configure immich, VMID 604' })
    .click();
  await page.getByLabel('Access policy').selectOption('public');
  await page
    .getByRole('button', { name: 'Review network change', exact: true })
    .click();
  const apply = page.getByRole('button', { name: 'Apply reviewed change' });
  await expect(apply).toBeDisabled();
  await page.getByLabel(/I reviewed the public exposure/).check();
  await expect(apply).toBeEnabled();
  const initialReconciliationReads = reconciliationReads;
  const initialOperationReads = operationReads;
  await apply.click();
  await expect(page.getByText(/Apply was attempted/)).toBeVisible();
  await expect(apply).toBeDisabled();
  expect(reconciliationReads).toBe(initialReconciliationReads + 1);
  expect(operationReads).toBe(initialOperationReads + 1);
  await navigate(page, 'Activity');
  await expect(page.getByText('Partial', { exact: true })).toBeVisible();
});
