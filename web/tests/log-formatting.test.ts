import { describe, expect, it } from 'vitest';
import { formatLogDuration, resolveIntlLocale } from '@/components/modules/log/format';

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
});
