// Service Worker for Octopus PWA
// Next.js: hashed assets under /_next/static/ are immutable (Cache First)

/**
 * Cache naming
 * - Prefix MUST match `web/src/lib/sw.ts` (OCTOPUS_CACHE_PREFIX)
 * - Bump CACHE_VERSION when you change caching behavior in this file
 * - FONT cache is version-independent (fonts persist across updates)
 */
const CACHE_PREFIX = 'octopus';
const CACHE_VERSION = 'v2';
const CACHE_NAMES = {
    static: `${CACHE_PREFIX}-static-${CACHE_VERSION}`,
    app: `${CACHE_PREFIX}-app-${CACHE_VERSION}`,
    // Font cache is NOT versioned - persists across app updates
    font: `${CACHE_PREFIX}-font`,
};

const SW_MESSAGE_TYPE = {
    SKIP_WAITING: 'SKIP_WAITING',
    CLEAR_CACHE: 'CLEAR_CACHE',
    CACHE_CLEARED: 'CACHE_CLEARED',
};

// Precache (PWA essentials)
const PRECACHE_URLS = ['/', '/manifest.json', '/web-app-manifest-192x192.png', '/web-app-manifest-512x512.png', '/logo-dark.svg'];

const NETWORK_ONLY_PREFIXES = ['/api', '/v1', '/v1beta'];
const NETWORK_ONLY_PATHS = new Set(['/health', '/ready', '/readiness', '/liveness', '/metrics']);

function pathMatchesPrefix(pathname, prefix) {
    return pathname === prefix || pathname.startsWith(`${prefix}/`);
}

function isNetworkOnlyPath(pathname) {
    return NETWORK_ONLY_PATHS.has(pathname) || NETWORK_ONLY_PREFIXES.some((prefix) => pathMatchesPrefix(pathname, prefix));
}

function isExplicitStaticAsset(pathname) {
    if (pathname === '/manifest.json') return true;
    if (pathname.startsWith('/locale/') && pathname.endsWith('.json')) return true;
    return /\.(?:avif|bmp|css|gif|ico|jpe?g|js|map|png|svg|webmanifest|webp|woff2?|ttf)$/i.test(pathname);
}

// ============ 安装事件 ============
self.addEventListener('install', (event) => {
    event.waitUntil(
        (async () => {
            // Best-effort precache: if one asset fails, we still want the SW to install.
            try {
                const cache = await caches.open(CACHE_NAMES.app);
                await cache.addAll(PRECACHE_URLS);
            } catch {
                // ignore
            }
            await self.skipWaiting();
        })()
    );
});

// ============ 激活事件 ============
self.addEventListener('activate', (event) => {
    event.waitUntil(
        (async () => {
            // Clean up old Octopus caches (previous versions), then take control.
            await deleteOctopusCaches({ keep: new Set(Object.values(CACHE_NAMES)) });
            await self.clients.claim();
        })()
    );
});

// ============ Fetch 事件 ============
self.addEventListener('fetch', (event) => {
    const { request } = event;
    const url = new URL(request.url);

    // 只处理同源 GET 请求
    if (url.origin !== location.origin || request.method !== 'GET') {
        return;
    }

    // API、鉴权相关模型目录和健康/观测端点必须始终直连网络。尤其不能把
    // 按 Authorization/x-api-key 过滤的 /v1/models 响应放进共享 Cache Storage。
    if (isNetworkOnlyPath(url.pathname) || url.pathname.includes('webpack-hmr')) {
        return;
    }

    // 页面导航：Network First，离线时仅返回静态 app shell。
    if (request.mode === 'navigate') {
        event.respondWith(networkFirst(request, CACHE_NAMES.app, { fallbackUrl: '/' }));
        return;
    }

    // 字体资源：Cache First（永久缓存，跨版本持久化）
    if (url.pathname.endsWith('.woff2') || url.pathname.endsWith('.woff') || url.pathname.endsWith('.ttf')) {
        event.respondWith(cacheFirst(request, CACHE_NAMES.font));
        return;
    }

    // /_next/static/ 资源：Cache First（带哈希，永不变）
    if (url.pathname.startsWith('/_next/static/')) {
        event.respondWith(cacheFirst(request, CACHE_NAMES.static));
        return;
    }

    // 仅明确的静态资源进入 Cache Storage。未知 GET 路径以及未来的动态
    // Next data/API 路径保持浏览器默认 network-only，避免新增接口被意外缓存。
    if (isExplicitStaticAsset(url.pathname)) {
        event.respondWith(staleWhileRevalidate(request, CACHE_NAMES.app));
    }
});

// ============ 缓存策略 ============

/**
 * Cache First：优先缓存，适用于带哈希的不变资源
 */
async function cacheFirst(request, cacheName) {
    const cache = await caches.open(cacheName);
    const cached = await cache.match(request);
    if (cached) {
        return cached;
    }

    try {
        const response = await fetch(request);
        if (response.ok) {
            cache.put(request, response.clone());
        }
        return response;
    } catch {
        // 离线且无缓存
        return new Response('Offline', { status: 503 });
    }
}

/**
 * Network First：优先网络，适用于需要最新内容的资源
 */
async function networkFirst(request, cacheName, { fallbackUrl = null } = {}) {
    const cache = await caches.open(cacheName);
    try {
        const response = await fetch(request);
        if (response.ok) {
            cache.put(request, response.clone());
        }
        return response;
    } catch {
        const cached = await cache.match(request);
        if (cached) {
            return cached;
        }
        // 如果有 fallback（通常是首页），返回 fallback
        if (fallbackUrl) {
            const fallback = await cache.match(fallbackUrl);
            if (fallback) return fallback;
        }
        return new Response('Offline', { status: 503 });
    }
}

/**
 * Stale While Revalidate：返回缓存同时后台更新
 */
async function staleWhileRevalidate(request, cacheName) {
    const cache = await caches.open(cacheName);
    const cached = await cache.match(request);

    const fetchPromise = fetch(request)
        .then((response) => {
            if (response.ok) {
                cache.put(request, response.clone());
            }
            return response;
        })
        .catch(() => cached || new Response('Offline', { status: 503 }));

    return cached || fetchPromise;
}

// ============ 消息事件 ============
self.addEventListener('message', (event) => {
    const { type } = event.data || {};

    switch (type) {
        case SW_MESSAGE_TYPE.SKIP_WAITING:
            self.skipWaiting();
            break;

        case SW_MESSAGE_TYPE.CLEAR_CACHE:
            // Only clear Octopus caches (avoid nuking other same-origin caches).
            // PRESERVE font cache - fonts should persist across updates.
            event.waitUntil(
                (async () => {
                    await deleteOctopusCaches({ keep: new Set([CACHE_NAMES.font]) });
                    const clients = await self.clients.matchAll();
                    clients.forEach((client) => client.postMessage({ type: SW_MESSAGE_TYPE.CACHE_CLEARED }));
                })()
            );
            break;
    }
});

// ========= Helpers =========
function isOctopusCacheName(name) {
    return name.startsWith(`${CACHE_PREFIX}-`);
}

async function deleteOctopusCaches({ keep } = {}) {
    const names = await caches.keys();
    const deletions = names
        .filter((name) => isOctopusCacheName(name))
        .filter((name) => !(keep && keep.has(name)))
        .map((name) => caches.delete(name));
    await Promise.all(deletions);
}
