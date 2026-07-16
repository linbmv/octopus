'use client';

import { useEffect, useMemo, useState } from 'react';
import { Activity, ChevronDown, HelpCircle, SlidersHorizontal } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { toast } from '@/components/common/Toast';
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/animate-ui/components/animate/tooltip';
import { Input } from '@/components/ui/input';
import { Switch } from '@/components/ui/switch';
import { SettingKey, useSetSetting, useSettingList } from '@/api/endpoints/setting';
import {
    detectHealthPreset,
    healthPresetChanges,
    HEALTH_PRESETS,
    type HealthPreset,
} from './health-presets';

const advancedFields = [
    { key: SettingKey.HealthMinAdaptiveTimeout, label: 'minAdaptiveTimeout', type: 'number' },
    { key: SettingKey.HealthSlowModelMinTimeout, label: 'slowModelMinTimeout', type: 'number' },
    { key: SettingKey.HealthRecoveryProbeEvery, label: 'recoveryProbeEvery', type: 'number' },
    { key: SettingKey.HealthRecoveryProbeInterval, label: 'recoveryProbeInterval', type: 'number' },
    { key: SettingKey.HealthTimeoutRateThreshold, label: 'timeoutRateThreshold', type: 'number' },
    { key: SettingKey.HealthMaxMultiplierStack, label: 'maxMultiplierStack', type: 'number' },
    { key: SettingKey.StickyHealthyFirstTokenTimeout, label: 'stickyHealthyTimeout', type: 'number' },
    { key: SettingKey.HealthSlowModelKeywords, label: 'slowModelKeywords', type: 'text' },
] as const;

export function SettingHealth() {
    const t = useTranslations('setting');
    const { data: settings = [] } = useSettingList();
    const setSetting = useSetSetting();
    const [advancedOpen, setAdvancedOpen] = useState(false);
    const [drafts, setDrafts] = useState<Record<string, string>>({});
    const values = useMemo(() => new Map(settings.map((setting) => [setting.key, setting.value])), [settings]);
    const preset = detectHealthPreset(settings);

    useEffect(() => {
        const next = Object.fromEntries(advancedFields.map((field) => [field.key, values.get(field.key) ?? '']));
        queueMicrotask(() => setDrafts(next));
    }, [values]);

    const save = async (key: string, value: string) => {
        if (values.get(key) === value) return;
        try {
            await setSetting.mutateAsync({ key, value });
            toast.success(t('saved'));
        } catch (error) {
            toast.error(error instanceof Error ? error.message : String(error));
        }
    };

    const selectPreset = async (nextPreset: HealthPreset) => {
        const changes = healthPresetChanges(nextPreset, settings);
        if (changes.length === 0) return;
        try {
            await Promise.all(changes.map((change) => setSetting.mutateAsync(change)));
            toast.success(t('saved'));
        } catch (error) {
            toast.error(error instanceof Error ? error.message : String(error));
        }
    };

    const smartEnabled = values.get(SettingKey.SmartHealthEnabled) === 'true';
    return (
        <div className="space-y-5 rounded-3xl border border-border bg-card p-6">
            <header>
                <h2 className="flex items-center gap-2 text-lg font-bold text-card-foreground">
                    <Activity className="size-5" />
                    {t('health.title')}
                </h2>
                <p className="mt-1 text-xs text-muted-foreground">{t('health.description')}</p>
            </header>

            <div className="grid grid-cols-2 gap-2">
                {HEALTH_PRESETS.map((mode) => (
                    <button
                        key={mode}
                        type="button"
                        onClick={() => void selectPreset(mode)}
                        disabled={setSetting.isPending}
                        aria-pressed={preset === mode}
                        className={`rounded-2xl border p-3 text-left transition-colors ${preset === mode ? 'border-primary bg-primary/10' : 'border-border bg-background/40 hover:bg-muted/40'}`}
                    >
                        <span className="text-sm font-semibold">{t(`health.presets.${mode}.label`)}</span>
                        <span className="mt-1 block text-xs text-muted-foreground">{t(`health.presets.${mode}.description`)}</span>
                    </button>
                ))}
            </div>

            <button
                type="button"
                onClick={() => setAdvancedOpen((open) => !open)}
                className="flex w-full items-center justify-between rounded-xl border border-border px-3 py-2 text-sm font-medium"
                aria-expanded={advancedOpen}
            >
                <span className="flex items-center gap-2"><SlidersHorizontal className="size-4" />{t('health.advanced')}</span>
                <ChevronDown className={`size-4 transition-transform ${advancedOpen ? 'rotate-180' : ''}`} />
            </button>

            {advancedOpen && (
                <div className="space-y-4 border-t border-border pt-4">
                    <HealthToggle
                        label={t('smartHealth.label')}
                        hint={t('smartHealth.description')}
                        checked={smartEnabled}
                        disabled={setSetting.isPending}
                        onChange={(checked) => void save(SettingKey.SmartHealthEnabled, String(checked))}
                    />
                    <HealthToggle
                        label={t('healthWeighted.label')}
                        hint={t('healthWeighted.description')}
                        checked={values.get(SettingKey.HealthWeightedBalancerEnabled) === 'true'}
                        disabled={setSetting.isPending || !smartEnabled}
                        onChange={(checked) => void save(SettingKey.HealthWeightedBalancerEnabled, String(checked))}
                    />
                    <HealthToggle
                        label={t('health.shadowMode')}
                        hint={t('health.shadowModeHint')}
                        checked={values.get(SettingKey.HealthShadowMode) === 'true'}
                        disabled={setSetting.isPending || !smartEnabled}
                        onChange={(checked) => void save(SettingKey.HealthShadowMode, String(checked))}
                    />
                    <div className="grid gap-3 sm:grid-cols-2">
                        {advancedFields.map((field) => (
                            <label key={field.key} className="grid gap-1 text-xs text-muted-foreground">
                                {t(`health.fields.${field.label}`)}
                                <Input
                                    type={field.type}
                                    min={field.type === 'number' ? 0 : undefined}
                                    value={drafts[field.key] ?? ''}
                                    onChange={(event) => setDrafts((current) => ({ ...current, [field.key]: event.target.value }))}
                                    onBlur={() => void save(field.key, drafts[field.key] ?? '')}
                                    disabled={setSetting.isPending}
                                    className="h-9 rounded-xl text-sm"
                                />
                            </label>
                        ))}
                    </div>
                </div>
            )}
        </div>
    );
}

function HealthToggle({ label, hint, checked, disabled, onChange }: { label: string; hint: string; checked: boolean; disabled: boolean; onChange: (checked: boolean) => void }) {
    return (
        <div className="flex items-center justify-between gap-3">
            <div className="min-w-0">
                <div className="flex items-center gap-1.5 text-sm font-medium">
                    {label}
                    <TooltipProvider><Tooltip><TooltipTrigger asChild><HelpCircle className="size-3.5 cursor-help text-muted-foreground" /></TooltipTrigger><TooltipContent>{hint}</TooltipContent></Tooltip></TooltipProvider>
                </div>
            </div>
            <Switch checked={checked} onCheckedChange={onChange} disabled={disabled} aria-label={label} />
        </div>
    );
}
