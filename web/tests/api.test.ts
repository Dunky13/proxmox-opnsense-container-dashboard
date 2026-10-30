import { afterEach, expect, it, vi } from 'vitest';
import { Api } from '../src/api';
import { settingsSchema } from '../src/schemas';
import { settings } from './fixtures';

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

it('keeps the deadline through a hung response body, ignores late completion and frees timers', async () => {
  vi.useFakeTimers();
  let complete: ((value: string) => void) | undefined;
  const response = new Response();
  vi.spyOn(response, 'text').mockImplementation(
    () =>
      new Promise<string>((resolve) => {
        complete = resolve;
      }),
  );
  vi.stubGlobal(
    'fetch',
    vi.fn<typeof fetch>(async () => response),
  );
  const request = new Api('', vi.fn()).request('/settings', settingsSchema);
  const rejected = expect(request).rejects.toMatchObject({ code: 'timeout' });
  await vi.advanceTimersByTimeAsync(105_000);
  await rejected;
  complete?.(JSON.stringify(settings));
  await vi.advanceTimersByTimeAsync(0);
  expect(vi.getTimerCount()).toBe(0);
});

it('distinguishes caller cancellation from deadline and ignores a late unauthorized response', async () => {
  vi.useFakeTimers();
  let complete: ((value: Response) => void) | undefined;
  vi.stubGlobal(
    'fetch',
    vi.fn<typeof fetch>(
      () =>
        new Promise<Response>((resolve) => {
          complete = resolve;
        }),
    ),
  );
  const unauthorized = vi.fn();
  const controller = new AbortController();
  const request = new Api('', unauthorized).request(
    '/settings',
    settingsSchema,
    { signal: controller.signal },
  );
  const rejected = expect(request).rejects.toMatchObject({
    name: 'AbortError',
  });
  controller.abort();
  await rejected;
  complete?.(new Response('{}', { status: 401 }));
  await vi.advanceTimersByTimeAsync(0);
  expect(unauthorized).not.toHaveBeenCalled();
  expect(vi.getTimerCount()).toBe(0);
});

it('does not start an already cancelled request', async () => {
  vi.useFakeTimers();
  const fetchMock = vi.fn<typeof fetch>();
  vi.stubGlobal('fetch', fetchMock);
  const controller = new AbortController();
  controller.abort();
  await expect(
    new Api('', vi.fn()).request('/settings', settingsSchema, {
      signal: controller.signal,
    }),
  ).rejects.toMatchObject({ name: 'AbortError' });
  expect(fetchMock).not.toHaveBeenCalled();
  expect(vi.getTimerCount()).toBe(0);
});

it('allows a guarded write beyond the read deadline and never retries an unknown outcome', async () => {
  vi.useFakeTimers();
  let complete: ((value: Response) => void) | undefined;
  const fetchMock = vi.fn<typeof fetch>(
    () =>
      new Promise<Response>((resolve) => {
        complete = resolve;
      }),
  );
  vi.stubGlobal('fetch', fetchMock);
  const api = new Api('', vi.fn());
  const successful = api.request('/settings', settingsSchema, {
    method: 'PUT',
    body: '{}',
  });
  await vi.advanceTimersByTimeAsync(110_000);
  complete?.(new Response(JSON.stringify(settings)));
  await expect(successful).resolves.toEqual(settings);
  const uncertain = api.request('/settings', settingsSchema, {
    method: 'PUT',
    body: '{}',
  });
  const rejected = expect(uncertain).rejects.toMatchObject({
    code: 'timeout',
    message: expect.stringContaining('outcome may be unknown'),
  });
  await vi.advanceTimersByTimeAsync(120_000);
  await rejected;
  expect(fetchMock).toHaveBeenCalledTimes(2);
  expect(vi.getTimerCount()).toBe(0);
});

it('cleans up the caller listener and deadline after success', async () => {
  vi.useFakeTimers();
  vi.stubGlobal(
    'fetch',
    vi.fn<typeof fetch>(async () => new Response(JSON.stringify(settings))),
  );
  const controller = new AbortController();
  const remove = vi.spyOn(controller.signal, 'removeEventListener');
  await expect(
    new Api('', vi.fn()).request('/settings', settingsSchema, {
      signal: controller.signal,
    }),
  ).resolves.toEqual(settings);
  expect(remove).toHaveBeenCalledWith('abort', expect.any(Function));
  expect(vi.getTimerCount()).toBe(0);
});

it('reports active authorization rejection and invalid bodies without inventing data', async () => {
  const unauthorized = vi.fn();
  const fetchMock = vi
    .fn<typeof fetch>()
    .mockResolvedValueOnce(new Response('{}', { status: 401 }))
    .mockResolvedValueOnce(new Response('{'));
  vi.stubGlobal('fetch', fetchMock);
  const api = new Api('', unauthorized);
  await expect(api.request('/settings', settingsSchema)).rejects.toMatchObject({
    status: 401,
    code: 'unauthenticated',
  });
  expect(unauthorized).toHaveBeenCalledOnce();
  await expect(api.request('/settings', settingsSchema)).rejects.toMatchObject({
    code: 'invalidResponse',
  });
});

it('bounds an indefinitely pending read and aborts its transport', async () => {
  vi.useFakeTimers();
  let signal: AbortSignal | null | undefined;
  vi.stubGlobal(
    'fetch',
    vi.fn<typeof fetch>((_input, init) => {
      signal = init?.signal;
      return new Promise<Response>(() => {});
    }),
  );
  const request = new Api('', vi.fn()).request('/settings', settingsSchema);
  const rejected = expect(request).rejects.toMatchObject({ code: 'timeout' });
  await vi.advanceTimersByTimeAsync(105_000);
  expect(signal?.aborted).toBe(true);
  await rejected;
  expect(vi.getTimerCount()).toBe(0);
});
