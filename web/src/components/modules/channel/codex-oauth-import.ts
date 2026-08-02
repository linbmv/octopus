export const MAX_CODEX_OAUTH_CREDENTIAL_BYTES = 8192;

export type CodexOAuthImportError =
    | 'empty'
    | 'tooLarge'
    | 'invalidJson'
    | 'invalidType'
    | 'missingAccessToken';

export type CodexOAuthImportResult =
    | { ok: true; value: string }
    | { ok: false; error: CodexOAuthImportError };

export function parseCodexOAuthCredentialImport(raw: string): CodexOAuthImportResult {
    const value = raw.trim();
    if (!value) {
        return { ok: false, error: 'empty' };
    }
    if (new TextEncoder().encode(value).byteLength > MAX_CODEX_OAUTH_CREDENTIAL_BYTES) {
        return { ok: false, error: 'tooLarge' };
    }

    let parsed: unknown;
    try {
        parsed = JSON.parse(value);
    } catch {
        return { ok: false, error: 'invalidJson' };
    }
    if (!isRecord(parsed)) {
        return { ok: false, error: 'invalidJson' };
    }

    if (typeof parsed.type === 'string' && parsed.type.trim() && parsed.type.trim().toLowerCase() !== 'codex') {
        return { ok: false, error: 'invalidType' };
    }

    const flatAccessToken = typeof parsed.access_token === 'string' ? parsed.access_token.trim() : '';
    const nestedAccessToken = isRecord(parsed.tokens) && typeof parsed.tokens.access_token === 'string'
        ? parsed.tokens.access_token.trim()
        : '';
    if (!flatAccessToken && !nestedAccessToken) {
        return { ok: false, error: 'missingAccessToken' };
    }

    return { ok: true, value };
}

function isRecord(value: unknown): value is Record<string, unknown> {
    return typeof value === 'object' && value !== null && !Array.isArray(value);
}
