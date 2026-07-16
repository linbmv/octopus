import { useQuery } from '@tanstack/react-query';
import { apiClient } from '../client';
import { formatCount, formatMoney, formatTime } from '@/lib/utils';
import type {
    StatsAPIKey,
    StatsDaily,
    StatsErrorLevels,
    StatsHourly,
    StatsTotal,
} from '../contracts';

export type {
    AttemptErrorLevel,
    StatsAPIKey,
    StatsChannel,
    StatsDaily,
    StatsErrorLevelCounts,
    StatsErrorLevels,
    StatsErrorLevelTrendPoint,
    StatsHourly,
    StatsMetrics,
    StatsTotal,
} from '../contracts';

export const ERROR_LEVEL_STATS_DEFAULT_WINDOW_HOURS = 24;
export const ERROR_LEVEL_STATS_MAX_WINDOW_HOURS = 7 * 24;

export function buildStatsErrorLevelsPath(
    windowHours = ERROR_LEVEL_STATS_DEFAULT_WINDOW_HOURS,
    channelId?: number,
): string {
    if (!Number.isInteger(windowHours) || windowHours < 1 || windowHours > ERROR_LEVEL_STATS_MAX_WINDOW_HOURS) {
        throw new RangeError(`windowHours must be an integer between 1 and ${ERROR_LEVEL_STATS_MAX_WINDOW_HOURS}`);
    }
    if (channelId !== undefined && (!Number.isInteger(channelId) || channelId < 1)) {
        throw new RangeError('channelId must be a positive integer');
    }
    const params = new URLSearchParams({ window_hours: String(windowHours) });
    if (channelId !== undefined) params.set('channel_id', String(channelId));
    return `/api/v1/stats/error-levels?${params.toString()}`;
}

export interface StatsMetricsFormatted {
    input_token: ReturnType<typeof formatCount>;
    output_token: ReturnType<typeof formatCount>;
    input_cost: ReturnType<typeof formatMoney>;
    output_cost: ReturnType<typeof formatMoney>;
    wait_time: ReturnType<typeof formatTime>;
    request_success: ReturnType<typeof formatCount>;
    request_failed: ReturnType<typeof formatCount>;

    request_count: ReturnType<typeof formatCount>;
    total_token: ReturnType<typeof formatCount>;
    total_cost: ReturnType<typeof formatMoney>;
}

export interface StatsDailyFormatted extends StatsMetricsFormatted {
    date: string;
}

export type StatsTotalFormatted = StatsMetricsFormatted;

export interface StatsHourlyFormatted extends StatsMetricsFormatted {
    hour: number;
    date: string;
}

export interface StatsAPIKeyFormatted extends StatsMetricsFormatted {
    api_key_id: number;
}
/**
 * 获取今日统计数据 Hook
 */
export function useStatsToday() {
    return useQuery({
        queryKey: ['stats', 'today'],
        queryFn: async () => {
            return apiClient.get<StatsDaily>('/api/v1/stats/today');
        },
        refetchInterval: 30000,
        refetchOnMount: 'always',
    });
}

/**
 * 获取每日统计数据 Hook
 */
export function useStatsDaily() {
    return useQuery({
        queryKey: ['stats', 'daily'],
        queryFn: async () => {
            return apiClient.get<StatsDaily[]>('/api/v1/stats/daily');
        },
        select: (data) => data.map((item): StatsDailyFormatted => ({
            input_token: formatCount(item.input_token),
            output_token: formatCount(item.output_token),
            total_token: formatCount(item.input_token + item.output_token),
            input_cost: formatMoney(item.input_cost),
            output_cost: formatMoney(item.output_cost),
            total_cost: formatMoney(item.input_cost + item.output_cost),
            wait_time: formatTime(item.wait_time),
            request_success: formatCount(item.request_success),
            request_failed: formatCount(item.request_failed),
            request_count: formatCount(item.request_success + item.request_failed),
            date: item.date,
        })),
        refetchInterval: 3600000, // 1 小时
        refetchOnMount: 'always',
    });
}
/**
 * 获取总统计数据 Hook
 */
export function useStatsHourly() {
    return useQuery({
        queryKey: ['stats', 'hourly'],
        queryFn: async () => {
            return apiClient.get<StatsHourly[]>('/api/v1/stats/hourly');
        },
        select: (data) => data.map((item): StatsHourlyFormatted => ({
            hour: item.hour,
            date: item.date,
            input_token: formatCount(item.input_token),
            output_token: formatCount(item.output_token),
            total_token: formatCount(item.input_token + item.output_token),
            input_cost: formatMoney(item.input_cost),
            output_cost: formatMoney(item.output_cost),
            total_cost: formatMoney(item.input_cost + item.output_cost),
            wait_time: formatTime(item.wait_time),
            request_success: formatCount(item.request_success),
            request_failed: formatCount(item.request_failed),
            request_count: formatCount(item.request_success + item.request_failed),
        })),
        refetchInterval: 10000,// 10 秒
        refetchOnMount: 'always',
    });
}

export function useStatsTotal() {
    return useQuery({
        queryKey: ['stats', 'total'],
        queryFn: async () => {
            return apiClient.get<StatsTotal>('/api/v1/stats/total');
        },
        select: (data) => ({
            input_token: formatCount(data.input_token),
            output_token: formatCount(data.output_token),
            total_token: formatCount(data.input_token + data.output_token),
            input_cost: formatMoney(data.input_cost),
            output_cost: formatMoney(data.output_cost),
            total_cost: formatMoney(data.input_cost + data.output_cost),
            wait_time: formatTime(data.wait_time),
            request_success: formatCount(data.request_success),
            request_failed: formatCount(data.request_failed),
            request_count: formatCount(data.request_success + data.request_failed),
        }),
        refetchInterval: 10000,// 10 秒
        refetchOnMount: 'always',
    });
}

export function useStatsErrorLevels(channelId?: number, windowHours = ERROR_LEVEL_STATS_DEFAULT_WINDOW_HOURS) {
    return useQuery({
        queryKey: ['stats', 'error-levels', windowHours, channelId ?? 'all'],
        queryFn: async () => apiClient.get<StatsErrorLevels>(buildStatsErrorLevelsPath(windowHours, channelId)),
        refetchInterval: 30000,
        refetchOnMount: 'always',
    });
}



/**
 * 获取 API Key 统计数据列表 Hook
 */
export function useStatsAPIKey() {
    return useQuery({
        queryKey: ['stats', 'apikey'],
        queryFn: async () => {
            return apiClient.get<StatsAPIKey[]>('/api/v1/stats/apikey');
        },
        select: (data) => data.map((item): StatsAPIKeyFormatted => ({
            api_key_id: item.api_key_id,
            input_token: formatCount(item.input_token),
            output_token: formatCount(item.output_token),
            total_token: formatCount(item.input_token + item.output_token),
            input_cost: formatMoney(item.input_cost),
            output_cost: formatMoney(item.output_cost),
            total_cost: formatMoney(item.input_cost + item.output_cost),
            wait_time: formatTime(item.wait_time),
            request_success: formatCount(item.request_success),
            request_failed: formatCount(item.request_failed),
            request_count: formatCount(item.request_success + item.request_failed),
        })),
        refetchInterval: 30000,
        refetchOnMount: 'always',
    });
}
