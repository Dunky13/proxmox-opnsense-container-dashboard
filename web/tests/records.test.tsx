import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, expect, it, vi } from 'vitest';
import { App } from '../src/App';
import { ContainerIssueSummary } from '../src/ContainerIssues';
import { containerViews } from '../src/viewmodel';
import type { Reconciliation } from '../src/schemas';
import {
  auth,
  container,
  reconciliation,
  settings,
  withIssues,
} from './fixtures';

const blocker =
  'Import unavailable: expected one standalone domain or child domain for immich.example.test; found 0';
const lines = [
  'unowned reservation exactly matches IP 192.168.6.4; review existing-resource import',
  blocker,
  'expected one exact reservation; found 2 IP/MAC candidates',
];
const noisy: Reconciliation = {
  ...reconciliation,
  items: [
    {
      key: container.key,
      state: 'conflict',
      owned: false,
      // The backend appends import blockers to both lists.
      ...withIssues(
        ['unownedConflict', 'importBlocker', 'importBlocker', 'importBlocker'],
        [lines[0], blocker, blocker, lines[2]],
      ),
      importIssues: [blocker, lines[2], blocker],
      existingResources: [
        {
          kind: 'reservation',
          id: 'res-1',
          name: 'immich',
          summary: 'exact IP and MAC match; unowned',
        },
      ],
    },
  ],
};
function mockApi() {
  vi.stubGlobal(
    'fetch',
    vi.fn<typeof fetch>(async (input) => {
      const path = String(input).replace('/api/v1', '');
      const body =
        path === '/auth/status'
          ? auth
          : path === '/settings'
            ? settings
            : path === '/reconciliation'
              ? noisy
              : path === '/jobs' || path === '/operations'
                ? []
                : null;
      return Response.json(body, { status: body === null ? 404 : 200 });
    }),
  );
}
afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
});

it('shows a compact deduplicated issue summary in cards and the full list only in the inspector', async () => {
  mockApi();
  const user = userEvent.setup();
  render(<App />);
  const card = (
    await screen.findByRole('button', { name: 'Inspect immich, VMID 604' })
  ).closest('article');
  if (!card) throw new Error('Missing record');
  expect(within(card).getByText(/\+2 more/)).toBeVisible();
  expect(within(card).getByText(lines[0], { exact: false })).toBeVisible();
  expect(within(card).queryByText(blocker)).not.toBeInTheDocument();
  expect(within(card).queryByText(/Existing configuration · /)).toBeNull();
  expect(within(card).queryByText('Import unavailable')).toBeNull();

  await user.click(
    within(card).getByRole('button', { name: 'Configure immich, VMID 604' }),
  );
  const inspector = screen.getByRole('complementary', {
    name: 'Details for immich',
  });
  expect(within(inspector).getByText('Import unavailable')).toBeVisible();
  expect(within(inspector).getAllByText(blocker)).toHaveLength(1);
  expect(within(inspector).getAllByText(lines[2])).toHaveLength(1);
  expect(within(inspector).getByText(/Existing configuration/)).toBeVisible();
});

it('keeps maintenance actions and the domain in rows', async () => {
  mockApi();
  const user = userEvent.setup();
  render(<App />);
  await user.click(await screen.findByRole('button', { name: 'Rows' }));
  const row = screen
    .getByRole('button', { name: 'Inspect immich, VMID 604' })
    .closest('article');
  if (!row) throw new Error('Missing record');
  expect(
    within(row).getByRole('button', { name: /Review app update immich/ }),
  ).toBeInTheDocument();
  expect(
    within(row).getByRole('button', { name: /Review os update immich/ }),
  ).toBeInTheDocument();
  expect(within(row).getByText(/^immich\./)).toBeVisible();
  expect(within(row).getByText(/\+2 more/)).toBeVisible();
});

it('uses human state labels, reuses empty filters and hides unknown versions', async () => {
  mockApi();
  const user = userEvent.setup();
  render(<App />);
  await screen.findByRole('heading', { name: 'Containers' });
  const state = screen.getByLabelText('State', { exact: true });
  expect(within(state).getByRole('option', { name: 'Running' })).toHaveValue(
    'running',
  );
  expect(within(state).getByRole('option', { name: 'Stopped' })).toHaveValue(
    'stopped',
  );
  await user.type(
    screen.getByLabelText('Search all container values'),
    'no-such-container',
  );
  await user.selectOptions(state, 'running');
  await user.click(
    screen.getByRole('button', { name: 'Clear search and filters' }),
  );
  expect(screen.getByLabelText('Search all container values')).toHaveValue('');
  expect(state).toHaveValue('');

  const [versioned, unversioned] = containerViews(
    {
      ...reconciliation,
      inventory: {
        ...reconciliation.inventory,
        containers: [container, { ...container, key: 'main:9', version: '' }],
      },
    },
    settings,
  );
  expect(versioned?.distribution).toBe('debian 13');
  expect(unversioned?.distribution).toBe('debian');
});

it('falls back to a resource or missing-details line when no issue text exists', () => {
  const [withResources, bare] = containerViews(
    {
      ...reconciliation,
      inventory: {
        ...reconciliation.inventory,
        containers: [container, { ...container, key: 'main:9', warnings: [] }],
      },
      items: [
        { ...noisy.items[0]!, ...withIssues('', []), importIssues: [] },
        {
          key: 'main:9',
          state: 'conflict',
          owned: false,
          ...withIssues('', []),
        },
      ],
    },
    settings,
  );
  if (!withResources || !bare) throw new Error('Missing views');
  const { unmount } = render(<ContainerIssueSummary view={withResources} />);
  expect(
    screen.getByText('Existing configuration · 1 resources'),
  ).toBeVisible();
  unmount();
  render(<ContainerIssueSummary view={bare} />);
  expect(screen.getByText(/Conflict details were not returned/)).toBeVisible();
});
