import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Api } from '../src/api';
import { SettingsEditor } from '../src/SettingsEditor';
import {
  accessListDiscoverySchema,
  subnetDiscoverySchema,
} from '../src/schemas';
import {
  createDraft,
  settingsPayload,
  validateDraft,
} from '../src/settings-model';
import { settings } from './fixtures';

const safeId = 'aaaaaaaa-1111-4111-8111-111111111111';
const unsafeId = 'aaaaaaaa-2222-4222-8222-222222222222';
const discovery = {
  opnsenseId: 'edge',
  revision: 7,
  accessLists: [
    {
      id: safeId,
      name: 'Shared LAN and VPN',
      description: 'Manual sites also use this policy',
      cidrs: ['10.8.0.0/24', '192.168.6.0/24'],
      matcher: 'client_ip',
      clientIpContext: {
        trustedProxyListId: 'aaaaaaaa-4444-4444-8444-444444444444',
        trustedProxyCidrs: ['192.0.2.0/24'],
        headers: [
          {
            id: 'aaaaaaaa-5555-4555-8555-555555555555',
            type: 'X-Forwarded-For',
          },
        ],
        headerOrder: 'configured',
        enableLayer4: '0',
        proxyProtocolAllow: '',
        suitable: true,
        reason: '',
        summary: 'client_ip preserves configured Caddy trust.',
      },
      inversion: '0',
      suitable: true,
      reason: '',
    },
    {
      id: unsafeId,
      name: 'Forwarded clients',
      description: '',
      cidrs: ['192.168.6.0/24'],
      matcher: 'client_ip',
      inversion: '0',
      suitable: false,
      reason: 'Unsafe global trusted proxy CIDRs.',
    },
  ],
};
function response(value: string, status = 200) {
  return new Response(value, {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}
afterEach(() => vi.unstubAllGlobals());

describe('saved firewall discovery', () => {
  it('validates discovery boundaries and preserves optional legacy settings without requiring them', () => {
    expect(
      accessListDiscoverySchema.safeParse({ ...discovery, accessLists: null })
        .success,
    ).toBe(false);
    expect(
      accessListDiscoverySchema.safeParse({
        ...discovery,
        accessLists: [{ id: safeId }],
      }).success,
    ).toBe(false);
    expect(
      subnetDiscoverySchema.safeParse({
        opnsenseId: 'edge',
        revision: 7,
        subnets: [],
      }).success,
    ).toBe(true);
    expect(
      subnetDiscoverySchema.safeParse({
        opnsenseId: 'edge',
        revision: 7,
        subnets: 'failed',
      }).success,
    ).toBe(false);
    const draft = createDraft(settings);
    const firewall = draft.opnsenses[0];
    if (!firewall) throw new Error('fixture missing');
    firewall.subnetId = '';
    firewall.certificateId = '';
    firewall.domainReady = false;
    delete firewall.upstreamScheme;
    delete firewall.upstreamPort;
    firewall.privateAccessListId = safeId;
    expect(validateDraft(draft)).toEqual([]);
    const payload = settingsPayload(draft);
    expect(payload.opnsenses[0]).toHaveProperty('privateAccessListId', safeId);
    expect(JSON.stringify(payload)).not.toMatch(
      /editorKey|accessLists|hasApi|clearApi/,
    );
    firewall.privateAccessListId = 'invalid';
    expect(validateDraft(draft).length).toBeGreaterThan(0);
  });
  it('loads lists without adopting the first, keeps selection on failure and saves only settings fields', async () => {
    let failed = false;
    let payload: BodyInit | null | undefined;
    const fetcher = vi.fn<typeof fetch>(async (input, init) => {
      const path = String(input);
      if (path.endsWith('/profiles')) return response('[]');
      if (path.endsWith('/access-lists'))
        return failed
          ? response(
              '{"error":{"code":"unavailable","message":"Caddy permission denied"}}',
              503,
            )
          : response(JSON.stringify(discovery));
      if (path.endsWith('/settings') && init?.method === 'PUT') {
        payload = init.body;
        return response(
          JSON.stringify({
            ...settings,
            revision: 8,
            opnsenses: settings.opnsenses.map((o) => ({
              ...o,
              privateAccessListId: safeId,
            })),
          }),
        );
      }
      return response('{}', 404);
    });
    vi.stubGlobal('fetch', fetcher);
    const user = userEvent.setup();
    render(
      <SettingsEditor
        api={new Api('', vi.fn())}
        settings={settings}
        onSaved={async () => {}}
      />,
    );
    const select = screen.getByLabelText('Default private access list');
    expect(select).toHaveValue('');
    await user.click(
      screen.getByRole('button', { name: 'Fetch saved Caddy access lists' }),
    );
    await screen.findByRole('option', { name: 'Shared LAN and VPN' });
    expect(select).toHaveValue('');
    expect(
      screen.getByRole('option', { name: 'Shared LAN and VPN' }),
    ).toBeEnabled();
    expect(
      screen.getByLabelText('Discovered Caddy access lists'),
    ).toHaveTextContent('Matcher: client_ip');
    expect(
      screen.getByLabelText('Discovered Caddy access lists'),
    ).toHaveTextContent('Selected headers: X-Forwarded-For');
    expect(
      screen.getByRole('option', { name: /Forwarded clients/ }),
    ).toBeDisabled();
    expect(
      screen.getByLabelText('Discovered Caddy access lists'),
    ).toHaveTextContent('10.8.0.0/24');
    await user.selectOptions(select, safeId);
    failed = true;
    await user.click(
      screen.getByRole('button', { name: 'Fetch saved Caddy access lists' }),
    );
    await screen.findByRole('alert');
    expect(screen.getByRole('alert')).toHaveTextContent(
      'Previously loaded definitions are stale',
    );
    expect(select).toHaveValue(safeId);
    await user.click(
      screen.getByRole('button', { name: 'Save configuration' }),
    );
    await waitFor(() => expect(payload).toBeDefined());
    expect(payload).toContain(`"privateAccessListId":"${safeId}"`);
    expect(payload).toContain('"revision":7');
    expect(payload).not.toMatch(
      /hasApi|apiSecret|apiKey|accessLists|listError|subnets|editorKey/,
    );
  });
  it('distinguishes empty discovery, unavailable definitions and changed credential drafts', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn<typeof fetch>(async (input) =>
        String(input).endsWith('/access-lists')
          ? response(JSON.stringify({ ...discovery, accessLists: [] }))
          : response('[]'),
      ),
    );
    const current = {
      ...settings,
      opnsenses: settings.opnsenses.map((o) => ({
        ...o,
        privateAccessListId: safeId,
      })),
    };
    const user = userEvent.setup();
    render(
      <SettingsEditor
        api={new Api('', vi.fn())}
        settings={current}
        onSaved={async () => {}}
      />,
    );
    expect(screen.getByLabelText('Default private access list')).toHaveValue(
      safeId,
    );
    expect(
      screen.getByRole('option', { name: /definition not loaded/ }),
    ).toBeInTheDocument();
    await user.click(
      screen.getByRole('button', { name: 'Fetch saved Caddy access lists' }),
    );
    await screen.findByText(/No Caddy access lists exist/);
    expect(screen.getByLabelText('Default private access list')).toHaveValue(
      safeId,
    );
    expect(
      screen.getByRole('option', { name: /definition missing/ }),
    ).toBeInTheDocument();
    await user.type(screen.getByLabelText('API secret'), 'draft-secret');
    expect(
      screen.getByRole('button', { name: 'Fetch saved Caddy access lists' }),
    ).toBeDisabled();
    expect(
      screen.getByRole('button', { name: 'Discover saved KEA subnets' }),
    ).toBeDisabled();
    expect(screen.getByText(/Reads use saved credentials only/)).toBeVisible();
    expect(screen.queryByLabelText('KEA subnet UUID')).not.toBeInTheDocument();
    expect(
      screen.queryByLabelText('Default upstream port'),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByLabelText('Default upstream scheme'),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByText(/DNS and domain prerequisites are configured/),
    ).not.toBeInTheDocument();
  });
});
