import { describe, expect, it, vi } from 'vitest';
import { Api } from '../src/api';
import { reconciliationSchema } from '../src/schemas';
import {
  createDraft,
  settingsPayload,
  validateDraft,
} from '../src/settings-model';
import {
  containerViews,
  emptyFilters,
  filterViews,
  updateBlocked,
} from '../src/viewmodel';
import { container, reconciliation, settings } from './fixtures';

describe('API boundaries', () => {
  it('accepts Go null slices without accepting malformed or missing data', () => {
    expect(
      reconciliationSchema.parse({ ...reconciliation, items: null }).items,
    ).toEqual([]);
    expect(
      reconciliationSchema.safeParse({ ...reconciliation, items: ['bad'] })
        .success,
    ).toBe(false);
    expect(
      reconciliationSchema.safeParse({ inventory: reconciliation.inventory })
        .success,
    ).toBe(false);
  });
  it('returns to authentication on 401, rather than substituting empty inventory', async () => {
    const unauthenticated = vi.fn();
    vi.stubGlobal(
      'fetch',
      vi
        .fn<typeof fetch>()
        .mockResolvedValue(new Response('{}', { status: 401 })),
    );
    await expect(
      new Api('', unauthenticated).request(
        '/reconciliation',
        reconciliationSchema,
      ),
    ).rejects.toMatchObject({ status: 401 });
    expect(unauthenticated).toHaveBeenCalledOnce();
    vi.unstubAllGlobals();
  });
  it('rejects invalid JSON shapes visibly', async () => {
    vi.stubGlobal(
      'fetch',
      vi
        .fn<typeof fetch>()
        .mockResolvedValue(new Response('{"containers":[]}')),
    );
    await expect(
      new Api('', vi.fn()).request('/reconciliation', reconciliationSchema),
    ).rejects.toMatchObject({ code: 'invalidResponse' });
    vi.unstubAllGlobals();
  });
});
describe('settings writes', () => {
  it('preserves omitted secrets and strips every read-only secret flag', () => {
    const payload = settingsPayload(createDraft(settings));
    expect(payload.hosts[0]).not.toHaveProperty('tokenSecret');
    expect(payload.hosts[0]).not.toHaveProperty('hasTokenSecret');
    expect(payload.opnsenses[0]).not.toHaveProperty('apiKey');
    expect(payload.opnsenses[0]).not.toHaveProperty('apiSecret');
    expect(payload.opnsenses[0]).not.toHaveProperty('hasApiKey');
    expect(payload.opnsenses[0]).not.toHaveProperty('hasApiSecret');
    expect(payload.revision).toBe(7);
    expect(validateDraft(createDraft(settings))).toEqual([]);
  });
  it('clears only explicitly checked secrets and preserves unchanged siblings', () => {
    const draft = createDraft(settings);
    draft.hosts = draft.hosts.map((h) => ({
      ...h,
      clearTokenSecret: h.id === 'main',
    }));
    draft.opnsenses = draft.opnsenses.map((o) => ({
      ...o,
      apiKey: 'replacement-key',
      clearApiSecret: true,
    }));
    const payload = settingsPayload(draft);
    expect(payload.hosts[0]).toHaveProperty('tokenSecret', '');
    expect(payload.hosts[1]).not.toHaveProperty('tokenSecret');
    expect(payload.opnsenses[0]).toMatchObject({
      apiKey: 'replacement-key',
      apiSecret: '',
    });
  });
  it('rejects broad access ranges and mismatched address profiles', () => {
    const draft = createDraft(settings);
    draft.opnsenses = draft.opnsenses.map((o) => ({
      ...o,
      allowlistCidrs: ['::/0'],
    }));
    expect(validateDraft(draft).length).toBeGreaterThan(0);
    draft.opnsenses = createDraft(settings).opnsenses;
    draft.hosts = draft.hosts.map((h) => ({ ...h, network: '192.168.8.0/24' }));
    expect(validateDraft(draft).join(' ')).toContain('matching');
  });
});
describe('shared inventory view model', () => {
  it('composes filters and searches identity, address, provider and distro values', () => {
    const views = containerViews(reconciliation, settings);
    expect(
      filterViews(views, {
        query: 'firewall.example.test 06:04 13 immich.example.test',
        host: 'main',
        state: 'running',
        distribution: 'debian',
        exposure: 'LAN / VPN',
      }).map((v) => v.container.key),
    ).toEqual(['main:604']);
    expect(
      filterViews(views, { ...emptyFilters, query: '192.168.7.1' }).map(
        (v) => v.container.key,
      ),
    ).toEqual(['pvedell:701']);
    expect(
      filterViews(views, {
        ...emptyFilters,
        query: 'vaultwarden',
        state: 'running',
      }),
    ).toEqual([]);
  });
  it('explains stopped/template/SSH update exclusions truthfully', () => {
    const views = containerViews(reconciliation, settings);
    const stopped = views.find((v) => v.container.status === 'stopped');
    const running = views.find((v) => v.container.key === container.key);
    expect(stopped && updateBlocked(stopped, [], 'appUpdate')).toContain(
      'running',
    );
    expect(running && updateBlocked(running, [], 'appUpdate')).toBe('');
    expect(
      running &&
        updateBlocked(
          { ...running, container: { ...running.container, template: true } },
          [],
          'appUpdate',
        ),
    ).toContain('Templates');
    expect(
      views.find((v) => v.container.hostId === 'pvedell')?.distribution,
    ).toBe('debian 13');
  });
});

it('filters unknown distributions separately from all distributions and searches route overrides', () => {
  const views = containerViews(
    {
      ...reconciliation,
      inventory: {
        ...reconciliation.inventory,
        containers: [
          container,
          {
            ...container,
            key: 'main:609',
            vmid: 609,
            distribution: '',
            version: '',
          },
        ],
      },
    },
    settings,
  );
  expect(
    filterViews(views, { ...emptyFilters, distribution: '__unknown__' }).map(
      (v) => v.container.key,
    ),
  ).toEqual(['main:609']);
  expect(
    filterViews(views, { ...emptyFilters, query: 'http 2283' }),
  ).toHaveLength(2);
});
