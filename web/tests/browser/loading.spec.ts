import { expect, test, type Page } from '@playwright/test';
import { auth, reconciliation, settings } from '../fixtures';

// Keep this regression independent of implementation helpers so the same file
// can capture the pending screen and fail against the pre-fix embedded UI.
const evidenceDir = process.env.DASHBOARD_EVIDENCE_DIR || '../.local/evidence';

async function navigate(page: Page, name: string) {
  const menu = page.getByRole('button', { name: 'Open navigation' });
  if (await menu.isVisible()) await menu.click();
  await page
    .getByRole('navigation', { name: 'Main navigation' })
    .filter({ visible: true })
    .getByRole('button', { name, exact: true })
    .click();
}

for (const viewport of [
  { name: 'desktop', width: 1440, height: 1000 },
  { name: 'mobile', width: 390, height: 844 },
]) {
  test(`pending inventory leaves configuration, activity and read recovery usable on ${viewport.name}`, async ({
    browser,
    baseURL,
  }, testInfo) => {
    const context = await browser.newContext({
      baseURL,
      viewport,
      deviceScaleFactor: 2,
      colorScheme: 'dark',
    });
    const page = await context.newPage();
    let release: (() => void) | undefined;
    const held = new Promise<void>((resolve) => {
      release = resolve;
    });
    let hold = true;
    await page.route('**/api/v1/**', async (route) => {
      const path = new URL(route.request().url()).pathname;
      if (path.endsWith('/reconciliation') && hold) await held;
      await route.fulfill({
        json: path.endsWith('/auth/status')
          ? auth
          : path.endsWith('/settings')
            ? settings
            : path.endsWith('/reconciliation')
              ? reconciliation
              : [],
      });
    });
    try {
      const fast = Promise.all(
        ['/settings', '/jobs', '/operations'].map((path) =>
          page.waitForResponse((response) =>
            response.url().endsWith(`/api/v1${path}`),
          ),
        ),
      );
      await page.goto('/');
      await fast;
      // Capture BEFORE the first assertion of repaired behavior, including on base.
      const pending = `${evidenceDir}/loading-${viewport.name}-dark-pending.png`;
      await page.screenshot({ path: pending, fullPage: true });
      await testInfo.attach(`${viewport.name} pending inventory`, {
        path: pending,
        contentType: 'image/png',
      });
      await expect(
        page.getByLabel('Smallest available VMID per host'),
      ).toContainText('main proxmox');
      await expect(page.getByText('605', { exact: true })).not.toBeVisible();
      await navigate(page, 'Settings');
      await expect(
        page.getByRole('heading', { name: 'Settings', exact: true }),
      ).toBeVisible();
      await page.screenshot({
        path: `${evidenceDir}/loading-${viewport.name}-dark-settings-pending.png`,
        fullPage: true,
      });
      await navigate(page, 'Activity');
      await expect(
        page.getByRole('heading', { name: 'Activity', exact: true }),
      ).toBeVisible();
      await expect(
        page.getByText('No operations yet', { exact: true }),
      ).toBeVisible();
      await page
        .getByRole('button', { name: 'Cancel refresh', exact: true })
        .click();
      await expect(
        page.getByRole('button', { name: 'Refresh', exact: true }),
      ).toBeEnabled();
      await navigate(page, 'Containers');
      hold = false;
      release?.();
      await page.getByRole('button', { name: 'Refresh', exact: true }).click();
      await expect(page.getByText('605', { exact: true })).toBeVisible();
      await expect(
        page.getByRole('button', { name: 'Configure immich, VMID 604' }),
      ).toBeVisible();
      const recovered = `${evidenceDir}/loading-${viewport.name}-dark-recovered.png`;
      await page.screenshot({ path: recovered, fullPage: true });
      await testInfo.attach(`${viewport.name} recovered inventory`, {
        path: recovered,
        contentType: 'image/png',
      });
      expect(
        await page.evaluate(
          () =>
            document.documentElement.scrollWidth >
            document.documentElement.clientWidth,
        ),
      ).toBe(false);
    } finally {
      release?.();
      await context.close();
    }
  });
}

test('inventory is usable before hung history while maintenance stays blocked', async ({
  page,
}, testInfo) => {
  let release: (() => void) | undefined;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route('**/api/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith('/jobs')) await held;
    await route.fulfill({
      json: path.endsWith('/auth/status')
        ? auth
        : path.endsWith('/settings')
          ? settings
          : path.endsWith('/reconciliation')
            ? reconciliation
            : [],
    });
  });
  try {
    const fast = Promise.all(
      ['/settings', '/reconciliation', '/operations'].map((path) =>
        page.waitForResponse((response) =>
          response.url().endsWith(`/api/v1${path}`),
        ),
      ),
    );
    await page.goto('/');
    await fast;
    const pending = `${evidenceDir}/loading-history-pending.png`;
    await page.screenshot({ path: pending, fullPage: true });
    await testInfo.attach('inventory before pending history', {
      path: pending,
      contentType: 'image/png',
    });
    await expect(
      page.getByRole('button', { name: 'Configure immich, VMID 604' }),
    ).toBeVisible();
    await expect(page.getByText('605', { exact: true })).toBeVisible();
    const update = page.getByRole('button', {
      name: 'Review app update immich, VMID 604',
    });
    await expect(update).toBeDisabled();
    await navigate(page, 'Activity');
    await expect(
      page.getByText(/Complete activity history is unavailable/),
    ).toBeVisible();
    release?.();
    await expect(
      page.getByText('No operations yet', { exact: true }),
    ).toBeVisible();
    await navigate(page, 'Containers');
    await expect(update).toBeEnabled();
  } finally {
    release?.();
  }
});

test('mobile timeout retains stale observations and exposes an enabled retry', async ({
  browser,
  baseURL,
}, testInfo) => {
  const context = await browser.newContext({
    baseURL,
    viewport: { width: 390, height: 844 },
    deviceScaleFactor: 2,
    colorScheme: 'dark',
  });
  const page = await context.newPage();
  let hold = false;
  let release: (() => void) | undefined;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route('**/api/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith('/reconciliation') && hold) await held;
    await route.fulfill({
      json: path.endsWith('/auth/status')
        ? auth
        : path.endsWith('/settings')
          ? settings
          : path.endsWith('/reconciliation')
            ? reconciliation
            : [],
    });
  });
  try {
    await page.clock.install();
    await page.goto('/');
    await expect(page.getByText('605', { exact: true })).toBeVisible();
    hold = true;
    await page.getByRole('button', { name: 'Refresh', exact: true }).click();
    const pending = `${evidenceDir}/loading-mobile-dark-stale-pending.png`;
    await page.screenshot({ path: pending, fullPage: true });
    await testInfo.attach('mobile previous observations while pending', {
      path: pending,
      contentType: 'image/png',
    });
    await expect(
      page.getByRole('button', { name: 'Cancel refresh', exact: true }),
    ).toBeEnabled();
    await page.getByRole('button', { name: 'Dismiss loading status' }).click();
    await page.clock.fastForward(105_000);
    await expect(page.getByRole('alert')).toContainText(
      'provider inventory: The read timed out',
    );
    await expect(page.getByRole('alert')).toContainText(
      'Treat values as stale',
    );
    await expect(page.getByText('605', { exact: true })).not.toBeVisible();
    await expect(
      page.getByRole('button', { name: 'Review app update immich, VMID 604' }),
    ).toBeDisabled();
    await expect(
      page.getByRole('button', { name: 'Retry refresh', exact: true }),
    ).toBeEnabled();
    const path = `${evidenceDir}/loading-mobile-dark-timeout.png`;
    await page.screenshot({ path, fullPage: true });
    await testInfo.attach('mobile timeout and retry', {
      path,
      contentType: 'image/png',
    });
    hold = false;
    release?.();
    await page
      .getByRole('button', { name: 'Retry refresh', exact: true })
      .click();
    await expect(page.getByText('605', { exact: true })).toBeVisible();
    await expect(page.getByRole('alert')).not.toBeVisible();
    expect(
      await page.evaluate(
        () =>
          document.documentElement.scrollWidth >
          document.documentElement.clientWidth,
      ),
    ).toBe(false);
  } finally {
    release?.();
    await context.close();
  }
});

for (const viewport of [
  { name: 'desktop', width: 1440, height: 1000 },
  { name: 'mobile', width: 390, height: 844 },
]) {
  for (const colorScheme of ['light', 'dark'] satisfies ('light' | 'dark')[]) {
    test(`${viewport.name} ${colorScheme}: loading toast updates, stays dismissed and returns for a new refresh`, async ({
      browser,
      baseURL,
    }, testInfo) => {
      const context = await browser.newContext({
        baseURL,
        viewport,
        deviceScaleFactor: 2,
        colorScheme,
        reducedMotion: colorScheme === 'dark' ? 'reduce' : 'no-preference',
      });
      const page = await context.newPage();
      function gate() {
        let release = () => {};
        const promise = new Promise<void>((resolve) => {
          release = resolve;
        });
        return { promise, release };
      }
      const config = gate();
      const inventory = gate();
      const jobs = gate();
      const operations = gate();
      const laterInventory = gate();
      let later = false;
      let failLater = false;
      await page.route('**/api/v1/**', async (route) => {
        const path = new URL(route.request().url()).pathname;
        if (path.endsWith('/settings')) await config.promise;
        if (path.endsWith('/reconciliation'))
          await (later ? laterInventory.promise : inventory.promise);
        if (path.endsWith('/reconciliation') && later && failLater) {
          await route.fulfill({
            status: 503,
            json: {
              error: {
                code: 'providerUnavailable',
                message: 'Container read fixture failed. Retry refresh.',
              },
            },
          });
          return;
        }
        if (path.endsWith('/jobs')) await jobs.promise;
        if (path.endsWith('/operations')) await operations.promise;
        await route.fulfill({
          json: path.endsWith('/auth/status')
            ? auth
            : path.endsWith('/settings')
              ? settings
              : path.endsWith('/reconciliation')
                ? reconciliation
                : path.endsWith('/profiles')
                  ? settings.hosts
                  : [],
        });
      });
      async function capture(state: string) {
        const path = `${evidenceDir}/toast-${viewport.name}-${colorScheme}-${state}.png`;
        await page.screenshot({ path });
        await testInfo.attach(`${viewport.name} ${colorScheme} ${state}`, {
          path,
          contentType: 'image/png',
        });
      }
      try {
        const started = Promise.all(
          ['/settings', '/reconciliation', '/jobs', '/operations'].map((path) =>
            page.waitForRequest((request) =>
              request.url().endsWith(`/api/v1${path}`),
            ),
          ),
        );
        await page.clock.install();
        await page.goto('/');
        await started;
        await capture('all-pending');
        const toast = page.getByRole('complementary', {
          name: 'Dashboard loading status',
        });
        await expect(toast.getByRole('listitem')).toHaveText([
          /Settings.*Loading/,
          /Containers and proxy configuration.*Loading/,
          /Update history.*Loading/,
          /Operation history.*Loading/,
        ]);
        await expect(page.getByRole('status')).toHaveAttribute(
          'aria-live',
          'polite',
        );
        await expect(page.getByRole('status')).toHaveAttribute(
          'aria-atomic',
          'true',
        );
        await expect(toast).toHaveCount(1);
        await expect(toast.getByRole('progressbar')).toHaveCount(4);
        for (const bar of await toast.getByRole('progressbar').all())
          await expect(bar).not.toHaveAttribute('aria-valuenow');
        expect(
          await toast
            .locator('.loading-read.pending .loading-read-bar > span')
            .first()
            .evaluate((element) => getComputedStyle(element).animationName),
        ).toBe(colorScheme === 'dark' ? 'none' : 'pending-read');
        config.release();
        await expect(toast.getByRole('listitem')).toHaveText([
          /Settings.*Loaded/,
          /Containers and proxy configuration.*Loading/,
          /Update history.*Loading/,
          /Operation history.*Loading/,
        ]);
        await capture('settings-loaded');
        jobs.release();
        await expect(toast.getByRole('listitem')).toHaveText([
          /Settings.*Loaded/,
          /Containers and proxy configuration.*Loading/,
          /Update history.*Loaded/,
          /Operation history.*Loading/,
        ]);
        const close = toast.getByRole('button', {
          name: 'Dismiss loading status',
        });
        await close.focus();
        await close.press('Enter');
        await expect(toast).not.toBeVisible();
        await expect(
          page.getByRole('button', { name: 'Cancel refresh', exact: true }),
        ).toBeFocused();
        await capture('dismissed-still-loading');
        operations.release();
        await expect(
          page.getByRole('button', { name: 'Cancel refresh', exact: true }),
        ).toBeEnabled();
        await expect(toast).not.toBeVisible();
        inventory.release();
        await expect(
          page.getByRole('button', { name: 'Refresh', exact: true }),
        ).toBeEnabled();
        await expect(
          page.getByRole('button', { name: 'Configure immich, VMID 604' }),
        ).toBeVisible();
        later = true;
        await page
          .getByRole('button', { name: 'Refresh', exact: true })
          .click();
        await expect(toast.getByRole('listitem')).toHaveText([
          /Settings.*Loaded/,
          /Containers and proxy configuration.*Loading/,
          /Update history.*Loaded/,
          /Operation history.*Loaded/,
        ]);
        await capture('later-refresh');
        const box = await toast.boundingBox();
        if (!box) throw new Error('Loading toast is missing');
        expect(box.x).toBeGreaterThanOrEqual(16);
        expect(box.x + box.width).toBeLessThanOrEqual(viewport.width - 16);
        expect(box.y + box.height).toBeLessThanOrEqual(viewport.height - 16);
        expect(
          await page.evaluate(
            () =>
              document.documentElement.scrollWidth >
              document.documentElement.clientWidth,
          ),
        ).toBe(false);
        failLater = true;
        laterInventory.release();
        await expect(toast.locator('.loading-read.failed')).toContainText(
          'Containers and proxy configuration',
        );
        await expect(page.getByRole('alert')).toContainText(
          'Container read fixture failed',
        );
        await capture('failed');
        await page.clock.fastForward(1200);
        await expect(toast).not.toBeVisible();
        await expect(page.getByRole('alert')).toBeVisible();
        failLater = false;
        await page
          .getByRole('button', { name: 'Retry refresh', exact: true })
          .click();
        await expect(toast.locator('.loading-read.loaded')).toHaveCount(4);
        await expect(
          toast.getByText('Refresh complete', { exact: true }),
        ).toBeVisible();
        await capture('completed-retry');
        await close.focus();
        await page.clock.fastForward(5000);
        await expect(close).toBeFocused();
        await expect(toast).toBeVisible();
        await capture('focused-completion');
        const refresh = page.getByRole('button', {
          name: 'Refresh',
          exact: true,
        });
        // An async action can request another refresh without moving focus.
        const nextInventory = page.waitForResponse((response) =>
          response.url().endsWith('/api/v1/reconciliation'),
        );
        await refresh.evaluate((button) => {
          if (!(button instanceof HTMLButtonElement))
            throw new Error('Refresh control must be a button');
          button.click();
        });
        await nextInventory;
        await expect(toast.locator('.loading-read.loaded')).toHaveCount(4);
        await page.clock.fastForward(5000);
        await expect(close).toBeFocused();
        await expect(toast).toBeVisible();
        await capture('focused-next-refresh');
        await refresh.focus();
        await page.clock.fastForward(1200);
        await expect(toast).not.toBeVisible();
        await expect(refresh).toBeFocused();
        await expect(page.getByRole('alert')).not.toBeVisible();
        await expect(
          page.getByRole('button', { name: 'Refresh', exact: true }),
        ).toBeEnabled();
      } finally {
        for (const hold of [
          config,
          inventory,
          jobs,
          operations,
          laterInventory,
        ])
          hold.release();
        await context.close();
      }
    });
  }
}
