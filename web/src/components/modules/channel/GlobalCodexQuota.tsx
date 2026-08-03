'use client';

import { BatteryMedium, RefreshCw } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { ChannelType, useChannelList, useGlobalCodexQuota, useRefreshGlobalCodexQuota, type Channel, type ChannelKey, type CodexQuota, type CodexQuotaWindow } from '@/api/endpoints/channel';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
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
                <Button type="button" variant="ghost" className="h-8 max-w-[11rem] shrink-0 gap-1.5 rounded-lg border border-border/70 bg-card/70 px-2 text-[11px] shadow-sm hover:bg-accent sm:max-w-none" aria-label={t('openPanel')}>
                    <span className="relative flex size-4 shrink-0 items-center justify-center text-primary">
                        <BatteryMedium className="size-4" />
                        <span className={cn('absolute -right-0.5 -top-0.5 size-1.5 rounded-full ring-2 ring-card', summary.limited ? 'bg-orange-500' : 'bg-emerald-500')} />
                    </span>
                    <span className="hidden truncate font-medium sm:inline">{t('shortTitle')}</span>
                    {quotaQuery.isLoading || channelsQuery.isLoading ? <RefreshCw className="size-3 animate-spin text-muted-foreground" /> : <span className={cn('font-semibold tabular-nums', summary.maxUsed >= 90 ? 'text-destructive' : summary.maxUsed >= 70 ? 'text-orange-600 dark:text-orange-400' : 'text-emerald-600 dark:text-emerald-400')}>{summary.maxUsed}%</span>}
                </Button>
            </PopoverTrigger>
            <PopoverContent align="end" sideOffset={8} className="w-[min(calc(100vw-1rem),30rem)] overflow-hidden p-0">
                <div className="flex max-h-[min(88vh,56rem)] flex-col">
                    <header className="flex shrink-0 items-center gap-2 border-b border-border/70 bg-card/95 px-3 py-2.5">
                        <span className="flex size-7 shrink-0 items-center justify-center rounded-md bg-primary/10 text-primary"><BatteryMedium className="size-4" /></span>
                        <div className="min-w-0 flex-1"><h2 className="truncate text-sm font-semibold">{t('title')}</h2><p className="truncate text-[10px] text-muted-foreground">{t('description')}</p></div>
                        <Button type="button" size="sm" variant="outline" className="h-7 shrink-0 rounded-md px-2 text-[11px]" disabled={refresh.isPending} onClick={() => refresh.mutate()} aria-label={t('refresh')}><RefreshCw className={cn('mr-1 size-3', refresh.isPending && 'animate-spin')} />{refresh.isPending ? t('refreshing') : t('refreshShort')}</Button>
                    </header>

                    {quotaQuery.isError && <p className="p-4 text-xs text-destructive">{t('loadFailed')}</p>}
                    {initialLoading ? <p className="p-4 text-xs text-muted-foreground">{t('loading')}</p> : items.length ? <div className="min-h-0 space-y-1.5 overflow-y-auto bg-muted/10 p-2">{items.map(({ channel, key, quota }) => <CodexQuotaListItem key={`${channel.id}-${key.id}`} channel={channel} keyData={key} quota={quota} />)}</div> : <p className="p-4 text-xs text-muted-foreground">{t('empty')}</p>}
                </div>
            </PopoverContent>
        </Popover>
    );
}

function CodexQuotaListItem({ channel, keyData, quota }: { channel: Channel; keyData: ChannelKey; quota?: CodexQuota }) {
    const t = useTranslations('codexQuota');
    const usage = maxUsage(quota);
    const name = keyData.remark?.trim() || `${channel.name} #${keyData.id}`;
    const windows = quotaWindowEntries(quota, t).slice(0, 2);
    const enabled = channel.enabled && keyData.enabled;

    return <article className="rounded-lg border border-border/60 bg-card px-2.5 py-2.5 text-left">
        <div className="flex items-start gap-2">
            <span className={cn('mt-1.5 size-2 shrink-0 rounded-full', enabled ? 'bg-emerald-500' : 'bg-slate-300')} />
            <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-1.5"><Badge className="h-5 rounded-md bg-indigo-100 px-1.5 text-[10px] text-indigo-600 hover:bg-indigo-100 dark:bg-indigo-950/50 dark:text-indigo-300">Codex</Badge><Badge variant="outline" className="h-5 rounded-md px-1.5 text-[10px]">{enabled ? t('enabled') : t('disabled')}</Badge></div>
                <div className="mt-1 truncate text-xs font-medium" title={name}>{name}</div>
            </div>
            <span className={cn('shrink-0 text-sm font-semibold tabular-nums', usage >= 90 ? 'text-destructive' : usage >= 70 ? 'text-orange-600 dark:text-orange-400' : 'text-emerald-600 dark:text-emerald-400')}>{quota ? `${usage}%` : '—'}</span>
        </div>

        <div className="mt-2 flex flex-wrap items-center gap-x-2 gap-y-1 text-[10px] font-medium">
            <Badge variant="secondary" className="h-5 rounded-full bg-emerald-50 px-2 text-emerald-600 hover:bg-emerald-50 dark:bg-emerald-950/30 dark:text-emerald-300">{t('success')} {formatCount(channel.stats?.request_success)}</Badge>
            <Badge variant="secondary" className="h-5 rounded-full bg-rose-50 px-2 text-rose-500 hover:bg-rose-50 dark:bg-rose-950/30 dark:text-rose-300">{t('failed')} {formatCount(channel.stats?.request_failed)}</Badge>
            <span className="text-muted-foreground">{t('plan')} <strong className="text-foreground">{quota?.plan_type || '—'}</strong></span>
        </div>

        {windows.length ? <div className="mt-2 grid grid-cols-1 gap-1.5 sm:grid-cols-2">{windows.map(({ window, label }) => {
            const value = clampPercent(window.used_percent);
            return <div key={`${label}-${window.reset_at}`} className="min-w-0">
                <div className="flex items-center justify-between gap-2 text-[10px] text-muted-foreground"><span>{label}</span><span className="shrink-0 font-semibold tabular-nums text-foreground">{value}%</span></div>
                <div className="mt-1 h-1.5 overflow-hidden rounded-full bg-muted"><div className={cn('h-full rounded-full', value >= 90 ? 'bg-rose-400' : value >= 70 ? 'bg-amber-400' : 'bg-lime-400')} style={{ width: `${value}%` }} /></div>
            </div>;
        })}</div> : null}
    </article>;
}

function summarize(quotas: CodexQuota[]) {
    const maxUsed = quotas.flatMap((quota) => quotaWindows(quota)).reduce((max, value) => Math.max(max, Math.max(0, Math.min(100, Math.round(value)))), 0);
    return { limited: quotas.filter((quota) => Boolean(quota.error || quota.rate_limit?.limit_reached || quota.rate_limit?.allowed === false || quota.code_review_rate_limit?.limit_reached || quota.code_review_rate_limit?.allowed === false || quota.credits?.overage_limit_reached)).length, maxUsed };
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

function maxUsage(quota?: CodexQuota) {
    return quotaWindows(quota).reduce((max, value) => Math.max(max, Math.max(0, Math.min(100, Math.round(value)))), 0);
}

function quotaWindowLabel(seconds: number, t: QuotaTranslator) {
    if (seconds === 604800) return t('weeklyLimit');
    if (seconds === 2592000) return t('monthlyLimit');
    if (seconds === 18000) return t('fiveHours');
    return t('windowDays', { count: Math.max(1, Math.round(seconds / 86_400)) });
}

function clampPercent(value: number) {
    return Math.max(0, Math.min(100, Math.round(value)));
}

function formatCount(value?: number) {
    return (value ?? 0).toLocaleString();
}
