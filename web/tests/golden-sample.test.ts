import { describe, expect, it } from 'vitest';
import {
    parseGoldenSampleInput,
    splitDiffEntry,
} from '@/components/modules/channel/golden-sample';

describe('golden sample parsing', () => {
    it('parses a compact JSON sample', () => {
        const parsed = parseGoldenSampleInput(
            JSON.stringify({
                method: 'POST',
                url: 'https://provider.test/v1/responses',
                headers: { 'User-Agent': ['codex-tui/0.144.6'], originator: 'codex-tui' },
                body: { model: 'm', input: 'hi' },
            }),
        );
        expect('sample' in parsed).toBe(true);
        if ('sample' in parsed) {
            expect(parsed.sample.url).toBe('https://provider.test/v1/responses');
            expect(parsed.sample.headers?.['User-Agent']).toEqual(['codex-tui/0.144.6']);
            expect(parsed.sample.headers?.originator).toEqual(['codex-tui']);
            expect(parsed.sample.source).toBe('ui_paste');
        }
    });

    it('rejects invalid input shapes', () => {
        expect(parseGoldenSampleInput('')).toEqual({ error: 'empty' });
        expect(parseGoldenSampleInput('not json')).toEqual({ error: 'invalidJson' });
        expect(parseGoldenSampleInput('[1,2]')).toEqual({ error: 'invalidJson' });
        expect(parseGoldenSampleInput('{"method":"POST"}')).toEqual({ error: 'missingUrl' });
    });

    it('rejects auth headers and secret-looking values before any network call', () => {
        expect(
            parseGoldenSampleInput(
                JSON.stringify({ url: 'https://x.test/v1', headers: { Authorization: ['Bearer sk-abc'] } }),
            ),
        ).toEqual({ error: 'authSecret' });
        expect(
            parseGoldenSampleInput(
                JSON.stringify({ url: 'https://x.test/v1', headers: { 'X-Custom': ['sk-plainkey'] } }),
            ),
        ).toEqual({ error: 'authSecret' });
        expect(
            parseGoldenSampleInput(
                JSON.stringify({ url: 'https://x.test/v1', headers: { Cookie: ['session=1'] } }),
            ),
        ).toEqual({ error: 'authSecret' });
    });
});

describe('diff entry presentation', () => {
    it('splits backend diff entries into action and subject', () => {
        expect(splitDiffEntry('header_added:originator')).toEqual({ action: 'added', subject: 'originator' });
        expect(splitDiffEntry('header_removed:content-type')).toEqual({ action: 'removed', subject: 'content-type' });
        expect(splitDiffEntry('header_changed:user-agent')).toEqual({ action: 'changed', subject: 'user-agent' });
        expect(splitDiffEntry('body_key_added:instructions')).toEqual({ action: 'added', subject: 'instructions' });
        expect(splitDiffEntry('method:POST!=GET')).toEqual({ action: 'changed', subject: 'POST!=GET' });
    });
});
