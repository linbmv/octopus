import { ApiError } from './types';
import { HttpStatus } from './types';

export const API_BASE_URL = process.env.NEXT_PUBLIC_API_BASE_URL || '.';
const configuredTimeout = Number(process.env.NEXT_PUBLIC_API_TIMEOUT_MS || 60_000);
const API_REQUEST_TIMEOUT_MS = Number.isFinite(configuredTimeout) && configuredTimeout > 0
    ? configuredTimeout
    : 60_000;

/**
 * 获取认证 Store（延迟导入以避免循环依赖）
 */
let getAuthStore: (() => {
    token: string | null;
    clearAuth: () => void;
    requirePasswordChange: () => void;
}) | null = null;

export function setAuthStoreGetter(getter: () => {
    token: string | null;
    clearAuth: () => void;
    requirePasswordChange: () => void;
}) {
    getAuthStore = getter;
}

/**
 * 全局错误处理
 */
const handleError = (error: ApiError) => {
    console.error('API Error:', error);

    // A 401 only clears local state. Calling the server logout endpoint from
    // this error path would recursively produce another 401.
    if (error.status === HttpStatus.UNAUTHORIZED) {
        if (getAuthStore) {
            const store = getAuthStore();
            store.clearAuth();
        }
    } else if (error.code === 'PASSWORD_CHANGE_REQUIRED' && getAuthStore) {
        getAuthStore().requirePasswordChange();
    }
};

/**
 * 处理响应
 */
async function handleResponse<T>(response: Response): Promise<T> {
    const contentType = response.headers.get('content-type');
    const isJson = contentType?.includes('application/json');

    const body = await response.text();
    let data: unknown = body;
    if (isJson && body) {
        try {
            data = JSON.parse(body);
        } catch {
            data = body;
        }
    }

    if (!response.ok) {
        const payload = data && typeof data === 'object' ? data as {
            message?: unknown;
            error?: { code?: unknown; message?: unknown; details?: unknown };
        } : undefined;
        const serverError = payload?.error;
        const message = typeof serverError?.message === 'string'
            ? serverError.message
            : typeof payload?.message === 'string'
                ? payload.message
                : typeof data === 'string' && data
                    ? data
                    : response.statusText;
        const code = typeof serverError?.code === 'string' ? serverError.code : `HTTP_${response.status}`;
        const details = serverError?.details && typeof serverError.details === 'object'
            ? serverError.details as Record<string, unknown>
            : undefined;
        const error = new ApiError(response.status, code, message, details);

        handleError(error);
        throw error;
    }

    // 如果是标准的 ApiResponse 格式，返回 data 字段
    if (data && typeof data === 'object' && 'data' in data) {
        return data.data as T;
    }

    return data as T;
}

export const CSRF_COOKIE_NAME = 'octopus_csrf';
export const CSRF_HEADER_NAME = 'X-Octopus-CSRF';

function methodNeedsCSRF(method: string): boolean {
    return !['GET', 'HEAD', 'OPTIONS', 'TRACE'].includes(method.toUpperCase());
}

function readCookie(name: string): string | null {
    if (typeof document === 'undefined') return null;
    const prefix = `${encodeURIComponent(name)}=`;
    for (const item of document.cookie.split(';')) {
        const value = item.trim();
        if (value.startsWith(prefix)) {
            try {
                return decodeURIComponent(value.slice(prefix.length));
            } catch {
                return null;
            }
        }
    }
    return null;
}

// Shared by apiClient and the streaming/upload/download paths that need raw
// fetch. `token` is populated only for API-key mode; administrator browser
// requests authenticate with the HttpOnly session cookie and send the
// JavaScript-readable, session-bound CSRF token on unsafe methods.
export function getAuthRequestHeaders(method: string): Headers {
    const headers = new Headers();
    if (typeof window === 'undefined' || !getAuthStore) return headers;

    const store = getAuthStore();
    if (store.token) {
        headers.set('Authorization', `Bearer ${store.token}`);
        return headers;
    }
    if (methodNeedsCSRF(method)) {
        const csrfToken = readCookie(CSRF_COOKIE_NAME);
        if (csrfToken) headers.set(CSRF_HEADER_NAME, csrfToken);
    }
    return headers;
}

/**
 * 发送请求
 */
async function request<T>(
    method: string,
    path: string,
    body?: BodyInit,
    params?: Record<string, string | number | boolean>,
    extraHeaders?: HeadersInit,
): Promise<T> {
    // 构建 URL
    const searchParams = params ? new URLSearchParams(
        Object.entries(params).map(([k, v]) => [k, String(v)])
    ).toString() : '';
    const url = `${API_BASE_URL}${path}${searchParams ? `?${searchParams}` : ''}`;

    // 构建请求头
    const headers = getAuthRequestHeaders(method);
    if (extraHeaders) {
        new Headers(extraHeaders).forEach((value, key) => headers.set(key, value));
    }

    // 只在有 body 时设置 Content-Type
    if (body) {
        headers.set('Content-Type', 'application/json');
    }

    const controller = new AbortController();
    const timeout = globalThis.setTimeout(() => controller.abort(), API_REQUEST_TIMEOUT_MS);
    try {
        const response = await fetch(url.toString(), {
            method,
            headers,
            body,
            signal: controller.signal,
            credentials: 'same-origin',
        });
        return await handleResponse<T>(response);
    } catch (cause) {
        if (cause instanceof ApiError) throw cause;
        if (controller.signal.aborted) {
            throw new ApiError(408, 'REQUEST_TIMEOUT', `Request timed out after ${API_REQUEST_TIMEOUT_MS} ms`);
        }
        throw new ApiError(0, 'NETWORK_ERROR', cause instanceof Error ? cause.message : 'Network request failed');
    } finally {
        globalThis.clearTimeout(timeout);
    }
}

/**
 * API 客户端 - 基础 HTTP 方法
 */
export const apiClient = {
    /**
     * GET 请求
     */
    get: <T>(path: string, params?: Record<string, string | number | boolean>): Promise<T> =>
        request<T>('GET', path, undefined, params),

    /**
     * POST 请求
     */
    post: <T>(path: string, data?: unknown, params?: Record<string, string | number | boolean>): Promise<T> =>
        request<T>('POST', path, data ? JSON.stringify(data) : undefined, params),

    postWithHeaders: <T>(path: string, data: unknown, headers: HeadersInit): Promise<T> =>
        request<T>('POST', path, JSON.stringify(data), undefined, headers),

    /**
     * PUT 请求
     */
    put: <T>(path: string, data?: unknown, params?: Record<string, string | number | boolean>): Promise<T> =>
        request<T>('PUT', path, data ? JSON.stringify(data) : undefined, params),

    /**
     * DELETE 请求
     */
    delete: <T>(path: string, params?: Record<string, string | number | boolean>): Promise<T> =>
        request<T>('DELETE', path, undefined, params),

    /**
     * PATCH 请求
     */
    patch: <T>(path: string, data?: unknown, params?: Record<string, string | number | boolean>): Promise<T> =>
        request<T>('PATCH', path, data ? JSON.stringify(data) : undefined, params),
};
