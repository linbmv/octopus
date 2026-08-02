import { describe, expect, it } from 'vitest';
import {
    MAX_CODEX_OAUTH_CREDENTIAL_BYTES,
    parseCodexOAuthCredentialImport,
} from '@/components/modules/channel/codex-oauth-import';

describe('Codex OAuth credential JSON import', () => {
    it('accepts flat CLIProxyAPI and nested Codex auth.json documents', () => {
        expect(parseCodexOAuthCredentialImport('  {"type":"codex","access_token":"access","refresh_token":"refresh"}  ')).toEqual({
            ok: true,
            value: '{"type":"codex","access_token":"access","refresh_token":"refresh"}',
        });
        expect(parseCodexOAuthCredentialImport('{"tokens":{"access_token":"access","refresh_token":"refresh"}}').ok).toBe(true);
    });

    it('rejects invalid or unrelated credential documents', () => {
        expect(parseCodexOAuthCredentialImport('[]')).toEqual({ ok: false, error: 'invalidJson' });
        expect(parseCodexOAuthCredentialImport('{"type":"other","access_token":"access"}')).toEqual({ ok: false, error: 'invalidType' });
        expect(parseCodexOAuthCredentialImport('{"type":"codex","refresh_token":"refresh"}')).toEqual({ ok: false, error: 'missingAccessToken' });
    });

    it('enforces the backend credential size limit in UTF-8 bytes', () => {
        const oversized = JSON.stringify({ access_token: '猫'.repeat(MAX_CODEX_OAUTH_CREDENTIAL_BYTES) });
        expect(parseCodexOAuthCredentialImport(oversized)).toEqual({ ok: false, error: 'tooLarge' });
    });
});
