import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiClient } from '../client';
import { logger } from '@/lib/logger';
import { formatStatsMetrics, StatsChannel, type StatsMetricsFormatted } from './stats';
import type {
    BaseUrl,
    Channel as ContractChannel,
    ChannelCircuitStatus,
    ChannelKey,
    CustomHeader,
	HeaderRule,
	JSONRewriteRule,
} from '../contracts';
export type { BaseUrl, ChannelCircuitStatus, ChannelKey, CustomHeader, HeaderRule, JSONRewriteRule } from '../contracts';

export type ChannelRuntimeURLStatus = {
    url: string;
    rank: number;
    known: boolean;
    latency_ms?: number;
    cooldown_remaining_seconds?: number;
    cooled: boolean;
    selection_reason: string;
};
/**
 * 渠道类型枚举
 */
export enum ChannelType {
    OpenAIChat = 'openai/chat_completions',
    OpenAIResponse = 'openai/responses',
    Anthropic = 'anthropic/messages',
    Gemini = 'gemini/contents',
    Volcengine = 'doubao',
    OpenAIEmbedding = 'openai/embeddings',
}

/**
 * 自动分组类型枚举
 */
export enum AutoGroupType {
    None = 0,   // 不自动分组
    Fuzzy = 1,  // 模糊匹配
    Exact = 2,  // 准确匹配
    Regex = 3,  // 正则匹配
}

export const ChannelPolicyProfile = {
    Standard: 'standard',
    Official: 'official',
    TrustedProxy: 'trusted_proxy',
    UntrustedProxy: 'untrusted_proxy',
} as const;

export type ChannelPolicyProfile = ContractChannel['policy_profile'];

/**
 * 渠道完整数据（与后端 model.Channel 对齐；数组字段在前端保证为 []）
 */
export type Channel = Omit<ContractChannel, 'auto_group' | 'stats' | 'type'> & {
    auto_group: AutoGroupType;
    stats: StatsChannel;
    type: ChannelType;
};

// Internal type: backend may return null for slice fields; normalize to [] in select()
type ChannelServer = Omit<Channel, 'base_urls' | 'custom_header' | 'header_rules' | 'json_rewrite_rules' | 'keys'> & {
    base_urls: BaseUrl[] | null;
    custom_header: CustomHeader[] | null;
	header_rules: HeaderRule[] | null;
	json_rewrite_rules: JSONRewriteRule[] | null;
    keys: ChannelKey[] | null;
};

/**
 * 创建渠道请求：必填字段 + 可选字段
 */
export type CreateChannelRequest = {
    name: string;
    type: ChannelType;
    enabled?: boolean;
    base_urls: BaseUrl[];
    keys: Array<Pick<ChannelKey, 'enabled' | 'channel_key' | 'remark'>>;
    model: string;
    custom_model?: string;
    proxy?: boolean;
    auto_sync?: boolean;
    auto_group?: AutoGroupType;
    custom_header?: CustomHeader[];
	header_rules?: HeaderRule[];
	json_rewrite_rules?: JSONRewriteRule[];
    channel_proxy?: string | null;
    param_override?: string | null;
    match_regex?: string | null;
    raw_passthrough?: boolean;
    rpm_limit?: number;
    max_concurrency?: number;
    user_agent?: string;
    policy_profile?: ChannelPolicyProfile;
};

/**
 * 更新渠道请求：id + 可选字段 + keys diff
 */
export type UpdateChannelRequest = {
    id: number;
    name?: string;
    type?: ChannelType;
    enabled?: boolean;
    base_urls?: BaseUrl[];
    model?: string;
    custom_model?: string;
    proxy?: boolean;
    auto_sync?: boolean;
    auto_group?: AutoGroupType;
    custom_header?: CustomHeader[];
	header_rules?: HeaderRule[];
	json_rewrite_rules?: JSONRewriteRule[];
    channel_proxy?: string | null;
    param_override?: string | null;
    match_regex?: string | null;
    raw_passthrough?: boolean;
    rpm_limit?: number;
    max_concurrency?: number;
    user_agent?: string;
    policy_profile?: ChannelPolicyProfile;
    // keys diff
    keys_to_add?: Array<Pick<ChannelKey, 'enabled' | 'channel_key' | 'remark'>>;
    keys_to_update?: Array<{ id: number; enabled?: boolean; channel_key?: string; remark?: string }>;
    keys_to_delete?: number[];
};

export type FetchModelRequest = {
    type: ChannelType;
    base_urls: BaseUrl[];
    keys: Array<Pick<ChannelKey, 'enabled' | 'channel_key'>>;
    proxy?: boolean;
    channel_proxy?: string | null;
    match_regex?: string | null;
    custom_header?: CustomHeader[];
};

export type CapabilityKind = 'text' | 'stream' | 'tool' | 'vision';
export type CapabilityStatus = 'supported' | 'unsupported' | 'unauthorized' | 'not_implemented' | 'transient';

export type CapabilityEvidence = {
    id: number;
    channel_id: number;
    channel_key_id: number;
    model: string;
    wire_protocol: string;
    capability: CapabilityKind;
    endpoint?: string;
    status: CapabilityStatus;
    error_class?: string;
    error_message?: string;
    http_status?: number;
    source: string;
    probed_at: string;
    expires_at: string;
    key_remark?: string;
    key_enabled: boolean;
    fresh: boolean;
};

export type CapabilityProbeReport = {
    requested: number;
    accepted: number;
    coalesced: number;
    dropped: number;
    budget_rejected: number;
    reserved_cost_usd: number;
    total_reserved_usd: number;
    remaining_cost_usd: number;
    queue: {
		accepted: number;
		coalesced: number;
		dropped: number;
		failures: number;
		queue_depth: number;
		queue_limit: number;
		concurrency: number;
    };
};

export type ProbeCapabilitiesRequest = {
    models?: string[];
    capabilities?: CapabilityKind[];
    max_cost_usd?: number;
};

/**
 * 获取渠道列表 Hook
 * 
 * @example
 * const { data: channels, isLoading, error } = useChannelList();
 * 
 * if (isLoading) return <Loading />;
 * if (error) return <Error message={error.message} />;
 * 
 * channels?.forEach(channel => console.log(channel.raw.name));
 */
export function useChannelList(options?: { enabled?: boolean }) {
    return useQuery({
        queryKey: ['channels', 'list'],
        queryFn: async () => {
            return apiClient.get<ChannelServer[]>('/api/v1/channel/list');
        },
        select: (data) => data.map((item) => ({
            raw: ({
                ...item,
                base_urls: item.base_urls ?? [],
                custom_header: item.custom_header ?? [],
				header_rules: item.header_rules ?? [],
				json_rewrite_rules: item.json_rewrite_rules ?? [],
                keys: item.keys ?? [],
            }) satisfies Channel,
            formatted: formatStatsMetrics(item.stats)
        })) as Array<{ raw: Channel; formatted: StatsMetricsFormatted }>,
        enabled: options?.enabled ?? true,
        refetchInterval: 30000,
        refetchOnMount: 'always',
    });
}

/**
 * 创建渠道 Hook
 * 
 * @example
 * const createChannel = useCreateChannel();
 * 
 * createChannel.mutate({
 *   name: 'OpenAI',
 *   type: ChannelType.OpenAIChat,
 *   base_urls: [{ url: 'https://api.openai.com', delay: 0 }],
 *   keys: [{ enabled: true, channel_key: 'sk-xxx' }],
 *   model: 'gpt-4',
 * });
 */
export function useCreateChannel() {
    const queryClient = useQueryClient();

    return useMutation({
        mutationFn: async (data: CreateChannelRequest) => {
            return apiClient.post<ChannelServer>('/api/v1/channel/create', data);
        },
        onSuccess: (data) => {
            logger.log('渠道创建成功:', data);
            queryClient.invalidateQueries({ queryKey: ['channels', 'list'] });
            queryClient.invalidateQueries({ queryKey: ['models', 'list'] });
            queryClient.invalidateQueries({ queryKey: ['models', 'channel'] });
        },
        onError: (error) => {
            logger.error('渠道创建失败:', error);
        },
    });
}

/**
 * 更新渠道 Hook
 * 
 * @example
 * const updateChannel = useUpdateChannel();
 * 
 * updateChannel.mutate({
 *   id: 1,
 *   name: 'OpenAI Updated',
 *   type: ChannelType.OpenAIChat,
 *   enabled: true,
 *   base_urls: [{ url: 'https://api.openai.com', delay: 0 }],
 *   keys_to_add: [{ enabled: true, channel_key: 'sk-xxx' }],
 *   model: 'gpt-4-turbo',
 *   proxy: false,
 * });
 */
export function useUpdateChannel() {
    const queryClient = useQueryClient();

    return useMutation({
        mutationFn: async (data: UpdateChannelRequest) => {
            return apiClient.post<ChannelServer>('/api/v1/channel/update', data);
        },
        onSuccess: (data) => {
            logger.log('渠道更新成功:', data);
            queryClient.invalidateQueries({ queryKey: ['channels', 'list'] });
            queryClient.invalidateQueries({ queryKey: ['models', 'channel'] });
            queryClient.invalidateQueries({ queryKey: ['groups', 'list'] });
        },
        onError: (error) => {
            logger.error('渠道更新失败:', error);
        },
    });
}

/**
 * 删除渠道 Hook
 * 
 * @example
 * const deleteChannel = useDeleteChannel();
 * 
 * deleteChannel.mutate(1); // 删除 ID 为 1 的渠道
 */
export function useDeleteChannel() {
    const queryClient = useQueryClient();

    return useMutation({
        mutationFn: async (id: number) => {
            return apiClient.delete<null>(`/api/v1/channel/delete/${id}`);
        },
        onSuccess: () => {
            logger.log('渠道删除成功');
            queryClient.invalidateQueries({ queryKey: ['channels', 'list'] });
            queryClient.invalidateQueries({ queryKey: ['models', 'channel'] });
            queryClient.invalidateQueries({ queryKey: ['groups', 'list'] });
        },
        onError: (error) => {
            logger.error('渠道删除失败:', error);
        },
    });
}

/**
 * 启用/禁用渠道 Hook
 * 
 * @example
 * const enableChannel = useEnableChannel();
 * 
 * enableChannel.mutate({ id: 1, enabled: true }); // 启用 ID 为 1 的渠道
 * enableChannel.mutate({ id: 1, enabled: false }); // 禁用 ID 为 1 的渠道
 */
export function useEnableChannel() {
    const queryClient = useQueryClient();

    return useMutation({
        mutationFn: async (data: { id: number; enabled: boolean }) => {
            return apiClient.post<null>('/api/v1/channel/enable', data);
        },
        onSuccess: () => {
            logger.log('渠道状态更新成功');
            queryClient.invalidateQueries({ queryKey: ['channels', 'list'] });
            queryClient.invalidateQueries({ queryKey: ['models', 'channel'] });
            queryClient.invalidateQueries({ queryKey: ['groups', 'list'] });
        },
        onError: (error) => {
            logger.error('渠道状态更新失败:', error);
        },
    });
}

/**
 * 获取渠道模型列表 Hook
 * 
 * @example
 * const fetchModel = useFetchModel();
 * 
 * fetchModel.mutate({
 *   type: ChannelType.OpenAIChat,
 *   base_urls: [{ url: 'https://api.openai.com', delay: 0 }],
 *   keys: [{ enabled: true, channel_key: 'sk-xxx' }],
 *   proxy: false,
 * });
 * 
 * // 在 onSuccess 中获取模型列表
 * fetchModel.data // ['gpt-4', 'gpt-3.5-turbo', ...]
 */
export function useFetchModel() {
    return useMutation({
        mutationFn: async (data: FetchModelRequest) => {
            return apiClient.post<string[]>('/api/v1/channel/fetch-model', data);
        },
        onSuccess: (data) => {
            logger.log('模型列表获取成功:', data);
        },
        onError: (error) => {
            logger.error('模型列表获取失败:', error);
        },
    });
}

/**
 * 获取渠道最后同步时间 Hook
 * 
 * @example
 * const lastSyncTime = useLastSyncTime();
 * 
 * if (lastSyncTime) {
 *   console.log('最后同步时间:', new Date(lastSyncTime).toLocaleString());
 * }
 */
export function useLastSyncTime() {
    return useQuery({
        queryKey: ['channels', 'last-sync-time'],
        queryFn: async () => {
            return apiClient.get<string>('/api/v1/channel/last-sync-time');
        },
        refetchInterval: 30000,
    });
}
/**
 * 同步渠道 Hook
 * 
 * @example
 * const syncChannel = useSyncChannel();
 * 
 * syncChannel.mutate();
 */
export function useSyncChannel() {
    const queryClient = useQueryClient();
    return useMutation({
        mutationFn: async () => {
            return apiClient.post<null>('/api/v1/channel/sync');
        },
        onSuccess: () => {
            logger.log('渠道同步成功');
            queryClient.invalidateQueries({ queryKey: ['channels', 'last-sync-time'] });
        },
        onError: (error) => {
            logger.error('渠道同步失败:', error);
        },
    });
}

export function useChannelCapabilities(channelId: number) {
    return useQuery({
        queryKey: ['channels', channelId, 'capabilities'],
        queryFn: async () => apiClient.get<CapabilityEvidence[]>(`/api/v1/channel/${channelId}/capabilities`),
        enabled: channelId > 0,
        refetchInterval: 5000,
    });
}

export function useProbeChannelCapabilities(channelId: number) {
    const queryClient = useQueryClient();
    return useMutation({
        mutationFn: async (request: ProbeCapabilitiesRequest = {}) => apiClient.post<CapabilityProbeReport>(`/api/v1/channel/${channelId}/capabilities/probe`, request),
        onSuccess: () => {
            queryClient.invalidateQueries({ queryKey: ['channels', channelId, 'capabilities'] });
        },
        onError: (error) => {
            logger.error('渠道能力探测失败:', error);
        },
    });
}

/**
 * 渠道熔断状态 Hook：返回当前被冻结（open/half_open）的 key×模型条目及剩余冷却。
 */
export function useChannelCircuit(channelId: number, enabled: boolean = true) {
    return useQuery({
        queryKey: ['channels', channelId, 'circuit'],
        queryFn: async () => apiClient.get<ChannelCircuitStatus[]>(`/api/v1/channel/${channelId}/circuit`),
        enabled: enabled && channelId > 0,
        refetchInterval: 5000,
    });
}

export function useChannelRuntimeURLs(channelId: number, enabled: boolean = true) {
    return useQuery({
        queryKey: ['channels', channelId, 'runtime-urls'],
        queryFn: async () => apiClient.get<ChannelRuntimeURLStatus[]>(`/api/v1/channel/${channelId}/runtime-urls`),
        enabled: enabled && channelId > 0,
        staleTime: 5000,
        refetchInterval: 10000,
    });
}

/**
 * 手动清除渠道熔断 Hook：被冻结的模型无需等待冷却即可重新投入使用。
 * 可选 model 参数精确到单个模型；缺省清除整个渠道。
 */
export function useResetChannelCircuit(channelId: number) {
    const queryClient = useQueryClient();
    return useMutation({
        mutationFn: async (model?: string) => {
            return apiClient.post<null>('/api/v1/channel/reset-circuit', {
                id: channelId,
                ...(model ? { model } : {}),
            });
        },
        onSuccess: () => {
            queryClient.invalidateQueries({ queryKey: ['channels', channelId, 'circuit'] });
        },
        onError: (error) => {
            logger.error('清除渠道熔断失败:', error);
        },
    });
}
