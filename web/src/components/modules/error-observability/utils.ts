import type { AttemptErrorLevel, StatsErrorLevelCounts } from '@/api/endpoints/stats';

export const ERROR_LEVEL_ORDER = ['key', 'channel', 'client'] as const satisfies readonly AttemptErrorLevel[];

export function errorLevelTotal(counts: StatsErrorLevelCounts): number {
    return ERROR_LEVEL_ORDER.reduce((total, level) => total + counts[level], 0);
}

export function errorLevelPercent(count: number, counts: StatsErrorLevelCounts): number {
    const total = errorLevelTotal(counts);
    return total === 0 ? 0 : (count / total) * 100;
}

export function formatTrendBucket(timestamp: number, locale?: string): string {
    return new Date(timestamp * 1000).toLocaleTimeString(locale, { hour: '2-digit', minute: '2-digit' });
}
