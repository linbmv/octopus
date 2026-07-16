import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

class MemoryStorage {
    values = new Map();
    getItem(key) { return this.values.get(key) ?? null; }
    setItem(key, value) { this.values.set(key, String(value)); }
    removeItem(key) { this.values.delete(key); }
}

function jsonResponse(body, init = {}) {
    return new Response(JSON.stringify(body), {
        ...init,
        headers: { 'content-type': 'application/json', ...(init.headers ?? {}) },
    });
}

async function loadAuth() {
    vi.resetModules();
    const sessionStorage = new MemoryStorage();
    const localStorage = new MemoryStorage();
    vi.stubGlobal('window', { sessionStorage, localStorage });
    vi.stubGlobal('document', { cookie: 'octopus_csrf=csrf-token' });
    return {
        module: await import('../src/api/endpoints/user.ts'),
        sessionStorage,
    };
}

describe('administrator auth store', () => {
    beforeEach(() => {
        vi.spyOn(console, 'error').mockImplementation(() => {});
    });

    afterEach(() => {
        vi.unstubAllGlobals();
        vi.restoreAllMocks();
    });

    it('never retains or persists a JWT returned to cookie-mode browser state', async () => {
        const { module, sessionStorage } = await loadAuth();
        module.useAuthStore.getState().setAuth({
            token: 'must-not-be-retained',
            expire_at: new Date(Date.now() + 60_000).toISOString(),
            must_change_password: false,
            auth_mode: 'cookie',
        });

        expect(module.useAuthStore.getState().token).toBeNull();
        expect(sessionStorage.getItem('auth-storage')).not.toContain('must-not-be-retained');
    });

    it('calls the CSRF-protected server logout endpoint then clears local state', async () => {
        const fetchMock = vi.fn(async (url, init) => {
            expect(String(url)).toContain('/api/v1/user/logout');
            expect(init.method).toBe('POST');
            expect(init.credentials).toBe('same-origin');
            expect(new Headers(init.headers).get('X-Octopus-CSRF')).toBe('csrf-token');
            return jsonResponse({ code: 200, data: 'logged out' });
        });
        vi.stubGlobal('fetch', fetchMock);
        const { module } = await loadAuth();
        module.useAuthStore.getState().setAuth({
            expire_at: new Date(Date.now() + 60_000).toISOString(),
            must_change_password: false,
            auth_mode: 'cookie',
        });

        await module.useAuthStore.getState().logout();

        expect(fetchMock).toHaveBeenCalledOnce();
        expect(module.useAuthStore.getState()).toMatchObject({
            status: 'anonymous',
            isAuthenticated: false,
            token: null,
        });
    });
});
