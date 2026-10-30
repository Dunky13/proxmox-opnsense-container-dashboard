import { act, renderHook, waitFor } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { Api } from '../src/api';
import { useDashboard } from '../src/use-dashboard';
import { job, reconciliation, settings } from './fixtures';

afterEach(() => vi.unstubAllGlobals());
it('keeps a newly created job when an older full refresh completes later', async () => {
  let holdHistory = false;
  let completeHistory: ((response: Response) => void) | undefined;
  const fetchMock = vi.fn<typeof fetch>(async (input) => {
    const path = String(input);
    if (path.endsWith('/settings'))
      return new Response(JSON.stringify(settings));
    if (path.endsWith('/reconciliation'))
      return new Response(JSON.stringify(reconciliation));
    if (path.endsWith('/jobs') && holdHistory)
      return new Promise<Response>((resolve) => {
        completeHistory = resolve;
      });
    return new Response('[]');
  });
  vi.stubGlobal('fetch', fetchMock);
  const api = new Api('', vi.fn());
  const { result, unmount } = renderHook(() => useDashboard(api));
  await waitFor(() => expect(result.current.loading).toBe(false));
  holdHistory = true;
  let refreshing: Promise<void> | undefined;
  act(() => {
    refreshing = result.current.refresh();
  });
  await waitFor(() => expect(completeHistory).toBeTypeOf('function'));
  act(() => result.current.addJob(job));
  await act(async () => {
    completeHistory?.(new Response('[]'));
    await refreshing;
  });
  expect(result.current.jobs).toEqual([job]);
  const refresh = result.current.refresh;
  unmount();
  const calls = fetchMock.mock.calls.length;
  await refresh();
  expect(fetchMock).toHaveBeenCalledTimes(calls);
});
