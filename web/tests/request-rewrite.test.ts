import { describe, expect, it } from 'vitest';
import {
    isProtectedAuthenticationHeader,
    normalizeHeaderRules,
    normalizeJSONRewriteRules,
} from '@/components/modules/channel/request-rewrite';

describe('advanced channel request rewrite form', () => {
    it('normalizes ordered header actions without exposing authentication headers', () => {
        expect(isProtectedAuthenticationHeader(' Authorization ')).toBe(true);
        expect(isProtectedAuthenticationHeader('X-Custom-Api-Key')).toBe(true);
		expect(isProtectedAuthenticationHeader('X-Session-Token')).toBe(true);
        expect(isProtectedAuthenticationHeader('X-Trace-ID')).toBe(false);
        expect(normalizeHeaderRules([
            { action: ' APPEND ', header_key: ' X-Trace ', header_value: 'one' },
            { action: 'remove', header_key: 'X-Old', header_value: 'discarded' },
            { action: 'set', header_key: '   ', header_value: 'ignored' },
        ])).toEqual([
            { action: 'append', header_key: 'X-Trace', header_value: 'one' },
            { action: 'remove', header_key: 'X-Old', header_value: '' },
        ]);
    });

    it('keeps JSON override values encoded and clears remove values', () => {
        expect(normalizeJSONRewriteRules([
            { action: ' OVERRIDE ', path: ' /options/temperature ', value: ' 0.25 ' },
            { action: 'remove', path: '/messages/0/internal', value: 'true' },
            { action: 'remove', path: '  ', value: null },
        ])).toEqual([
            { action: 'override', path: '/options/temperature', value: '0.25' },
            { action: 'remove', path: '/messages/0/internal', value: null },
        ]);
    });
});
