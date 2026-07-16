import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

async function loadClient(timeout = '60000') {
    process.env.NEXT_PUBLIC_API_TIMEOUT_MS = timeout;
    vi.resetModules();
    return import('../src/api/client.ts');
}

function jsonResponse(body, init = {}) {
    return new Response(JSON.stringify(body), {
        ...init,
        headers: { 'content-type': 'application/json', ...(init.headers ?? {}) },
    });
}

describe('api client', () => {
    beforeEach(() => {
        vi.stubGlobal('window', globalThis);
        vi.spyOn(console, 'error').mockImplementation(() => {});
    });

    afterEach(() => {
        vi.useRealTimers();
        vi.unstubAllGlobals();
        vi.restoreAllMocks();
        delete process.env.NEXT_PUBLIC_API_TIMEOUT_MS;
    });

    it('unwraps successful API envelopes and sends an API-key Bearer token', async () => {
        const fetchMock = vi.fn(async (_url, init) => {
            expect(new Headers(init.headers).get('Authorization')).toBe('Bearer session-token');
            expect(init.credentials).toBe('same-origin');
            return jsonResponse({ code: 200, data: { value: 7 } });
        });
        vi.stubGlobal('fetch', fetchMock);
        const { apiClient, setAuthStoreGetter } = await loadClient();
        setAuthStoreGetter(() => ({
            token: 'session-token',
            clearAuth: vi.fn(),
            requirePasswordChange: vi.fn(),
        }));

        await expect(apiClient.get('/api/value')).resolves.toEqual({ value: 7 });
        expect(fetchMock).toHaveBeenCalledOnce();
    });

    it('preserves backend machine codes and routes forced password changes', async () => {
        vi.stubGlobal('fetch', vi.fn(async () => jsonResponse({
            code: 403,
            message: 'change required',
            error: { code: 'PASSWORD_CHANGE_REQUIRED', message: 'change required', details: { action: 'change-password' } },
        }, { status: 403 })));
        const requirePasswordChange = vi.fn();
        const { apiClient, setAuthStoreGetter } = await loadClient();
        setAuthStoreGetter(() => ({ token: 'token', clearAuth: vi.fn(), requirePasswordChange }));

        await expect(apiClient.get('/api/protected')).rejects.toMatchObject({
            name: 'ApiError',
            status: 403,
            code: 'PASSWORD_CHANGE_REQUIRED',
            details: { action: 'change-password' },
        });
        expect(requirePasswordChange).toHaveBeenCalledOnce();
    });

    it('normalizes non-JSON HTTP errors and clears local auth on 401 without recursive logout', async () => {
        vi.stubGlobal('fetch', vi.fn(async () => new Response('invalid token', {
            status: 401,
            headers: { 'content-type': 'text/plain' },
        })));
        const clearAuth = vi.fn();
        const { apiClient, setAuthStoreGetter } = await loadClient();
        setAuthStoreGetter(() => ({ token: 'bad', clearAuth, requirePasswordChange: vi.fn() }));

        await expect(apiClient.get('/api/protected')).rejects.toMatchObject({
            status: 401,
            code: 'HTTP_401',
            message: 'invalid token',
        });
        expect(clearAuth).toHaveBeenCalledOnce();
    });

    it('uses cookie auth and the readable CSRF cookie for unsafe administrator requests', async () => {
        vi.stubGlobal('document', { cookie: 'theme=dark; octopus_csrf=csrf-value' });
        const fetchMock = vi.fn(async (_url, init) => {
            const headers = new Headers(init.headers);
            expect(headers.get('Authorization')).toBeNull();
            expect(headers.get('X-Octopus-CSRF')).toBe('csrf-value');
            expect(init.credentials).toBe('same-origin');
            return jsonResponse({ code: 200, data: 'ok' });
        });
        vi.stubGlobal('fetch', fetchMock);
        const { apiClient, setAuthStoreGetter } = await loadClient();
        setAuthStoreGetter(() => ({ token: null, clearAuth: vi.fn(), requirePasswordChange: vi.fn() }));

        await expect(apiClient.post('/api/protected', {})).resolves.toBe('ok');
        expect(fetchMock).toHaveBeenCalledOnce();
    });

    it('maps network failures to a stable ApiError', async () => {
        vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('connection lost'); }));
        const { apiClient } = await loadClient();

        await expect(apiClient.get('/api/value')).rejects.toMatchObject({
            status: 0,
            code: 'NETWORK_ERROR',
            message: 'connection lost',
        });
    });

    it('aborts requests at the configured deadline', async () => {
        vi.useFakeTimers();
        vi.stubGlobal('fetch', vi.fn((_url, init) => new Promise((_resolve, reject) => {
            init.signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')), { once: true });
        })));
        const { apiClient } = await loadClient('5');

        const result = expect(apiClient.get('/api/slow')).rejects.toMatchObject({
            status: 408,
            code: 'REQUEST_TIMEOUT',
        });
        await vi.advanceTimersByTimeAsync(6);
        await result;
    });
});
