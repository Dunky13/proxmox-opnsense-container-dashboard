import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { App } from '../src/App';
import { auth, reconciliation, settings } from './fixtures';

const stoppedReason = /Updates require a running container/;
const readinessReason = /Readiness checks require a running container/;

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
              ? reconciliation
              : path === '/jobs' || path === '/operations'
                ? []
                : null;
      return Response.json(body, { status: body === null ? 404 : 200 });
    }),
  );
}
beforeEach(() => localStorage.clear());
afterEach(() => vi.unstubAllGlobals());

const record = async (name: string) => {
  const card = (
    await screen.findByRole('button', { name: `Inspect ${name}, VMID 606` })
  ).closest('article');
  if (!card) throw new Error('Missing record');
  return card;
};

it('shows one reason for a stopped container on its card, its row and its inspector', async () => {
  mockApi();
  const user = userEvent.setup();
  render(<App />);

  const card = await record('vaultwarden');
  expect(
    within(card).getByRole('button', { name: /Review app update/ }),
  ).toBeDisabled();
  expect(
    within(card).getByRole('button', { name: /Review os update/ }),
  ).toBeDisabled();
  const cardReason = within(card).getByText(stoppedReason).textContent;

  await user.click(screen.getByRole('button', { name: 'Rows' }));
  const row = await record('vaultwarden');
  expect(row.className).toContain('container-row');
  expect(within(row).getByText(stoppedReason).textContent).toBe(cardReason);

  await user.click(
    within(row).getByRole('button', {
      name: 'Configure vaultwarden, VMID 606',
    }),
  );
  const inspector = screen.getByRole('complementary', {
    name: 'Details for vaultwarden',
  });
  // Both update actions share the reason, so the card lists it once and the
  // inspector under each button.
  const inspectorReasons = within(inspector)
    .getAllByText(stoppedReason)
    .map((node) => node.textContent);
  expect(inspectorReasons).toEqual([cardReason, cardReason]);
  expect(
    within(inspector).getByRole('button', { name: 'App update' }),
  ).toBeDisabled();
  expect(
    within(inspector).getByRole('button', { name: 'OS update' }),
  ).toBeDisabled();
});

it('explains why the readiness check is disabled instead of silently greying it out', async () => {
  mockApi();
  const user = userEvent.setup();
  render(<App />);
  const card = await record('vaultwarden');
  await user.click(
    within(card).getByRole('button', {
      name: 'Configure vaultwarden, VMID 606',
    }),
  );
  const inspector = screen.getByRole('complementary', {
    name: 'Details for vaultwarden',
  });
  const button = within(inspector).getByRole('button', {
    name: 'Check maintenance readiness',
  });
  expect(button).toBeDisabled();
  const reason = within(inspector).getByText(readinessReason);
  expect(reason).toBeVisible();
  expect(button).toHaveAttribute('aria-describedby', reason.id);
});

it('enables readiness and update actions for a running container on every surface', async () => {
  mockApi();
  const user = userEvent.setup();
  render(<App />);
  const card = (
    await screen.findByRole('button', { name: 'Inspect immich, VMID 604' })
  ).closest('article');
  if (!card) throw new Error('Missing record');
  expect(
    within(card).getByRole('button', { name: /Review app update/ }),
  ).toBeEnabled();
  await user.click(
    within(card).getByRole('button', { name: 'Configure immich, VMID 604' }),
  );
  const inspector = screen.getByRole('complementary', {
    name: 'Details for immich',
  });
  expect(
    within(inspector).getByRole('button', { name: 'App update' }),
  ).toBeEnabled();
  expect(
    within(inspector).getByRole('button', {
      name: 'Check maintenance readiness',
    }),
  ).toBeEnabled();
});
