'use client';

import { useEffect, useState, useRef } from 'react';
import { useTranslations } from 'next-intl';
import { ScrollText, Calendar, ShieldCheck, Trash2 } from 'lucide-react';
import { Input } from '@/components/ui/input';
import { Switch } from '@/components/ui/switch';
import { Button } from '@/components/ui/button';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import {
    useSettingList,
    useSetSetting,
    SettingKey,
    RelayLogContentMode,
    type RelayLogContentModeValue,
} from '@/api/endpoints/setting';
import { useClearLogs } from '@/api/endpoints/log';
import { toast } from '@/components/common/Toast';

const contentModeDescriptionKey = {
    [RelayLogContentMode.Metadata]: 'log.contentMode.description.metadata',
    [RelayLogContentMode.Full]: 'log.contentMode.description.full',
    [RelayLogContentMode.Disabled]: 'log.contentMode.description.disabled',
} as const satisfies Record<RelayLogContentModeValue, string>;

function isRelayLogContentMode(value: string): value is RelayLogContentModeValue {
    return Object.values(RelayLogContentMode).some(mode => mode === value);
}

export function SettingLog() {
    const t = useTranslations('setting');
    const { data: settings } = useSettingList();
    const setSetting = useSetSetting();
    const clearLogs = useClearLogs();

    const [enabled, setEnabled] = useState(true);
    const [keepPeriod, setKeepPeriod] = useState('7');
    const [contentMode, setContentMode] = useState<RelayLogContentModeValue>(RelayLogContentMode.Metadata);
    const [isClearing, setIsClearing] = useState(false);

    const initialEnabled = useRef(true);
    const initialKeepPeriod = useRef('7');
    const initialContentMode = useRef<RelayLogContentModeValue>(RelayLogContentMode.Metadata);

    useEffect(() => {
        if (settings) {
            const enabledSetting = settings.find(s => s.key === SettingKey.RelayLogKeepEnabled);
            const periodSetting = settings.find(s => s.key === SettingKey.RelayLogKeepPeriod);
            const contentModeSetting = settings.find(s => s.key === SettingKey.RelayLogContentMode);
            if (enabledSetting) {
                const isEnabled = enabledSetting.value === 'true';
                queueMicrotask(() => setEnabled(isEnabled));
                initialEnabled.current = isEnabled;
            }
            if (periodSetting) {
                queueMicrotask(() => setKeepPeriod(periodSetting.value));
                initialKeepPeriod.current = periodSetting.value;
            }
            if (contentModeSetting && isRelayLogContentMode(contentModeSetting.value)) {
                const loadedMode = contentModeSetting.value;
                queueMicrotask(() => setContentMode(loadedMode));
                initialContentMode.current = loadedMode;
            }
        }
    }, [settings]);

    const handleEnabledChange = (checked: boolean) => {
        setEnabled(checked);
        setSetting.mutate(
            { key: SettingKey.RelayLogKeepEnabled, value: checked ? 'true' : 'false' },
            {
                onSuccess: () => {
                    toast.success(t('saved'));
                    initialEnabled.current = checked;
                }
            }
        );
    };

    const handleContentModeChange = (value: string) => {
        if (!isRelayLogContentMode(value)) return;

        const previous = initialContentMode.current;
        setContentMode(value);
        setSetting.mutate(
            { key: SettingKey.RelayLogContentMode, value },
            {
                onSuccess: () => {
                    toast.success(t('saved'));
                    initialContentMode.current = value;
                },
                onError: () => {
                    setContentMode(previous);
                    toast.error(t('log.contentMode.saveFailed'));
                },
            }
        );
    };

    const handleKeepPeriodSave = () => {
        if (keepPeriod === initialKeepPeriod.current) return;

        setSetting.mutate(
            { key: SettingKey.RelayLogKeepPeriod, value: keepPeriod },
            {
                onSuccess: () => {
                    toast.success(t('saved'));
                    initialKeepPeriod.current = keepPeriod;
                }
            }
        );
    };

    const handleClearLogs = () => {
        setIsClearing(true);
        clearLogs.mutate(undefined, {
            onSuccess: () => {
                toast.success(t('log.clearSuccess'));
                setIsClearing(false);
            },
            onError: () => {
                toast.error(t('log.clearFailed'));
                setIsClearing(false);
            }
        });
    };

    return (
        <div className="rounded-3xl border border-border bg-card p-6 space-y-5">
            <h2 className="text-lg font-bold text-card-foreground flex items-center gap-2">
                <ScrollText className="h-5 w-5" />
                {t('log.title')}
            </h2>

            {/* 新日志的内容采集策略；全文必须由管理员明确选择。 */}
            <div className="space-y-2">
                <div className="flex items-center justify-between gap-4">
                    <div className="flex items-center gap-3">
                        <ShieldCheck className="h-5 w-5 text-muted-foreground" />
                        <span className="text-sm font-medium">{t('log.contentMode.label')}</span>
                    </div>
                    <Select value={contentMode} onValueChange={handleContentModeChange}>
                        <SelectTrigger className="w-48 rounded-xl">
                            <SelectValue />
                        </SelectTrigger>
                        <SelectContent className="rounded-xl">
                            <SelectItem value={RelayLogContentMode.Metadata} className="rounded-xl">
                                {t('log.contentMode.option.metadata')}
                            </SelectItem>
                            <SelectItem value={RelayLogContentMode.Full} className="rounded-xl">
                                {t('log.contentMode.option.full')}
                            </SelectItem>
                            <SelectItem value={RelayLogContentMode.Disabled} className="rounded-xl">
                                {t('log.contentMode.option.disabled')}
                            </SelectItem>
                        </SelectContent>
                    </Select>
                </div>
                <p className={contentMode === RelayLogContentMode.Full ? 'text-xs text-amber-600 dark:text-amber-400' : 'text-xs text-muted-foreground'}>
                    {t(contentModeDescriptionKey[contentMode])}
                </p>
            </div>

            {/* 是否启用历史日志 */}
            <div className="flex items-center justify-between gap-4">
                <div className="flex items-center gap-3">
                    <ScrollText className="h-5 w-5 text-muted-foreground" />
                    <span className="text-sm font-medium">{t('log.enabled.label')}</span>
                </div>
                <Switch
                    checked={enabled}
                    onCheckedChange={handleEnabledChange}
                />
            </div>

            {/* 历史日志保存范围 */}
            <div className="flex items-center justify-between gap-4">
                <div className="flex items-center gap-3">
                    <Calendar className="h-5 w-5 text-muted-foreground" />
                    <span className="text-sm font-medium">{t('log.keepPeriod.label')}</span>
                </div>
                <Input
                    type="number"
                    value={keepPeriod}
                    onChange={(e) => setKeepPeriod(e.target.value)}
                    onBlur={handleKeepPeriodSave}
                    placeholder={t('log.keepPeriod.placeholder')}
                    className="w-48 rounded-xl"
                    disabled={!enabled}
                />
            </div>

            {/* 清空历史日志 */}
            <div className="flex items-center justify-between gap-4">
                <div className="flex items-center gap-3">
                    <Trash2 className="h-5 w-5 text-muted-foreground" />
                    <span className="text-sm font-medium">{t('log.clear.label')}</span>
                </div>
                <Button
                    variant="destructive"
                    size="sm"
                    onClick={handleClearLogs}
                    disabled={isClearing}
                    className="rounded-xl"
                >
                    {isClearing ? t('log.clear.clearing') : t('log.clear.button')}
                </Button>
            </div>
        </div>
    );
}
