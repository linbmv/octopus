export function resolveIntlLocale(locale: string): string {
    if (locale === 'zh_hans') return 'zh-CN';
    if (locale === 'zh_hant') return 'zh-TW';
    return locale;
}

export function formatLogTime(timestamp: number, locale = 'en'): string {
    return new Date(timestamp * 1000).toLocaleString(resolveIntlLocale(locale), {
        month: '2-digit',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
    });
}

export function formatLogDuration(ms: number): string {
    return ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(2)}s`;
}

export function shouldShowReasoningTokens(tokens: number): boolean {
    return Number.isFinite(tokens) && tokens > 0;
}
