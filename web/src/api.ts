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
      if (error instanceof DOMException && error.name === 'AbortError')
        throw error;
      throw new ApiError(
        'Could not reach Container Proxy. Check your connection and retry.',
      );
    }
    if (response.status === 401) {
      this.unauthenticated();
      throw new ApiError(
        'Your session has expired or the token was rejected. Sign in to continue.',
        401,
        'unauthenticated',
      );
    }
    const raw = await response.text();
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
    const response = await fetch('/api/v1/auth/logout', {
      method: 'POST',
      credentials: 'same-origin',
      headers: this.token ? { Authorization: `Bearer ${this.token}` } : {},
    });
    if (!response.ok)
      throw new ApiError(
        'Sign out could not be confirmed. Retry before leaving this shared browser.',
        response.status,
      );
  }
}
