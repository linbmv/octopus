import type { GoldenSampleInput } from '@/api/endpoints/channel';

export type GoldenSampleParseError = 'empty' | 'invalidJson' | 'missingUrl' | 'tooLarge' | 'authSecret';

const MAX_SAMPLE_BYTES = 68 * 1024;

const AUTH_HEADER_NAMES = new Set([
    'authorization',
    'proxy-authorization',
    'x-api-key',
    'api-key',
    'x-goog-api-key',
    'cookie',
    'set-cookie',
]);

/**
 * Parses a pasted golden sample into the API payload. Only compact JSON is
 * accepted client-side; auth headers are rejected before the request leaves
 * the browser so secrets never reach the backend or its logs.
 */
export function parseGoldenSampleInput(
    raw: string,
): { sample: GoldenSampleInput } | { error: GoldenSampleParseError } {
    const trimmed = raw.trim();
    if (!trimmed) return { error: 'empty' };
    if (new Blob([trimmed]).size > MAX_SAMPLE_BYTES) return { error: 'tooLarge' };

    let parsed: unknown;
    try {
        parsed = JSON.parse(trimmed);
    } catch {
        return { error: 'invalidJson' };
    }
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
        return { error: 'invalidJson' };
    }
    const record = parsed as Record<string, unknown>;
    const url = typeof record.url === 'string' ? record.url.trim() : '';
    if (!url) return { error: 'missingUrl' };

    const headers = normalizeHeaders(record.headers);
    if (headers === 'authSecret') return { error: 'authSecret' };

    return {
        sample: {
            method: typeof record.method === 'string' ? record.method : undefined,
            url,
            headers,
            body: record.body,
            source: 'ui_paste',
        },
    };
}

function normalizeHeaders(value: unknown): Record<string, string[]> | undefined | 'authSecret' {
    if (typeof value !== 'object' || value === null || Array.isArray(value)) return undefined;
    const result: Record<string, string[]> = {};
    for (const [name, entry] of Object.entries(value as Record<string, unknown>)) {
        const key = name.trim();
        if (!key) continue;
        if (AUTH_HEADER_NAMES.has(key.toLowerCase())) return 'authSecret';
        const values = Array.isArray(entry) ? entry : [entry];
        const clean = values.filter((item): item is string => typeof item === 'string');
        if (clean.some(looksLikeSecret)) return 'authSecret';
        if (clean.length > 0) result[key] = clean;
    }
    return Object.keys(result).length > 0 ? result : undefined;
}

function looksLikeSecret(value: string): boolean {
    const lower = value.trim().toLowerCase();
    return lower.startsWith('bearer ') || lower.startsWith('basic ') || lower.startsWith('sk-');
}

/** Splits a backend diff entry like "header_added:originator" into UI parts. */
export function splitDiffEntry(entry: string): { action: 'added' | 'removed' | 'changed'; subject: string } {
    const separator = entry.indexOf(':');
    const prefix = separator >= 0 ? entry.slice(0, separator) : entry;
    const subject = separator >= 0 ? entry.slice(separator + 1) : entry;
    if (prefix.endsWith('_added')) return { action: 'added', subject };
    if (prefix.endsWith('_removed')) return { action: 'removed', subject };
    return { action: 'changed', subject };
}
