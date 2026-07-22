import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiClient, API_BASE_URL, getAuthRequestHeaders } from '../client';
import { logger } from '@/lib/logger';
import type { DBImportConflictPolicy, DBImportResult, Setting } from '../contracts';

export type { DBImportConflictPolicy, DBImportResult, Setting } from '../contracts';

export const SettingKey = {
    ProxyURL: 'proxy_url',
    StatsSaveInterval: 'stats_save_interval',
    ModelInfoUpdateInterval: 'model_info_update_interval',
    SyncLLMInterval: 'sync_llm_interval',
    RelayLogKeepEnabled: 'relay_log_keep_enabled',
    RelayLogKeepPeriod: 'relay_log_keep_period',
    RelayLogContentMode: 'relay_log_content_mode',
    CORSAllowOrigins: 'cors_allow_origins',
    CircuitBreakerThreshold: 'circuit_breaker_threshold',
    CircuitBreakerCooldown: 'circuit_breaker_cooldown',
    CircuitBreakerMaxCooldown: 'circuit_breaker_max_cooldown',
    CircuitBreakerHalfOpenProbes: 'circuit_breaker_half_open_probes',
    CircuitBreakerProbeLease: 'circuit_breaker_probe_lease',
    SmartHealthEnabled: 'smart_health_enabled',
    HealthWeightedBalancerEnabled: 'health_weighted_balancer_enabled',
    HealthMinAdaptiveTimeout: 'health_min_adaptive_timeout',
    HealthSlowModelMinTimeout: 'health_slow_model_min_timeout',
    HealthRecoveryProbeEvery: 'health_recovery_probe_every',
    HealthRecoveryProbeInterval: 'health_recovery_probe_interval',
    HealthTimeoutRateThreshold: 'health_timeout_rate_threshold',
    HealthSlowModelKeywords: 'health_slow_model_keywords',
    HealthShadowMode: 'health_shadow_mode',
    HealthMaxMultiplierStack: 'health_max_multiplier_stack',
    StickyHealthyFirstTokenTimeout: 'sticky_healthy_first_token_timeout',
    ChannelCardPinnedIDs: 'channel_card_pinned_ids',
    GroupCardPinnedIDs: 'group_card_pinned_ids',
} as const;

export const RelayLogContentMode = {
    Metadata: 'metadata',
    Full: 'full',
    Disabled: 'disabled',
} as const;

export type RelayLogContentModeValue = typeof RelayLogContentMode[keyof typeof RelayLogContentMode];

/**
 * 获取 Setting 列表 Hook
 * 
 * @example
 * const { data: settings, isLoading, error } = useSettingList();
 * 
 * if (isLoading) return <Loading />;
 * if (error) return <Error message={error.message} />;
 * 
 * settings?.forEach(setting => console.log(setting.key, setting.value));
 */
export function useSettingList() {
    return useQuery({
        queryKey: ['settings', 'list'],
        queryFn: async () => {
            return apiClient.get<Setting[]>('/api/v1/setting/list');
        },
        refetchInterval: 30000,
        refetchOnMount: 'always',
    });
}

/**
 * 设置 Setting Hook
 * 
 * @example
 * const setSetting = useSetSetting();
 * 
 * setSetting.mutate({
 *   key: 'theme',
 *   value: 'dark',
 * });
 */
export function useSetSetting() {
    const queryClient = useQueryClient();

    return useMutation({
        mutationFn: async (data: Setting) => {
            return apiClient.post<Setting>('/api/v1/setting/set', data);
        },
        onSuccess: (data) => {
            logger.log('Setting 设置成功:', data);
            queryClient.setQueryData<Setting[]>(['settings', 'list'], (current = []) => {
                const existingIndex = current.findIndex((setting) => setting.key === data.key);
                if (existingIndex === -1) return [...current, data];
                return current.map((setting, index) => index === existingIndex ? data : setting);
            });
            queryClient.invalidateQueries({ queryKey: ['settings', 'list'] });
        },
        onError: (error) => {
            logger.error('Setting 设置失败:', error);
        },
    });
}

export interface DBExportOptions {
    include_logs?: boolean;
    include_stats?: boolean;
    password?: string;
}

export interface DBImportOptions {
    file: File;
    password?: string;
    dry_run?: boolean;
    conflict_policy?: DBImportConflictPolicy;
}

export class DBImportConflictResponseError extends Error {
    readonly result: DBImportResult;

    constructor(message: string, result: DBImportResult) {
        super(message);
        this.name = 'DBImportConflictResponseError';
        this.result = result;
    }
}

export const BACKUP_PASSWORD_HEADER = 'X-Octopus-Backup-Password';

type ApiResponse<T> = {
    code?: number;
    message?: string;
    data?: T;
};

function isRecord(value: unknown): value is Record<string, unknown> {
    return typeof value === 'object' && value !== null;
}

function getMessageField(value: unknown): string | undefined {
    if (!isRecord(value)) return undefined;
    const msg = value.message;
    if (typeof msg === 'string') return msg;
    const nestedError = value.error;
    if (!isRecord(nestedError)) return undefined;
    return typeof nestedError.message === 'string' ? nestedError.message : undefined;
}

function getDataField<T>(value: unknown): T | undefined {
    if (!isRecord(value)) return undefined;
    return (value as ApiResponse<T>).data;
}

export function addBackupPasswordHeader(headers: Headers, password?: string): Headers {
    if (password) headers.set(BACKUP_PASSWORD_HEADER, password);
    return headers;
}

export function buildDBExportSearchParams(options: DBExportOptions): URLSearchParams {
    const params = new URLSearchParams();
    params.set('include_logs', String(!!options.include_logs));
    params.set('include_stats', String(!!options.include_stats));
    return params;
}

export function buildDBImportSearchParams(options: Pick<DBImportOptions, 'dry_run' | 'conflict_policy'>): URLSearchParams {
    const params = new URLSearchParams();
    params.set('dry_run', String(!!options.dry_run));
    params.set('conflict_policy', options.conflict_policy ?? 'reject');
    return params;
}

function getImportConflictResult(value: unknown): DBImportResult | undefined {
    if (!isRecord(value) || !isRecord(value.error) || !isRecord(value.error.details)) return undefined;
    const result = value.error.details.result;
    return isRecord(result) ? result as unknown as DBImportResult : undefined;
}

function parseFilename(contentDisposition: string | null): string | null {
    if (!contentDisposition) return null;
    // e.g. attachment; filename="octopus-export-20250101120000.json"
    const match = contentDisposition.match(/filename="([^"]+)"/i);
    return match?.[1] ?? null;
}

function exportFallbackFilename(encrypted: boolean) {
    const d = new Date();
    const pad = (n: number) => String(n).padStart(2, '0');
    const ts = `${d.getFullYear()}${pad(d.getMonth() + 1)}${pad(d.getDate())}${pad(d.getHours())}${pad(d.getMinutes())}${pad(d.getSeconds())}`;
    return `octopus-export-${ts}${encrypted ? '.octopus-backup' : '.json'}`;
}

async function downloadBlob(blob: Blob, filename: string) {
    const url = URL.createObjectURL(blob);
    try {
        const a = document.createElement('a');
        a.href = url;
        a.download = filename;
        document.body.appendChild(a);
        a.click();
        a.remove();
    } finally {
        URL.revokeObjectURL(url);
    }
}

/**
 * 导出数据库（下载明文 JSON 或加密备份文件）
 */
export function useExportDB() {
    return useMutation({
        mutationFn: async (options: DBExportOptions = {}) => {
            const params = buildDBExportSearchParams(options);

            const headers = addBackupPasswordHeader(getAuthRequestHeaders('GET'), options.password);

            const res = await fetch(`${API_BASE_URL}/api/v1/setting/export?${params.toString()}`, {
                method: 'GET',
                headers,
                credentials: 'same-origin',
            });

            if (!res.ok) {
                const text = await res.text();
                throw new Error(text || res.statusText);
            }

            const blob = await res.blob();
            const filename = parseFilename(res.headers.get('content-disposition')) || exportFallbackFilename(!!options.password);
            await downloadBlob(blob, filename);
            return { filename };
        },
        onError: (error) => {
            logger.error('导出数据库失败:', error);
        },
    });
}

/**
 * 恢复数据库（上传明文 JSON 或加密备份；v1 仅允许恢复到空业务库）
 */
export function useImportDB() {
    return useMutation({
        mutationFn: async ({ file, password, dry_run, conflict_policy }: DBImportOptions) => {
            const form = new FormData();
            form.append('file', file);

            const headers = addBackupPasswordHeader(getAuthRequestHeaders('POST'), password);
			const params = buildDBImportSearchParams({ dry_run, conflict_policy });

            const res = await fetch(`${API_BASE_URL}/api/v1/setting/import?${params.toString()}`, {
                method: 'POST',
                headers,
                body: form,
                credentials: 'same-origin',
            });

            const contentType = res.headers.get('content-type') || '';
            const isJson = contentType.includes('application/json');
            const data = isJson ? await res.json() : await res.text();

            if (!res.ok) {
                const message = getMessageField(data) ?? (typeof data === 'string' ? data : res.statusText);
				const conflictResult = getImportConflictResult(data);
				if (res.status === 409 && conflictResult) {
					throw new DBImportConflictResponseError(message, conflictResult);
				}
                throw new Error(message);
            }

            // 支持后端标准 ApiResponse：{code,message,data:{...}}
            const nested = getDataField<DBImportResult>(data);
            return nested ?? (data as DBImportResult);
        },
        onError: (error) => {
            logger.error('导入数据库失败:', error);
        },
    });
}
