import { expect, test } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import {
  authSchema,
  accessListDiscoverySchema,
  providerSnapshotSchema,
  reconciliationSchema,
  settingsSchema,
} from '../../src/schemas';
import { createDraft, settingsPayload } from '../../src/settings-model';

const evidenceDir = process.env.DASHBOARD_EVIDENCE_DIR || '../.local/evidence';

test('saved Caddy selection, automatic VLAN subnets, per-app upstreams and unchanged shared policy', async ({
  page,
}) => {
  test.skip(
    process.env.DASHBOARD_DEMO_TEST !== '1',
    'Requires isolated demo backend.',
  );
  const auth = authSchema.parse(
    await (await page.request.get('/api/v1/auth/status')).json(),
  );
  expect(auth.demo).toBe(true);
  const current = settingsSchema.parse(
    await (await page.request.get('/api/v1/settings')).json(),
  );
  const firewall = current.opnsenses[0];
  if (!firewall) throw new Error('Demo firewall missing');
  // Test on isolated state only. Restore the saved selection when complete.
  await page.setViewportSize({ width: 1440, height: 1100 });
  await page.goto('/');
  await page
    .getByRole('navigation', { name: 'Main navigation' })
    .filter({ visible: true })
    .getByRole('button', { name: /Settings/ })
    .click();
  await expect(page.getByLabel('Default private access list')).toHaveValue('');
  await page.screenshot({
    path: `${evidenceDir}/discovery-before-selection-desktop.png`,
    fullPage: true,
  });
  const fetched = page.waitForResponse((r) =>
    new URL(r.url()).pathname.endsWith('/access-lists'),
  );
  await page
    .getByRole('button', { name: 'Fetch saved Caddy access lists' })
    .click();
  const lists = accessListDiscoverySchema.parse(await (await fetched).json());
  const shared = lists.accessLists.find((a) => a.name === 'Shared LAN and VPN');
  if (!shared) throw new Error('Shared demo policy missing');
  await expect(page.getByLabel('Default private access list')).toHaveValue('');
  await expect(
    page.getByRole('option', { name: /Forwarded client policy/ }),
  ).toBeDisabled();
  await page
    .getByRole('button', { name: 'Discover saved KEA subnets' })
    .click();
  const subnetView = page.getByLabel('Discovered KEA subnets');
  await expect(subnetView).toContainText('192.168.6.0/24');
  await expect(subnetView).toContainText('192.168.7.0/24');
  await expect(page.getByLabel('KEA subnet UUID', { exact: true })).toHaveCount(
    0,
  );
  await expect(
    page.getByLabel('Default upstream port', { exact: true }),
  ).toHaveCount(0);
  await page.getByLabel('Default private access list').selectOption(shared.id);
  await page.screenshot({
    path: `${evidenceDir}/discovery-after-selection-desktop.png`,
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({
    path: `${evidenceDir}/discovery-after-selection-mobile.png`,
    fullPage: true,
  });
  expect(
    await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth,
    ),
  ).toBe(false);
  expect(
    (
      await new AxeBuilder({ page })
        .withTags(['wcag2a', 'wcag2aa', 'wcag21aa'])
        .analyze()
    ).violations.map((v) => v.id),
  ).toEqual([]);
  await page.getByRole('button', { name: 'Save configuration' }).click();
  await expect(
    page.getByText(
      'Configuration saved. Connection tests now use this revision.',
    ),
  ).toBeVisible();
  await page.setViewportSize({ width: 1440, height: 1100 });
  const snapshot = providerSnapshotSchema.parse(
    await (
      await page.request.post(`/api/v1/opnsenses/${firewall.id}/test`, {
        data: {},
      })
    ).json(),
  );
  const sharedBefore = snapshot.resources.find((r) => r.id === shared.id);
  await page
    .getByRole('navigation', { name: 'Main navigation' })
    .filter({ visible: true })
    .getByRole('button', { name: /Containers/ })
    .click();
  for (const app of [
    {
      name: 'paperless',
      vmid: 603,
      scheme: 'http',
      port: '8000',
      subnet: '192.168.6.0/24',
    },
    {
      name: 'homebridge',
      vmid: 702,
      scheme: 'https',
      port: '8581',
      subnet: '192.168.7.0/24',
    },
  ]) {
    await page
      .getByRole('button', { name: `Configure ${app.name}, VMID ${app.vmid}` })
      .click();
    await page.getByLabel('Upstream scheme').selectOption(app.scheme);
    await page.getByLabel('Upstream port').fill(app.port);
    await page
      .getByRole('button', { name: 'Review network change', exact: true })
      .click();
    await expect(page.getByLabel('Review network plan')).toContainText(
      'Reuse shared access list: Shared LAN and VPN',
    );
    await expect(page.getByLabel('Review network plan')).toContainText(
      app.subnet,
    );
    await page.getByRole('button', { name: 'Apply reviewed change' }).click();
    await expect(
      page.getByRole('heading', { name: 'Review network change' }),
    ).not.toBeVisible();
    await expect(page.getByText(/Default shared private policy/)).toContainText(
      'Shared LAN and VPN',
    );
    await page.getByRole('button', { name: 'Close container details' }).click();
  }
  const data = reconciliationSchema.parse(
    await (await page.request.get('/api/v1/reconciliation')).json(),
  );
  expect(
    data.inventory.containers.find((c) => c.key === 'main:603')?.desiredRoute,
  ).toEqual({ scheme: 'http', port: 8000, public: false });
  expect(
    data.inventory.containers.find((c) => c.key === 'pvedell:702')
      ?.desiredRoute,
  ).toEqual({ scheme: 'https', port: 8581, public: false });
  const after = providerSnapshotSchema.parse(
    await (
      await page.request.post(`/api/v1/opnsenses/${firewall.id}/test`, {
        data: {},
      })
    ).json(),
  );
  expect(after.resources.find((r) => r.id === shared.id)).toEqual(sharedBefore);
  expect(
    after.resources.filter(
      (r) => r.kind === 'domain' && r.fields.accesslist === shared.id,
    ),
  ).toHaveLength(2);
  expect(after.resources.filter((r) => r.kind === 'acl')).toHaveLength(
    snapshot.resources.filter((r) => r.kind === 'acl').length,
  );
  const saved = settingsSchema.parse(
    await (await page.request.get('/api/v1/settings')).json(),
  );
  const draft = createDraft(saved);
  for (const o of draft.opnsenses)
    if (o.id === firewall.id) o.privateAccessListId = '';
  const restored = await page.request.put('/api/v1/settings', {
    data: settingsPayload(draft),
  });
  expect(restored.ok()).toBe(true);
});
