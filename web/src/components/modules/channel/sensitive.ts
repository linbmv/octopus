/**
 * Shared secret-detection rules for the self-healing feature. Mirrors the
 * backend's requestrewrite.IsProtectedHeader so the paste gate and the
 * response-leak guard stay in lockstep with server-side redaction.
 */

const AUTH_HEADER_NAMES = new Set([
    'authorization',
    'proxy-authorization',
    'authentication',
    'x-api-key',
    'x-goog-api-key',
    'api-key',
    'apikey',
    'token',
    'x-auth-token',
    'x-access-token',
    'access-token',
    'x-amz-security-token',
    'cookie',
    'set-cookie',
]);

export function isAuthHeaderName(name: string): boolean {
    const lower = name.trim().toLowerCase();
    if (!lower) return false;
    if (AUTH_HEADER_NAMES.has(lower)) return true;
    return (
        lower.includes('authorization') ||
        lower.includes('authentication') ||
        lower.endsWith('-api-key') ||
        lower.endsWith('-token') ||
        lower.endsWith('-credential')
    );
}

export function looksLikeSecretValue(value: string): boolean {
    const lower = value.trim().toLowerCase();
    return lower.startsWith('bearer ') || lower.startsWith('basic ') || lower.startsWith('sk-');
}
