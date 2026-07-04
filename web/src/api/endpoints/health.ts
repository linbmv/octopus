import { useQuery } from '@tanstack/react-query';
import { apiClient } from '../client';

export interface HealthTimeoutPolicy {
    source: string;
    min_timeout_ms: number;
    slow_model_profile: boolean;
    timeout_rate: number;
    timeout_rate_backoff: boolean;
}

export interface HealthStats {
    total_count: number;
    success_count: number;
    success_rate: number;
    timeout_count: number;
    auto_first_token_timeout_count: number;
    network_count: number;
    rate_limit_count: number;
    model_error_count: number;
    key_error_count: number;
    first_token_p50_ms: number;
    first_token_p95_ms: number;
    first_token_p99_ms: number;
    cv: number;
    consecutive_success: number;
    consecutive_failure: number;
    consecutive_timeout: number;
    last_event_at: string;
}

export interface HealthState {
    channel_id: number;
    key_id: number;
    model: string;
    score: number;
    stats: HealthStats;
    adaptive_timeout_ms: number;
    timeout_policy: HealthTimeoutPolicy;
}

export interface HealthStatusResponse {
    count: number;
    states: HealthState[];
}

export function useHealthStatus() {
    return useQuery({
        queryKey: ['health', 'status'],
        queryFn: () => apiClient.get<HealthStatusResponse>('/api/v1/health/status'),
        refetchInterval: 15000,
    });
}
