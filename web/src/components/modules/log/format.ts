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

const firstTokenTimeoutPattern = /\b(?:manual_first_token_timeout|auto_first_token_timeout|global_first_event_timeout|cold_start_first_event_timeout|non_stream_attempt_timeout|stream_first_event_budget|first_token_timeout):[^\s()]+\s*\((\d+)s\)/;

export function getFirstTokenTimeoutSeconds(error: string): number | null {
    const match = error.match(firstTokenTimeoutPattern);
    if (!match) return null;
    const seconds = Number(match[1]);
    return Number.isFinite(seconds) && seconds > 0 ? seconds : null;
}

export function formatFirstTokenMetric(
    firstTokenMs: number,
    error: string,
    labels: { observed: string; timeout: string; unavailable: string },
): string {
    const timeoutSeconds = getFirstTokenTimeoutSeconds(error);
    if (timeoutSeconds !== null) return `${labels.timeout} ≥${timeoutSeconds}s`;
    if (firstTokenMs > 0) return `${labels.observed} ${formatLogDuration(firstTokenMs)}`;
    return `${labels.observed} ${labels.unavailable}`;
}

export function shouldShowReasoningTokens(tokens: number): boolean {
    return Number.isFinite(tokens) && tokens > 0;
}
