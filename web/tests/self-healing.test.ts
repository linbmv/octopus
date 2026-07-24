import { describe, expect, it } from 'vitest';
import type { SelfHealingStatus } from '@/api/endpoints/channel';
import {
    canGeneratePatch,
    containsSensitiveSelfHealingLeak,
    deriveSelfHealingState,
    formatShapeSummary,
    isCapacityRootCause,
} from '@/components/modules/channel/self-healing';

function status(partial: Partial<SelfHealingStatus>): SelfHealingStatus {
    return {
        global_enabled: true,
        capture_success_baselines: true,
        channel_enabled: true,
        channel_config_version: 1,
        worker_available: true,
        sessions: [],
        patches: [],
        baselines: [],
        ...partial,
    };
}

describe('self-healing presentation', () => {
    it('keeps capacity root causes from becoming config patches', () => {
        expect(isCapacityRootCause('capacity')).toBe(true);
        expect(isCapacityRootCause('rate_limit')).toBe(true);
        expect(isCapacityRootCause('auth')).toBe(true);
        expect(canGeneratePatch('capacity')).toBe(false);
        expect(canGeneratePatch('protocol_drift')).toBe(true);
        expect(canGeneratePatch('waf_or_client_fingerprint')).toBe(true);
    });

    it('derives display states from gates, sessions, and patches', () => {
        expect(deriveSelfHealingState(undefined)).toBe('disabled');
        expect(deriveSelfHealingState(status({ global_enabled: false }))).toBe('disabled');
        expect(deriveSelfHealingState(status({ baselines: [{ id: 1 } as never] }))).toBe('watching');
        expect(
            deriveSelfHealingState(
                status({
                    sessions: [{ status: 'running', root_cause: 'protocol_drift' } as never],
                }),
            ),
        ).toBe('diagnosing');
        expect(
            deriveSelfHealingState(
                status({
                    sessions: [{ status: 'completed', root_cause: 'capacity' } as never],
                }),
            ),
        ).toBe('capacity');
        expect(
            deriveSelfHealingState(
                status({
                    patches: [{ status: 'previewed' } as never],
                }),
            ),
        ).toBe('patch_ready');
        expect(
            deriveSelfHealingState(
                status({
                    patches: [{ status: 'rolled_back' } as never],
                }),
            ),
        ).toBe('rolled_back');
    });

    it('never treats secret-like payloads as safe UI content', () => {
        expect(containsSensitiveSelfHealingLeak({ authorization: 'Bearer sk-test' })).toBe(true);
        expect(containsSensitiveSelfHealingLeak({ channel_key: 'secret' })).toBe(true);
        expect(
            containsSensitiveSelfHealingLeak({
                request_shape: { header_names: ['user-agent', 'content-type'], body_keys: ['model', 'messages'] },
            }),
        ).toBe(false);
    });

    it('formats request shapes without inventing values', () => {
        expect(formatShapeSummary(undefined)).toBe('—');
        expect(
            formatShapeSummary({
                method: 'POST',
                url: 'https://provider.test/v1/messages',
                headers: { 'user-agent': 'claude-cli', 'anthropic-version': '2023-06-01' },
                body: { body_bytes: 128, top_level_keys: ['model', 'messages'] },
                shape_sha256: 'abc',
                rewrite: { raw_passthrough: false },
            }),
        ).toContain('POST https://provider.test/v1/messages');
    });
});
