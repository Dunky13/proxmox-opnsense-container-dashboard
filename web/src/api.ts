import { z } from 'zod';
import { errorSchema } from './schemas';

export class ApiError extends Error {
  constructor(
    message: string,
    public status = 0,
    public code = 'connection',
  ) {
    super(message);
  }
}
export function messageOf(error: Error | object | string | null): string {
  return error instanceof ApiError
    ? error.message
    : 'The request could not be completed. Check your connection and retry.';
}
export class Api {
  constructor(
    private token: string,
    private unauthenticated: () => void,
  ) {}
  async request<T>(
    path: string,
    schema: z.ZodType<T>,
    options: {
      method?: 'GET' | 'POST' | 'PUT';
      body?: string;
      signal?: AbortSignal;
    } = {},
  ): Promise<T> {
    // The server allows 90s API work and a 100s socket write. Keep guarded
    // writes beyond both, including body delivery; never automatically retry.
    const write = options.method !== undefined && options.method !== 'GET';
    const deadline = write ? 120_000 : 105_000;
    const controller = new AbortController();
    let stopped: Error | null = null;
    let cancel = () => {};
    let timer: ReturnType<typeof setTimeout> | undefined;
    const boundary = new Promise<never>((_resolve, reject) => {
      const stop = (error: Error) => {
        if (stopped) return;
        stopped = error;
        controller.abort();
        reject(error);
      };
      cancel = () => stop(new DOMException('Request cancelled.', 'AbortError'));
      options.signal?.addEventListener('abort', cancel, { once: true });
      if (options.signal?.aborted) cancel();
      else
        timer = setTimeout(
          () =>
            stop(
              new ApiError(
                write
                  ? 'The request timed out. Its outcome may be unknown. Inspect Activity and refresh before another attempt. No automatic retry was sent.'
                  : 'The read timed out. Check your connection and retry refresh.',
                0,
                'timeout',
              ),
            ),
          deadline,
        );
    });
    const check = () => {
      if (stopped) throw stopped;
    };
    try {
      // Race the entire read, not just headers. This also bounds transports
      // that fail to settle when aborted. Late results have no side effects.
      return await Promise.race([
        this.read(
          path,
          schema,
          { ...options, signal: controller.signal },
          check,
        ),
        boundary,
      ]);
    } finally {
      clearTimeout(timer);
      options.signal?.removeEventListener('abort', cancel);
    }
  }
  private async read<T>(
    path: string,
    schema: z.ZodType<T>,
    options: {
      method?: 'GET' | 'POST' | 'PUT';
      body?: string;
      signal: AbortSignal;
    },
    check: () => void,
  ): Promise<T> {
    check();
    let response: Response;
    try {
      response = await fetch(`/api/v1${path}`, {
        method: options.method ?? 'GET',
        signal: options.signal,
        credentials: 'same-origin',
        cache: 'no-store',
        headers: {
          Accept: 'application/json',
          ...(options.body !== undefined
            ? { 'Content-Type': 'application/json' }
            : {}),
          ...(this.token ? { Authorization: `Bearer ${this.token}` } : {}),
        },
        ...(options.body !== undefined ? { body: options.body } : {}),
      });
    } catch (error) {
      check();
      if (error instanceof DOMException && error.name === 'AbortError')
        throw error;
      throw new ApiError(
        'Could not reach Container Proxy. Check your connection and retry.',
      );
    }
    check();
    if (response.status === 401) {
      this.unauthenticated();
      throw new ApiError(
        'Your session has expired or the token was rejected. Sign in to continue.',
        401,
        'unauthenticated',
      );
    }
    let raw: string;
    try {
      raw = await response.text();
    } catch {
      check();
      throw new ApiError(
        'The response could not be read. Check your connection and refresh current state before retrying.',
      );
    }
    check();
    if (!response.ok) {
      try {
        const parsed = errorSchema.safeParse(JSON.parse(raw));
        if (parsed.success)
          throw new ApiError(
            parsed.data.error.message,
            response.status,
            parsed.data.error.code,
          );
      } catch (error) {
        if (error instanceof ApiError) throw error;
      }
      throw new ApiError(
        `The server returned HTTP ${response.status}. Refresh the current state before retrying.`,
        response.status,
        'server',
      );
    }
    try {
      const parsed = schema.safeParse(JSON.parse(raw));
      if (parsed.success) return parsed.data;
    } catch {
      /* Invalid JSON is treated the same as an invalid response shape. */
    }
    throw new ApiError(
      'The server returned an invalid response. Existing data has been kept. Refresh or check the server version.',
      response.status,
      'invalidResponse',
    );
  }
  async logout(): Promise<void> {
    try {
      await this.request(
        '/auth/logout',
        z.object({ authenticated: z.literal(false) }),
        { method: 'POST' },
      );
    } catch (error) {
      throw new ApiError(
        `Sign out could not be confirmed. Retry before leaving this shared browser. ${messageOf(error instanceof Error ? error : null)}`,
        error instanceof ApiError ? error.status : 0,
        error instanceof ApiError ? error.code : 'connection',
      );
    }
  }
}
