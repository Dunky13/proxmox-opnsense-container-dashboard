import { act, fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { LoadingToast } from '../src/LoadingToast';
import type { DashboardReads } from '../src/use-dashboard';

const pending: DashboardReads = {
  configuration: 'pending',
  'provider inventory': 'pending',
  jobs: 'pending',
  operations: 'pending',
};
afterEach(() => vi.useRealTimers());

it('keeps a polite live region mounted before announcements', () => {
  const view = render(<LoadingToast refreshId={1} reads={null} />);
  const region = screen.getByRole('status');
  expect(region).toBeEmptyDOMElement();
  view.rerender(<LoadingToast refreshId={1} reads={pending} />);
  expect(screen.getByRole('status')).toBe(region);
  expect(region).toHaveAttribute('aria-live', 'polite');
  expect(region).toHaveAttribute('aria-atomic', 'true');
  expect(region).toHaveTextContent('Settings: Loading');
});

it('uses one toast with independent indeterminate bars and actual terminal states', () => {
  const view = render(<LoadingToast refreshId={1} reads={pending} />);
  expect(
    screen.getAllByRole('complementary', { name: 'Dashboard loading status' }),
  ).toHaveLength(1);
  expect(screen.getAllByRole('progressbar')).toHaveLength(4);
  for (const bar of screen.getAllByRole('progressbar'))
    expect(bar).not.toHaveAttribute('aria-valuenow');
  view.rerender(
    <LoadingToast
      refreshId={1}
      reads={{ ...pending, configuration: 'loaded', jobs: 'failed' }}
    />,
  );
  const rows = within(screen.getByRole('complementary')).getAllByRole(
    'listitem',
  );
  expect(rows[0]).toHaveTextContent('SettingsLoaded');
  expect(rows[1]).toHaveTextContent(
    'Containers and proxy configurationLoading',
  );
  expect(rows[2]).toHaveTextContent('Update historyFailed');
  expect(screen.getAllByRole('progressbar')).toHaveLength(2);
});

it('keeps dismissal through row transitions and shows a later explicit refresh', () => {
  const view = render(<LoadingToast refreshId={1} reads={pending} />);
  fireEvent.click(
    screen.getByRole('button', { name: 'Dismiss loading status' }),
  );
  view.rerender(
    <LoadingToast
      refreshId={1}
      reads={{ ...pending, configuration: 'loaded' }}
    />,
  );
  expect(screen.queryByRole('complementary')).not.toBeInTheDocument();
  expect(screen.getByRole('status')).toBeEmptyDOMElement();
  view.rerender(<LoadingToast refreshId={2} reads={pending} />);
  expect(screen.getAllByRole('complementary')).toHaveLength(1);
});

it('shows real completion/failure briefly then clears only the toast, cleaning its timer', async () => {
  vi.useFakeTimers();
  const view = render(<LoadingToast refreshId={1} reads={pending} />);
  view.rerender(
    <LoadingToast
      refreshId={1}
      reads={{
        configuration: 'loaded',
        'provider inventory': 'failed',
        jobs: 'loaded',
        operations: 'loaded',
      }}
    />,
  );
  expect(screen.getByText('Refresh finished with errors')).toBeVisible();
  expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1199);
  });
  expect(screen.getByRole('complementary')).toBeVisible();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1);
  });
  expect(screen.queryByRole('complementary')).not.toBeInTheDocument();
  view.rerender(
    <LoadingToast
      refreshId={2}
      reads={{
        configuration: 'loaded',
        'provider inventory': 'loaded',
        jobs: 'loaded',
        operations: 'loaded',
      }}
    />,
  );
  expect(screen.getByText('Refresh complete')).toBeVisible();
  view.unmount();
  expect(vi.getTimerCount()).toBe(0);
});
