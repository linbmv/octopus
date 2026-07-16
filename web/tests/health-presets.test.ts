import { describe, expect, it } from 'vitest';
import { SettingKey } from '@/api/endpoints/setting';
import {
    detectHealthPreset,
    healthPresetChanges,
    HEALTH_PRESET_VALUES,
} from '@/components/modules/setting/health-presets';

const setting = (key: string, value: string) => ({ key, value });

describe('health mode presets', () => {
    it('maps the legacy default settings to balanced mode', () => {
        expect(detectHealthPreset([
            setting(SettingKey.SmartHealthEnabled, 'true'),
            setting(SettingKey.HealthWeightedBalancerEnabled, 'false'),
            setting(SettingKey.HealthShadowMode, 'false'),
        ])).toBe('balanced');
    });

    it('keeps off, shadow, balanced, and aggressive mutually exclusive', () => {
        for (const [preset, values] of Object.entries(HEALTH_PRESET_VALUES)) {
            const settings = Object.entries(values).map(([key, value]) => setting(key, value));
            expect(detectHealthPreset(settings)).toBe(preset);
        }
    });

    it('returns only changed settings so advanced tuning values are preserved', () => {
        const current = [
            setting(SettingKey.SmartHealthEnabled, 'true'),
            setting(SettingKey.HealthWeightedBalancerEnabled, 'false'),
            setting(SettingKey.HealthShadowMode, 'false'),
            setting(SettingKey.HealthMinAdaptiveTimeout, '31'),
        ];
        expect(healthPresetChanges('aggressive', current)).toEqual([
            setting(SettingKey.HealthWeightedBalancerEnabled, 'true'),
        ]);
    });
});
