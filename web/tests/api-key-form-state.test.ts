import { describe, expect, it } from 'vitest';
import {
    apiKeyToFormState,
    toggleAPIKeyModel,
} from '@/components/modules/setting/useAPIKeyFormState';

describe('API key form conversion', () => {
    it('converts persisted API key values without losing limits or model access', () => {
        const expireAt = Date.UTC(2026, 6, 16, 9, 5, 0) / 1000;
        expect(apiKeyToFormState({
            id: 7,
            api_key: 'secret',
            name: 'automation',
            enabled: false,
            expire_at: expireAt,
            max_cost: 12.5,
            supported_models: 'gpt,claude',
        })).toEqual({
            form: {
                name: 'automation',
                enabled: false,
                expire_at: expireAt,
                max_cost: 12.5,
                supported_models: 'gpt,claude',
            },
            maxCostInput: '12.5',
            expireTime: '09:05',
        });
    });

    it('provides safe create defaults and deterministic model toggling', () => {
        expect(apiKeyToFormState()).toEqual({
            form: { name: '', enabled: true, expire_at: undefined, max_cost: undefined, supported_models: undefined },
            maxCostInput: '',
            expireTime: '00:00',
        });
        expect(toggleAPIKeyModel(undefined, 'gpt')).toBe('gpt');
        expect(toggleAPIKeyModel('gpt,claude', 'gpt')).toBe('claude');
        expect(toggleAPIKeyModel('claude', 'gpt')).toBe('claude,gpt');
    });
});
