import { describe, expect, it } from 'vitest';
import { formatFirstTokenMetric, formatLogDuration, getFirstTokenTimeoutSeconds, resolveIntlLocale, shouldShowReasoningTokens } from '@/components/modules/log/format';

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

    it('does not render an unobserved first token as zero milliseconds', () => {
        const labels = { observed: 'TTFT', timeout: 'First-token timeout', unavailable: 'not observed' };
        expect(formatFirstTokenMetric(0, 'global_first_event_timeout:waiting_headers (120s)', labels)).toBe('First-token timeout ≥120s');
        expect(formatFirstTokenMetric(0, 'connection refused', labels)).toBe('TTFT not observed');
        expect(formatFirstTokenMetric(1500, '', labels)).toBe('TTFT 1.50s');
        expect(getFirstTokenTimeoutSeconds('channel failed: non_stream_attempt_timeout:waiting_headers (30s)')).toBe(30);
        expect(getFirstTokenTimeoutSeconds('connection refused')).toBeNull();
    });

    it('shows reasoning metadata only for positive finite token counts', () => {
        expect(shouldShowReasoningTokens(12)).toBe(true);
        expect(shouldShowReasoningTokens(0)).toBe(false);
        expect(shouldShowReasoningTokens(-1)).toBe(false);
        expect(shouldShowReasoningTokens(Number.NaN)).toBe(false);
    });
});
