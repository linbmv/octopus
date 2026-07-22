import { describe, expect, it } from 'vitest';
import { formatLogDuration, resolveIntlLocale, shouldShowReasoningTokens } from '@/components/modules/log/format';

describe('log display formatting', () => {
    it('maps application locale identifiers to valid Intl locales', () => {
        expect(resolveIntlLocale('zh_hans')).toBe('zh-CN');
        expect(resolveIntlLocale('zh_hant')).toBe('zh-TW');
        expect(resolveIntlLocale('en')).toBe('en');
    });

    it('formats millisecond and second durations consistently', () => {
        expect(formatLogDuration(999)).toBe('999ms');
        expect(formatLogDuration(1500)).toBe('1.50s');
    });

    it('shows reasoning metadata only for positive finite token counts', () => {
        expect(shouldShowReasoningTokens(12)).toBe(true);
        expect(shouldShowReasoningTokens(0)).toBe(false);
        expect(shouldShowReasoningTokens(-1)).toBe(false);
        expect(shouldShowReasoningTokens(Number.NaN)).toBe(false);
    });
});
