'use client';

import { useCallback, useMemo, useState, type FormEvent } from 'react';
import { useTranslations } from 'next-intl';
import { CalendarDays, Check, Loader, X } from 'lucide-react';
import { Input } from '@/components/ui/input';
import { Calendar } from '@/components/ui/calendar';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { Switch } from '@/components/ui/switch';
import { Badge } from '@/components/ui/badge';
import { type APIKey } from '@/api/endpoints/apikey';
import { useGroupList } from '@/api/endpoints/group';
import { cn } from '@/lib/utils';

export type APIKeyFormValues = Omit<APIKey, 'id' | 'api_key'>;

function toExpireAt(date: Date, time: string): number {
    const t = /^\d{2}:\d{2}$/.test(time) ? time : '00:00';
    const [hh, mm] = t.split(':').map(Number);
    const d = new Date(Date.UTC(date.getFullYear(), date.getMonth(), date.getDate(), hh, mm, 0));
    return Math.floor(d.getTime() / 1000);
}

function parseExpireDate(expireAt?: number): Date | undefined {
    if (!expireAt) return undefined;
    const d = new Date(expireAt * 1000);
    return isNaN(d.getTime()) ? undefined : d;
}

function normalizeHHmm(input: string): string {
    const cleaned = input.replace(/[^\d:]/g, '');
    const parts = cleaned.includes(':') ? cleaned.split(':') : [cleaned.slice(0, 2), cleaned.slice(2, 4)];
    const hh = Math.min(23, Math.max(0, parseInt(parts[0] || '0', 10)));
    const mm = Math.min(59, Math.max(0, parseInt(parts[1] || '0', 10)));
    return `${hh.toString().padStart(2, '0')}:${mm.toString().padStart(2, '0')}`;
}

function normalizeMoneyInput(input: string): string {
    const cleaned = input.replace(/[^\d.]/g, '');
    const [intPart, ...rest] = cleaned.split('.');
    return rest.length > 0 ? `${intPart}.${rest.join('').slice(0, 6)}` : intPart;
}

function toggleModel(current: string | undefined, model: string): string | undefined {
    const models = current ? current.split(',').filter(Boolean) : [];
    const next = models.includes(model)
        ? models.filter((m) => m !== model)
        : [...models, model];
    return next.length ? next.join(',') : undefined;
}

function hasModel(supported: string | undefined, model: string): boolean {
    return supported ? supported.split(',').includes(model) : false;
}

interface APIKeyFormProps {
    apiKey?: APIKey;
    isPending: boolean;
    submitLabel: string;
    onSubmit: (data: APIKeyFormValues) => void;
    onClose: () => void;
}

export function APIKeyForm({ apiKey, isPending, submitLabel, onSubmit, onClose }: APIKeyFormProps) {
    const t = useTranslations('setting');
    const { data: groups = [] } = useGroupList();

    const [form, setForm] = useState<APIKeyFormValues>(() => ({
        name: apiKey?.name ?? '',
        enabled: apiKey?.enabled ?? true,
        expire_at: apiKey?.expire_at,
        max_cost: apiKey?.max_cost,
        supported_models: apiKey?.supported_models,
    }));
    const [maxCostInput, setMaxCostInput] = useState(() =>
        apiKey?.max_cost != null ? String(apiKey.max_cost) : ''
    );
    const [expireTime, setExpireTime] = useState(() => {
        if (apiKey?.expire_at) {
            const d = new Date(apiKey.expire_at * 1000);
            if (!isNaN(d.getTime())) {
                return `${d.getUTCHours().toString().padStart(2, '0')}:${d.getUTCMinutes().toString().padStart(2, '0')}`;
            }
        }
        return '00:00';
    });
    const [expireOpen, setExpireOpen] = useState(false);

    const availableModels = useMemo(() => {
        const names = groups.map((g) => g.name).filter(Boolean);
        return Array.from(new Set(names)).sort((a, b) => a.localeCompare(b));
    }, [groups]);

    const expireDate = parseExpireDate(form.expire_at);
    const neverExpire = !form.expire_at;
    const isUnlimitedCost = maxCostInput.trim() === '';

    const expireLabel = neverExpire
        ? t('apiKey.form.neverExpire')
        : expireDate
            ? expireDate.toLocaleDateString()
            : t('apiKey.form.selectDate');

    const updateForm = useCallback((updater: Partial<APIKeyFormValues>) => {
        setForm((prev) => ({ ...prev, ...updater }));
    }, []);

    const handleSelectDate = useCallback((d: Date | undefined) => {
        if (d) {
            updateForm({ expire_at: toExpireAt(d, expireTime) });
            setExpireOpen(false);
        } else {
            updateForm({ expire_at: undefined });
        }
    }, [updateForm, expireTime]);

    const handleTimeBlur = useCallback(() => {
        if (!expireDate) return;
        const normalized = normalizeHHmm(expireTime);
        setExpireTime(normalized);
        updateForm({ expire_at: toExpireAt(expireDate, normalized) });
    }, [expireDate, expireTime, updateForm]);

    const handleToggleNeverExpire = useCallback(() => {
        if (neverExpire) {
            updateForm({ expire_at: toExpireAt(new Date(), expireTime) });
        } else {
            updateForm({ expire_at: undefined });
            setExpireOpen(false);
        }
    }, [neverExpire, expireTime, updateForm]);

    const handleMaxCostChange = useCallback((val: string) => {
        const normalized = normalizeMoneyInput(val);
        setMaxCostInput(normalized);
        const num = parseFloat(normalized);
        updateForm({ max_cost: Number.isFinite(num) ? num : undefined });
    }, [updateForm]);

    const handleClearMaxCost = useCallback(() => {
        setMaxCostInput('');
        updateForm({ max_cost: undefined });
    }, [updateForm]);

    const handleSubmit = useCallback((e: FormEvent) => {
        e.preventDefault();
        if (!form.name.trim()) return;
        onSubmit(form);
    }, [form, onSubmit]);

    return (
        <form onSubmit={handleSubmit} className="grid gap-2">
            <label className="grid gap-1 text-xs text-muted-foreground">
                {t('apiKey.form.name')}
                <Input
                    type="text"
                    value={form.name}
                    onChange={(e) => updateForm({ name: e.target.value })}
                    className="h-9 text-sm rounded-xl"
                    disabled={isPending}
                    required
                />
            </label>

            <div className="grid gap-1 text-xs text-muted-foreground">
                {t('apiKey.form.maxCost')}
                <div className="flex items-center gap-2">
                    <div className="relative flex-1">
                        <span className="absolute left-3 top-1/2 -translate-y-1/2 text-sm text-muted-foreground">$</span>
                        <Input
                            type="text"
                            inputMode="decimal"
                            placeholder={t('apiKey.form.maxCostPlaceholder')}
                            value={maxCostInput}
                            onChange={(e) => handleMaxCostChange(e.target.value)}
                            className="h-9 text-sm rounded-xl pl-7"
                            disabled={isPending}
                        />
                    </div>
                    <button
                        type="button"
                        onClick={handleClearMaxCost}
                        disabled={isPending}
                        aria-pressed={isUnlimitedCost}
                        className={cn(
                            'h-9 px-3 rounded-xl border text-sm transition-colors shrink-0',
                            isUnlimitedCost
                                ? 'bg-primary text-primary-foreground border-primary/30'
                                : 'border-border bg-muted/20 text-foreground hover:bg-muted/30',
                            isPending && 'opacity-50 cursor-not-allowed'
                        )}
                    >
                        {t('apiKey.form.unlimited')}
                    </button>
                </div>
            </div>

            <div className="grid gap-1 text-xs text-muted-foreground">
                {t('apiKey.form.expireAt')}
                <div className="flex items-center gap-2 relative">
                    <Popover
                        open={expireOpen && !neverExpire}
                        onOpenChange={setExpireOpen}
                    >
                        <PopoverTrigger asChild>
                            <button
                                type="button"
                                disabled={isPending || neverExpire}
                                className="h-9 flex-1 flex items-center justify-between gap-2 rounded-xl border border-border bg-muted/20 px-3 text-sm text-foreground transition-colors hover:bg-muted/30 disabled:opacity-50"
                            >
                                <span className="truncate">{expireLabel}</span>
                                <CalendarDays className="size-4 text-muted-foreground" />
                            </button>
                        </PopoverTrigger>
                        <PopoverContent
                            align="start"
                            side="bottom"
                            sideOffset={8}
                            className="w-fit rounded-2xl border border-border/60 shadow-xl overflow-hidden bg-card p-0"
                        >
                            <Calendar
                                mode="single"
                                selected={expireDate}
                                onSelect={handleSelectDate}
                                disabled={isPending}
                                classNames={{ today: '' }}
                            />
                        </PopoverContent>
                    </Popover>

                    <Input
                        type="text"
                        value={expireTime}
                        onChange={(e) => setExpireTime(e.target.value.replace(/[^\d:]/g, '').slice(0, 5))}
                        onBlur={handleTimeBlur}
                        className="h-9 w-[92px] text-sm rounded-xl"
                        disabled={isPending || neverExpire || !expireDate}
                        inputMode="numeric"
                        placeholder="HH:mm"
                    />

                    <button
                        type="button"
                        onClick={handleToggleNeverExpire}
                        disabled={isPending}
                        aria-pressed={neverExpire}
                        className={cn(
                            'h-9 px-3 rounded-xl border text-sm transition-colors whitespace-nowrap shrink-0',
                            neverExpire
                                ? 'bg-primary text-primary-foreground border-primary/30'
                                : 'border-border bg-muted/20 text-foreground hover:bg-muted/30',
                            isPending && 'opacity-50 cursor-not-allowed'
                        )}
                    >
                        {t('apiKey.form.neverExpire')}
                    </button>
                </div>
            </div>

            <div className="grid gap-1">
                <div className="text-xs text-muted-foreground">{t('apiKey.form.supportedModels')}</div>
                <div className="max-h-40 overflow-auto rounded-xl p-2">
                    {availableModels.length === 0 ? (
                        <div className="text-xs text-muted-foreground py-2 text-center">
                            {t('apiKey.form.noModels')}
                        </div>
                    ) : (
                        <div className="flex flex-wrap gap-2">
                            {availableModels.map((m) => {
                                const checked = hasModel(form.supported_models, m);
                                return (
                                    <button
                                        key={m}
                                        type="button"
                                        disabled={isPending}
                                        onClick={() => updateForm({ supported_models: toggleModel(form.supported_models, m) })}
                                        className="text-left disabled:opacity-50"
                                    >
                                        <Badge
                                            variant={checked ? 'default' : 'outline'}
                                            className={cn(
                                                'cursor-pointer select-none',
                                                !checked && 'bg-background/40 hover:bg-background/70'
                                            )}
                                        >
                                            {m}
                                        </Badge>
                                    </button>
                                );
                            })}
                        </div>
                    )}
                </div>
                <div className="text-[11px] text-muted-foreground/80">{t('apiKey.form.modelsHint')}</div>
            </div>

            <div className="flex items-center justify-between pt-1">
                <span className="text-xs text-muted-foreground">{t('apiKey.form.enabled')}</span>
                <Switch
                    checked={form.enabled ?? true}
                    onCheckedChange={(checked) => updateForm({ enabled: checked })}
                    disabled={isPending}
                />
            </div>

            <div className="flex gap-2 pt-2 mt-3">
                <button
                    type="button"
                    onClick={onClose}
                    disabled={isPending}
                    className="flex-1 h-9 flex items-center justify-center gap-1.5 rounded-xl bg-muted text-muted-foreground text-sm font-medium transition-all hover:bg-muted/80 active:scale-[0.98] disabled:opacity-50"
                >
                    <X className="size-4" />
                    {t('apiKey.form.cancel')}
                </button>
                <button
                    type="submit"
                    disabled={isPending || !form.name.trim()}
                    className="flex-1 h-9 flex items-center justify-center gap-1.5 rounded-xl bg-primary text-primary-foreground text-sm font-medium transition-all hover:bg-primary/90 active:scale-[0.98] disabled:opacity-50"
                >
                    {isPending ? <Loader className="size-4 animate-spin" /> : <Check className="size-4" />}
                    {submitLabel}
                </button>
            </div>
        </form>
    );
}
