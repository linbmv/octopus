import { SettingKey } from '@/api/endpoints/setting';
import type { Setting } from '@/api/contracts';

export const HEALTH_PRESETS = ['off', 'shadow', 'balanced', 'aggressive'] as const;
export type HealthPreset = typeof HEALTH_PRESETS[number];

type HealthModeSettingKey =
    | typeof SettingKey.SmartHealthEnabled
    | typeof SettingKey.HealthWeightedBalancerEnabled
    | typeof SettingKey.HealthShadowMode;

export const HEALTH_PRESET_VALUES: Record<HealthPreset, Record<HealthModeSettingKey, string>> = {
    off: {
        [SettingKey.SmartHealthEnabled]: 'false',
        [SettingKey.HealthWeightedBalancerEnabled]: 'false',
        [SettingKey.HealthShadowMode]: 'false',
    },
    shadow: {
        [SettingKey.SmartHealthEnabled]: 'true',
        [SettingKey.HealthWeightedBalancerEnabled]: 'false',
        [SettingKey.HealthShadowMode]: 'true',
    },
    balanced: {
        [SettingKey.SmartHealthEnabled]: 'true',
        [SettingKey.HealthWeightedBalancerEnabled]: 'false',
        [SettingKey.HealthShadowMode]: 'false',
    },
    aggressive: {
        [SettingKey.SmartHealthEnabled]: 'true',
        [SettingKey.HealthWeightedBalancerEnabled]: 'true',
        [SettingKey.HealthShadowMode]: 'false',
    },
};

function valuesByKey(settings: Pick<Setting, 'key' | 'value'>[]): Map<string, string> {
    return new Map(settings.map((setting) => [setting.key, setting.value]));
}

export function detectHealthPreset(settings: Pick<Setting, 'key' | 'value'>[]): HealthPreset {
    const values = valuesByKey(settings);
    if (values.get(SettingKey.SmartHealthEnabled) !== 'true') return 'off';
    if (values.get(SettingKey.HealthShadowMode) === 'true') return 'shadow';
    if (values.get(SettingKey.HealthWeightedBalancerEnabled) === 'true') return 'aggressive';
    return 'balanced';
}

export function healthPresetChanges(
    preset: HealthPreset,
    settings: Pick<Setting, 'key' | 'value'>[],
): Setting[] {
    const values = valuesByKey(settings);
    return Object.entries(HEALTH_PRESET_VALUES[preset])
        .filter(([key, value]) => values.get(key) !== value)
        .map(([key, value]) => ({ key, value }));
}
