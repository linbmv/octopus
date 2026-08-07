'use client';

import { BatteryMedium, RefreshCw } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { ChannelType, useChannelList, useGlobalCodexQuota, useRefreshChannelQuota, useRefreshGlobalCodexQuota, useUpdateChannel, type Channel, type ChannelKey, type CodexQuota, type CodexQuotaWindow } from '@/api/endpoints/channel';
import { Button } from '@/components/ui/button';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { Switch } from '@/components/ui/switch';
import { cn } from '@/lib/utils';

type QuotaTranslator = ReturnType<typeof useTranslations>;
type QuotaWindowEntry = { window: CodexQuotaWindow; label: string };

/**
 * The quota surface is deliberately rendered in a portal-backed overlay. It
 * stays a list so the useful account state is visible without another click.
 */
export function GlobalCodexQuota() {
    const t = useTranslations('codexQuota');
    const channelsQuery = useChannelList();
    const quotaQuery = useGlobalCodexQuota();
    const refresh = useRefreshGlobalCodexQuota();
    const channels = (channelsQuery.data ?? []).map((item) => item.raw).filter((channel) => channel.type === ChannelType.OpenAICodex);
    const quotas = quotaQuery.data ?? [];
    const items = channels.flatMap((channel) => channel.keys.map((key) => ({ channel, key, quota: quotas.find((item) => item.channel_id === channel.id && item.channel_key_id === key.id) })));
    const summary = summarize(items.map((item) => item.quota).filter((quota): quota is CodexQuota => Boolean(quota)));
    const initialLoading = channelsQuery.isLoading && !channels.length;

    if (!channelsQuery.isLoading && !channels.length && !quotaQuery.isLoading && !quotaQuery.isError) return null;

    return (
        <Popover>
            <PopoverTrigger asChild>
                <Button type="button" variant="ghost" size="icon" className="rounded-xl transition-none text-muted-foreground hover:bg-transparent hover:text-foreground" aria-label={t('openPanel')} title={t('openPanel')} data-low-quota={summary.lowRemaining ? 'true' : 'false'}>
                    <BatteryMedium className={cn('size-4', quotaQuery.isLoading || channelsQuery.isLoading ? 'animate-pulse text-muted-foreground' : summary.lowRemaining ? 'text-destructive' : summary.limited ? 'text-orange-500' : summary.hasQuota ? 'text-emerald-600 dark:text-emerald-400' : 'text-muted-foreground')} />
                </Button>
            </PopoverTrigger>
            <PopoverContent align="end" sideOffset={8} className="w-[min(calc(100vw-1rem),30rem)] overflow-hidden p-0 shadow-none">
                <div className="flex max-h-[min(88vh,56rem)] flex-col">
                    <header className="flex shrink-0 items-center gap-2 border-b border-border/70 bg-card/95 px-3 py-2.5">
                        <span className="flex size-7 shrink-0 items-center justify-center rounded-md bg-primary/10 text-primary"><BatteryMedium className="size-4" /></span>
                        <div className="min-w-0 flex-1"><h2 className="truncate text-sm font-semibold">{t('title')}</h2><p className="truncate text-[10px] text-muted-foreground">{t('description')}</p></div>
                        <Button type="button" size="icon-sm" variant="ghost" className="size-7 rounded-md text-muted-foreground shadow-none hover:bg-muted/60 hover:text-foreground" disabled={refresh.isPending} onClick={() => refresh.mutate()} aria-label={t('refresh')} title={t('refresh')}><RefreshCw className={cn('size-3.5', refresh.isPending && 'animate-spin')} /></Button>
                    </header>

                    {quotaQuery.isError && <p className="p-4 text-xs text-destructive">{t('loadFailed')}</p>}
                    {initialLoading ? <p className="p-4 text-xs text-muted-foreground">{t('loading')}</p> : items.length ? <div className="min-h-0 space-y-1 overflow-y-auto bg-muted/10 p-1.5">{items.map(({ channel, key, quota }) => <CodexQuotaListItem key={`${channel.id}-${key.id}`} channel={channel} keyData={key} quota={quota} />)}</div> : <p className="p-4 text-xs text-muted-foreground">{t('empty')}</p>}
                </div>
            </PopoverContent>
        </Popover>
    );
}

function CodexQuotaListItem({ channel, keyData, quota }: { channel: Channel; keyData: ChannelKey; quota?: CodexQuota }) {
    const t = useTranslations('codexQuota');
    const updateChannel = useUpdateChannel();
    const refreshQuota = useRefreshChannelQuota(channel.id);
    const remark = keyData.remark?.trim();
    const name = remark || `${channel.name} #${keyData.id}`;
    const accountHint = quota?.account_hint || t('credentialFallback', { id: keyData.id });
    const windows = quotaWindowEntries(quota, t).slice(0, 2);
    const effectiveEnabled = channel.enabled && keyData.enabled;

    return <article className="rounded-md border border-border/60 bg-card px-2 py-1.5 text-left">
        <div className="flex min-w-0 items-center gap-2">
            <span className={cn('size-2 shrink-0 rounded-full', effectiveEnabled ? 'bg-emerald-500' : 'bg-slate-300')} aria-hidden="true" />
            <div className="min-w-0 flex-1">
                <div className="flex min-w-0 items-center gap-1.5">
                    <span className="truncate text-xs font-medium" title={`${channel.name} · ${name}`}>{name}</span>
                    {remark ? <span className="hidden shrink-0 truncate text-[10px] text-muted-foreground sm:inline" title={channel.name}>{channel.name}</span> : null}
                </div>
                <div className="mt-0.5 flex min-w-0 items-center gap-x-2 text-[10px] font-medium">
                    <span className="shrink-0 text-emerald-600 dark:text-emerald-300">{t('success')} {formatCount(keyData.stats?.request_success)}</span>
                    <span className="shrink-0 text-rose-500 dark:text-rose-300">{t('failed')} {formatCount(keyData.stats?.request_failed)}</span>
                    <span className="min-w-0 truncate text-muted-foreground">{t('plan')} <strong className="text-foreground">{quota?.plan_type || '—'}</strong></span>
                </div>
                <div className="mt-0.5 flex min-w-0 items-center gap-1 text-[9px] text-muted-foreground">
                    <span className="shrink-0">{t('account')}:</span>
                    <code className="truncate rounded bg-muted px-1 font-mono text-[9px]" title={accountHint}>{accountHint}</code>
                </div>
            </div>
            <div className="flex shrink-0 items-center gap-1">
                <Button type="button" size="icon-sm" variant="ghost" className="size-7 rounded-md text-muted-foreground shadow-none hover:bg-muted/60 hover:text-foreground" disabled={!keyData.enabled || refreshQuota.isPending} onClick={() => refreshQuota.mutate(keyData.id)} aria-label={`${t('refreshSingle')}: ${name}`} title={t('refreshSingle')}>
                    <RefreshCw className={cn('size-3.5', refreshQuota.isPending && 'animate-spin')} />
                </Button>
                <Switch className="shadow-none" checked={keyData.enabled} disabled={updateChannel.isPending} onCheckedChange={(enabled) => updateChannel.mutate({ id: channel.id, keys_to_update: [{ id: keyData.id, enabled }] })} aria-label={`${t('toggleEnable')}: ${name}`} />
            </div>
        </div>

        {windows.length ? <div className="mt-1 grid grid-cols-1 gap-x-3 gap-y-1 sm:grid-cols-2">{windows.map(({ window, label }) => {
            const value = clampPercent(window.used_percent);
            return <div key={`${label}-${window.reset_at}`} className="min-w-0">
                <div className="truncate text-[9px] text-muted-foreground">{label}</div>
                <div className="mt-0.5 h-1 overflow-hidden rounded-full bg-muted"><div className={cn('h-full rounded-full', value >= 90 ? 'bg-rose-400' : value >= 70 ? 'bg-amber-400' : 'bg-lime-400')} style={{ width: `${value}%` }} /></div>
            </div>;
        })}</div> : null}
    </article>;
}

function summarize(quotas: CodexQuota[]) {
    // The upstream field is used_percent; the battery warning is based on the
    // actual remaining quota so low remaining capacity is shown in red.
    const maxUsed = quotas.flatMap((quota) => quotaWindows(quota)).reduce((max, value) => Math.max(max, clampPercent(value)), 0);
    const hasQuota = quotas.length > 0;
    const remainingPercent = 100 - maxUsed;
    return {
        limited: quotas.filter((quota) => Boolean(quota.error || quota.rate_limit?.limit_reached || quota.rate_limit?.allowed === false || quota.code_review_rate_limit?.limit_reached || quota.code_review_rate_limit?.allowed === false || quota.credits?.overage_limit_reached)).length,
        hasQuota,
        lowRemaining: hasQuota && remainingPercent < 20,
    };
}

function quotaWindows(quota: CodexQuota | undefined) {
    if (!quota) return [];
    return [quota.rate_limit?.primary_window, quota.rate_limit?.secondary_window, quota.code_review_rate_limit?.primary_window, quota.code_review_rate_limit?.secondary_window, ...(quota.additional_rate_limits ?? []).flatMap((item) => [item.rate_limit?.primary_window, item.rate_limit?.secondary_window])].filter((window): window is NonNullable<typeof window> => Boolean(window)).map((window) => window.used_percent);
}

function quotaWindowEntries(quota: CodexQuota | undefined, t: QuotaTranslator): QuotaWindowEntry[] {
    if (!quota) return [];
    const windows = [quota.rate_limit?.primary_window, quota.rate_limit?.secondary_window, quota.code_review_rate_limit?.primary_window, quota.code_review_rate_limit?.secondary_window, ...(quota.additional_rate_limits ?? []).flatMap((item) => [item.rate_limit?.primary_window, item.rate_limit?.secondary_window])].filter((window): window is NonNullable<typeof window> => Boolean(window));
    return windows.map((window) => ({ window, label: quotaWindowLabel(window.limit_window_seconds, t) }));
}

function quotaWindowLabel(seconds: number, t: QuotaTranslator) {
    if (seconds === 604800) return t('weeklyLimit');
    if (seconds === 2592000) return t('monthlyLimit');
    if (seconds === 18000) return t('fiveHours');
    return t('windowDays', { count: Math.max(1, Math.round(seconds / 86_400)) });
}

function clampPercent(value: number) {
    return Number.isFinite(value) ? Math.max(0, Math.min(100, Math.round(value))) : 0;
}

function formatCount(value?: number) {
    return (value ?? 0).toLocaleString();
}
