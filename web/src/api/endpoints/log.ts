import type { InfiniteData } from '@tanstack/react-query';
import { useInfiniteQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { apiClient, API_BASE_URL } from '../client';
import { logger } from '@/lib/logger';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { RelayLog as ContractRelayLog } from '../contracts';

export type { AttemptStatus, ChannelAttempt } from '../contracts';
export type RelayLog = Omit<ContractRelayLog, 'id'> & { id: string };

export interface RelayLogCursorPage {
    items: RelayLog[];
    next_cursor?: string;
    has_more: boolean;
}

/**
 * 日志列表查询参数
 */
export interface LogListParams {
    page?: number;
    page_size?: number;
    start_time?: number;
    end_time?: number;
}

/**
 * 清空日志 Hook
 * 
 * @example
 * const clearLogs = useClearLogs();
 * 
 * clearLogs.mutate();
 */
export function useClearLogs() {
    const queryClient = useQueryClient();

    return useMutation({
        mutationFn: async () => {
            return apiClient.delete<null>('/api/v1/log/clear');
        },
        onSuccess: () => {
            logger.log('日志清空成功');
            queryClient.invalidateQueries({ queryKey: ['logs'] });
        },
        onError: (error) => {
            logger.error('日志清空失败:', error);
        },
    });
}

const logsInfiniteQueryKey = (pageSize: number) => ['logs', 'infinite', pageSize] as const;
export const relayLogStoreLimit = 500;

export function relayLogInfiniteDataSize<TPageParam>(data: InfiniteData<RelayLogCursorPage, TPageParam> | undefined): number {
    if (!data) return 0;
    const ids = new Set<string>();
    for (const page of data.pages) {
        for (const log of page.items) ids.add(log.id);
    }
    return ids.size;
}

/**
 * Bounds the actual React Query InfiniteData store, preserving page/cursor
 * order and removing cache/DB overlap. Once the newest `limit` unique logs are
 * retained, the final page is terminal so historical loading cannot silently
 * replace recent observability data.
 */
export function trimRelayLogInfiniteData<TPageParam>(
    data: InfiniteData<RelayLogCursorPage, TPageParam> | undefined,
    limit = relayLogStoreLimit,
): InfiniteData<RelayLogCursorPage, TPageParam> | undefined {
    if (!data) return data;
    if (data.pages.length === 0) return data;
    const boundedLimit = Number.isFinite(limit) ? Math.max(1, Math.floor(limit)) : relayLogStoreLimit;
    const seen = new Set<string>();
    const pages: RelayLogCursorPage[] = [];
    const pageParams: TPageParam[] = [];
    let reachedLimit = false;

    for (let pageIndex = 0; pageIndex < data.pages.length && !reachedLimit; pageIndex += 1) {
        const page = data.pages[pageIndex];
        const items: RelayLog[] = [];
        for (const log of page.items) {
            if (seen.has(log.id)) continue;
            seen.add(log.id);
            items.push(log);
            if (seen.size >= boundedLimit) {
                reachedLimit = true;
                break;
            }
        }
        if (items.length > 0 || pages.length === 0) {
            pages.push({ ...page, items });
            pageParams.push(data.pageParams[pageIndex]);
        }
    }

    if (reachedLimit) {
        const lastIndex = pages.length - 1;
        pages[lastIndex] = { ...pages[lastIndex], has_more: false, next_cursor: undefined };
    }
    return { pages, pageParams };
}

export function canLoadMoreRelayLogs<TPageParam>(
    data: InfiniteData<RelayLogCursorPage, TPageParam> | undefined,
    hasNextPage: boolean,
    limit = relayLogStoreLimit,
): boolean {
    return hasNextPage && relayLogInfiniteDataSize(data) < limit;
}

/**
 * 日志管理 Hook
 * 整合初始加载、SSE 实时推送、滚动加载更多
 * 
 * @example
 * const { logs, isConnected, hasMore, isLoadingMore, loadMore, clear } = useLogs();
 * 
 * // logs 自动包含历史日志和实时日志，按时间倒序
 * logs.forEach(log => console.log(log.request_model_name));
 * 
 * // 滚动到底部时加载更多
 * if (hasMore && !isLoadingMore) loadMore();
 */
export function useLogs(options: { pageSize?: number } = {}) {
    const { pageSize = 20 } = options;

    const [isConnected, setIsConnected] = useState(false);
    const [error, setError] = useState<Error | null>(null);
    const eventSourceRef = useRef<EventSource | null>(null);

    const queryClient = useQueryClient();

    const logsQuery = useInfiniteQuery({
        queryKey: logsInfiniteQueryKey(pageSize),
        initialPageParam: '0',
        queryFn: async ({ pageParam }) => {
            const params = new URLSearchParams();
            params.set('cursor', pageParam);
            params.set('page_size', String(pageSize));
            return apiClient.get<RelayLogCursorPage>(`/api/v1/log/list?${params.toString()}`);
        },
        getNextPageParam: (lastPage, allPages) => {
            const loaded = relayLogInfiniteDataSize({ pages: allPages, pageParams: allPages.map(() => '0') });
            if (loaded >= relayLogStoreLimit) return undefined;
            if (!lastPage.has_more || !lastPage.next_cursor) return undefined;
            return lastPage.next_cursor;
        },
        staleTime: Infinity,
        refetchOnMount: 'always',
    });

    const logs = useMemo(() => {
        const pages = logsQuery.data?.pages ?? [];
        const seen = new Set<string>();
        const merged: RelayLog[] = [];

        for (const page of pages) {
            for (const log of page.items) {
                if (seen.has(log.id)) continue;
                seen.add(log.id);
                merged.push(log);
            }
        }

        merged.sort((a, b) => {
            if (a.time === b.time) return compareDecimalIDs(b.id, a.id);
            return b.time - a.time;
        });
        return merged;
    }, [logsQuery.data]);

    const lastEventIDRef = useRef('0');
    useEffect(() => {
        const newestID = logs[0]?.id;
        if (newestID && compareDecimalIDs(newestID, lastEventIDRef.current) > 0) {
            lastEventIDRef.current = newestID;
        }
    }, [logs]);

    const loadMore = useCallback(async () => {
        if (!canLoadMoreRelayLogs(logsQuery.data, !!logsQuery.hasNextPage)) return;
        if (logsQuery.isFetchingNextPage) return;

        try {
            await logsQuery.fetchNextPage();
            queryClient.setQueryData(
                logsInfiniteQueryKey(pageSize),
                (old: InfiniteData<RelayLogCursorPage, string> | undefined) => trimRelayLogInfiniteData(old),
            );
        } catch (e) {
            logger.error('加载更多日志失败:', e);
        }
    }, [logsQuery, pageSize, queryClient]);

    useEffect(() => {
        let cancelled = false;
        let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
        let reconnectAttempt = 0;

        const scheduleReconnect = () => {
            if (cancelled) return;
            reconnectAttempt += 1;
            const delay = Math.min(30_000, 1000 * 2 ** (reconnectAttempt - 1));
            reconnectTimer = setTimeout(() => {
                reconnectTimer = null;
                connect();
            }, delay);
        };

        const connect = async () => {
            try {
                const { token } = await apiClient.get<{ token: string }>('/api/v1/log/stream-token');
                if (cancelled) return;

                eventSourceRef.current?.close();
                const streamParams = new URLSearchParams({ token });
                if (lastEventIDRef.current !== '0') {
                    streamParams.set('after', lastEventIDRef.current);
                }
                const eventSource = new EventSource(`${API_BASE_URL}/api/v1/log/stream?${streamParams.toString()}`);
                eventSourceRef.current = eventSource;

                eventSource.onopen = () => {
                    reconnectAttempt = 0;
                    setIsConnected(true);
                    setError(null);
                    void queryClient.invalidateQueries({ queryKey: logsInfiniteQueryKey(pageSize) });
                };

                eventSource.onmessage = (event) => {
                    try {
                        const log: RelayLog = JSON.parse(event.data);
                        const eventID = event.lastEventId || log.id;
                        if (eventID) lastEventIDRef.current = eventID;
                        queryClient.setQueryData(
                            logsInfiniteQueryKey(pageSize),
                            (old: InfiniteData<RelayLogCursorPage, string> | undefined) => {
                                if (!old) {
                                    return trimRelayLogInfiniteData({
                                        pages: [{ items: [log], has_more: false }],
                                        pageParams: ['0'],
                                    });
                                }

                                const exists = old.pages.some((p) => p.items.some((x) => x.id === log.id));
                                if (exists) return old;

                                const firstPage = old.pages[0] ?? { items: [], has_more: false };
                                const nextFirstPage = {
                                    ...firstPage,
                                    items: [log, ...firstPage.items],
                                };
                                return trimRelayLogInfiniteData({ ...old, pages: [nextFirstPage, ...old.pages.slice(1)] });
                            }
                        );
                    } catch (e) {
                        logger.error('解析日志数据失败:', e);
                    }
                };

                eventSource.addEventListener('gap', () => {
                    void queryClient.invalidateQueries({ queryKey: logsInfiniteQueryKey(pageSize) });
                });

                eventSource.onerror = () => {
                    if (cancelled) return;
                    setIsConnected(false);
                    setError(new Error('SSE 连接断开'));
                    eventSource.close();
                    if (eventSourceRef.current === eventSource) {
                        eventSourceRef.current = null;
                    }
                    scheduleReconnect();
                };
            } catch (e) {
                if (cancelled) return;
                setError(e instanceof Error ? e : new Error('获取 stream token 失败'));
                logger.error('获取 stream token 失败:', e);
                scheduleReconnect();
            }
        };

        connect();

        return () => {
            cancelled = true;
            if (reconnectTimer) {
                clearTimeout(reconnectTimer);
            }
            eventSourceRef.current?.close();
            eventSourceRef.current = null;
            setIsConnected(false);
        };
    }, [pageSize, queryClient]);

    const clear = useCallback(() => {
        queryClient.removeQueries({ queryKey: logsInfiniteQueryKey(pageSize) });
    }, [pageSize, queryClient]);

    return {
        logs,
        isConnected,
        error,
        hasMore: canLoadMoreRelayLogs(logsQuery.data, !!logsQuery.hasNextPage),
        isLoading: logsQuery.isLoading,
        isLoadingMore: logsQuery.isFetchingNextPage,
        loadMore,
        clear,
    };
}

export function compareDecimalIDs(left: string, right: string): number {
    const normalizedLeft = left.replace(/^0+(?=\d)/, '');
    const normalizedRight = right.replace(/^0+(?=\d)/, '');
    if (normalizedLeft.length !== normalizedRight.length) {
        return normalizedLeft.length - normalizedRight.length;
    }
    if (normalizedLeft === normalizedRight) return 0;
    return normalizedLeft > normalizedRight ? 1 : -1;
}
