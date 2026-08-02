import { readFile } from 'node:fs/promises';
import vm from 'node:vm';
import { describe, expect, it, vi } from 'vitest';

async function loadServiceWorker(existingCacheNames = []) {
    const listeners = new Map();
    const cache = {
        addAll: vi.fn(async () => undefined),
        match: vi.fn(async () => undefined),
        put: vi.fn(async () => undefined),
    };
    const caches = {
        delete: vi.fn(async () => true),
        keys: vi.fn(async () => existingCacheNames),
        open: vi.fn(async () => cache),
    };
    const fetch = vi.fn(async () => new Response('ok', { status: 200 }));
    const self = {
        addEventListener: vi.fn((type, listener) => listeners.set(type, listener)),
        clients: {
            claim: vi.fn(async () => undefined),
            matchAll: vi.fn(async () => []),
        },
        skipWaiting: vi.fn(async () => undefined),
    };
    const source = await readFile(new URL('../public/sw.js', import.meta.url), 'utf8');
    vm.runInNewContext(source, {
        URL,
        Promise,
        Response,
        Set,
        caches,
        console,
        fetch,
        location: { origin: 'https://octopus.test' },
        self,
    });
    return {
        cache,
        caches,
        fetch,
        onActivate: listeners.get('activate'),
        onFetch: listeners.get('fetch'),
    };
}

function dispatch(onFetch, path, { authorization, mode = 'cors' } = {}) {
    const respondWith = vi.fn();
    onFetch({
        request: {
            headers: authorization ? new Headers({ Authorization: authorization }) : new Headers(),
            method: 'GET',
            mode,
            url: `https://octopus.test${path}`,
        },
        respondWith,
    });
    return respondWith;
}

describe('service worker cache boundary', () => {
    it('never handles API-key model lists or health endpoints through Cache Storage', async () => {
        const { caches, onFetch } = await loadServiceWorker();

        for (const authorization of ['Bearer broad-key', 'Bearer restricted-key']) {
            expect(dispatch(onFetch, '/v1/models', { authorization })).not.toHaveBeenCalled();
        }
        for (const path of ['/v1beta/models', '/api/v1/user/status', '/health', '/ready', '/readiness', '/liveness', '/metrics']) {
            expect(dispatch(onFetch, path)).not.toHaveBeenCalled();
        }
        expect(caches.open).not.toHaveBeenCalled();
    });

    it('caches only explicit static assets and navigation app shell responses', async () => {
        const { caches, onFetch } = await loadServiceWorker();

        expect(dispatch(onFetch, '/unknown-dynamic-path')).not.toHaveBeenCalled();
        expect(dispatch(onFetch, '/logo.svg')).toHaveBeenCalledOnce();
        expect(dispatch(onFetch, '/', { mode: 'navigate' })).toHaveBeenCalledOnce();
        await vi.waitFor(() => expect(caches.open).toHaveBeenCalled());
    });

    it('bumps the cache namespace and deletes the old API-contaminated cache on activation', async () => {
        const { caches, onActivate } = await loadServiceWorker([
            'octopus-app-v1',
            'octopus-static-v1',
            'octopus-app-v2',
            'octopus-static-v2',
            'octopus-app-v3',
            'octopus-static-v3',
            'octopus-font',
            'unrelated-cache',
        ]);
        let activation;
        onActivate({ waitUntil: (promise) => { activation = promise; } });
        await activation;

        expect(caches.delete).toHaveBeenCalledWith('octopus-app-v1');
        expect(caches.delete).toHaveBeenCalledWith('octopus-static-v1');
        for (const retained of ['octopus-app-v3', 'octopus-static-v3', 'octopus-font', 'unrelated-cache']) {
            expect(caches.delete).not.toHaveBeenCalledWith(retained);
        }
    });
});
