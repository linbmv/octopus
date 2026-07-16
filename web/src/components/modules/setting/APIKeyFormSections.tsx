'use client';

import { CalendarDays } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { Badge } from '@/components/ui/badge';
import { Calendar } from '@/components/ui/calendar';
import { Input } from '@/components/ui/input';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { Switch } from '@/components/ui/switch';
import { cn } from '@/lib/utils';
import { hasAPIKeyModel, type APIKeyFormValues } from './useAPIKeyFormState';

export function APIKeyBasicSection({
    form,
    disabled,
    onChange,
}: {
    form: APIKeyFormValues;
    disabled: boolean;
    onChange: (change: Partial<APIKeyFormValues>) => void;
}) {
    const t = useTranslations('setting.apiKey.form');
    return (
        <section className="grid gap-2">
            <label className="grid gap-1 text-xs text-muted-foreground">
                {t('name')}
                <Input
                    type="text"
                    value={form.name}
                    onChange={(event) => onChange({ name: event.target.value })}
                    className="h-9 rounded-xl text-sm"
                    disabled={disabled}
                    required
                />
            </label>
            <div className="flex items-center justify-between pt-1">
                <span className="text-xs text-muted-foreground">{t('enabled')}</span>
                <Switch
                    checked={form.enabled ?? true}
                    onCheckedChange={(checked) => onChange({ enabled: checked })}
                    disabled={disabled}
                />
            </div>
        </section>
    );
}

export function APIKeyLimitSection({
    disabled,
    maxCostInput,
    isUnlimitedCost,
    expireOpen,
    expireDate,
    expireTime,
    neverExpire,
    onMaxCostChange,
    onClearMaxCost,
    onExpireOpenChange,
    onSelectDate,
    onExpireTimeChange,
    onTimeBlur,
    onToggleNeverExpire,
}: {
    disabled: boolean;
    maxCostInput: string;
    isUnlimitedCost: boolean;
    expireOpen: boolean;
    expireDate?: Date;
    expireTime: string;
    neverExpire: boolean;
    onMaxCostChange: (value: string) => void;
    onClearMaxCost: () => void;
    onExpireOpenChange: (open: boolean) => void;
    onSelectDate: (date: Date | undefined) => void;
    onExpireTimeChange: (value: string) => void;
    onTimeBlur: () => void;
    onToggleNeverExpire: () => void;
}) {
    const t = useTranslations('setting.apiKey.form');
    const expireLabel = neverExpire
        ? t('neverExpire')
        : expireDate?.toLocaleDateString() ?? t('selectDate');
    return (
        <section className="grid gap-2">
            <div className="grid gap-1 text-xs text-muted-foreground">
                {t('maxCost')}
                <div className="flex items-center gap-2">
                    <div className="relative flex-1">
                        <span className="absolute left-3 top-1/2 -translate-y-1/2 text-sm text-muted-foreground">$</span>
                        <Input
                            type="text"
                            inputMode="decimal"
                            placeholder={t('maxCostPlaceholder')}
                            value={maxCostInput}
                            onChange={(event) => onMaxCostChange(event.target.value)}
                            className="h-9 rounded-xl pl-7 text-sm"
                            disabled={disabled}
                        />
                    </div>
                    <ToggleButton active={isUnlimitedCost} disabled={disabled} onClick={onClearMaxCost}>
                        {t('unlimited')}
                    </ToggleButton>
                </div>
            </div>

            <div className="grid gap-1 text-xs text-muted-foreground">
                {t('expireAt')}
                <div className="relative flex items-center gap-2">
                    <Popover open={expireOpen && !neverExpire} onOpenChange={onExpireOpenChange}>
                        <PopoverTrigger asChild>
                            <button
                                type="button"
                                disabled={disabled || neverExpire}
                                className="flex h-9 flex-1 items-center justify-between gap-2 rounded-xl border border-border bg-muted/20 px-3 text-sm text-foreground transition-colors hover:bg-muted/30 disabled:opacity-50"
                            >
                                <span className="truncate">{expireLabel}</span>
                                <CalendarDays className="size-4 text-muted-foreground" />
                            </button>
                        </PopoverTrigger>
                        <PopoverContent align="start" side="bottom" sideOffset={8} className="w-fit overflow-hidden rounded-2xl border border-border/60 bg-card p-0 shadow-xl">
                            <Calendar mode="single" selected={expireDate} onSelect={onSelectDate} disabled={disabled} classNames={{ today: '' }} />
                        </PopoverContent>
                    </Popover>
                    <Input
                        type="text"
                        value={expireTime}
                        onChange={(event) => onExpireTimeChange(event.target.value)}
                        onBlur={onTimeBlur}
                        className="h-9 w-[92px] rounded-xl text-sm"
                        disabled={disabled || neverExpire || !expireDate}
                        inputMode="numeric"
                        placeholder="HH:mm"
                    />
                    <ToggleButton active={neverExpire} disabled={disabled} onClick={onToggleNeverExpire}>
                        {t('neverExpire')}
                    </ToggleButton>
                </div>
            </div>
        </section>
    );
}

export function APIKeyModelAccessSection({
    supportedModels,
    availableModels,
    disabled,
    onToggleModel,
}: {
    supportedModels?: string;
    availableModels: string[];
    disabled: boolean;
    onToggleModel: (model: string) => void;
}) {
    const t = useTranslations('setting.apiKey.form');
    return (
        <section className="grid gap-1">
            <div className="text-xs text-muted-foreground">{t('supportedModels')}</div>
            <div className="max-h-40 overflow-auto rounded-xl p-2">
                {availableModels.length === 0 ? (
                    <div className="py-2 text-center text-xs text-muted-foreground">{t('noModels')}</div>
                ) : (
                    <div className="flex flex-wrap gap-2">
                        {availableModels.map((model) => {
                            const checked = hasAPIKeyModel(supportedModels, model);
                            return (
                                <button key={model} type="button" disabled={disabled} onClick={() => onToggleModel(model)} className="text-left disabled:opacity-50">
                                    <Badge variant={checked ? 'default' : 'outline'} className={cn('cursor-pointer select-none', !checked && 'bg-background/40 hover:bg-background/70')}>
                                        {model}
                                    </Badge>
                                </button>
                            );
                        })}
                    </div>
                )}
            </div>
            <div className="text-[11px] text-muted-foreground/80">{t('modelsHint')}</div>
        </section>
    );
}

function ToggleButton({ active, disabled, onClick, children }: { active: boolean; disabled: boolean; onClick: () => void; children: React.ReactNode }) {
    return (
        <button
            type="button"
            onClick={onClick}
            disabled={disabled}
            aria-pressed={active}
            className={cn(
                'h-9 shrink-0 whitespace-nowrap rounded-xl border px-3 text-sm transition-colors',
                active ? 'border-primary/30 bg-primary text-primary-foreground' : 'border-border bg-muted/20 text-foreground hover:bg-muted/30',
                disabled && 'cursor-not-allowed opacity-50',
            )}
        >
            {children}
        </button>
    );
}
