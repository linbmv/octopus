'use client';

import { ArrowLeft, ChevronRight, RefreshCw, WalletCards } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { useState } from 'react';
import { ChannelType, useChannelList, useGlobalCodexQuota, useRefreshGlobalCodexQuota, type Channel, type ChannelKey, type CodexQuota } from '@/api/endpoints/channel';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { cn } from '@/lib/utils';
import { CodexQuotaCard } from './CodexQuotaCard';

/**
 * The quota surface is deliberately rendered in a portal-backed overlay. It
 * behaves like a compact Axonhub-style panel and never pushes page content.
 */
export function GlobalCodexQuota() {
    const t = useTranslations('codexQuota');
    const [selectedId, setSelectedId] = useState<string | null>(null);
    const channelsQuery = useChannelList();
    const quotaQuery = useGlobalCodexQuota();
    const refresh = useRefreshGlobalCodexQuota();
    const channels = (channelsQuery.data ?? []).map((item) => item.raw).filter((channel) => channel.type === ChannelType.OpenAICodex);
    const quotas = quotaQuery.data ?? [];
    const items = channels.flatMap((channel) => channel.keys.map((key) => ({ channel, key, quota: quotas.find((item) => item.channel_id === channel.id && item.channel_key_id === key.id) })));
    const summary = summarize(items.map((item) => item.quota).filter((quota): quota is CodexQuota => Boolean(quota)));
    const selected = items.find((item) => `${item.channel.id}-${item.key.id}` === selectedId);

    if (!channelsQuery.isLoading && !channels.length && !quotaQuery.isLoading && !quotaQuery.isError) return null;

    return (
        <Popover>
            <PopoverTrigger asChild>
                <Button type="button" variant="ghost" className="h-8 max-w-[11rem] shrink-0 gap-1.5 rounded-lg border border-border/70 bg-card/70 px-2 text-[11px] shadow-sm hover:bg-accent sm:max-w-none" aria-label={t('openPanel')}>
                    <span className="relative flex size-4 shrink-0 items-center justify-center rounded bg-primary/10 text-primary">
                        <WalletCards className="size-3" />
                        <span className={cn('absolute -right-0.5 -top-0.5 size-1.5 rounded-full ring-2 ring-card', summary.limited ? 'bg-orange-500' : 'bg-emerald-500')} />
                    </span>
                    <span className="hidden truncate font-medium sm:inline">{t('shortTitle')}</span>
                    {quotaQuery.isLoading || channelsQuery.isLoading ? <RefreshCw className="size-3 animate-spin text-muted-foreground" /> : <span className={cn('font-semibold tabular-nums', summary.maxUsed >= 90 ? 'text-destructive' : summary.maxUsed >= 70 ? 'text-orange-600 dark:text-orange-400' : 'text-emerald-600 dark:text-emerald-400')}>{summary.maxUsed}%</span>}
                </Button>
            </PopoverTrigger>
            <PopoverContent align="end" sideOffset={8} className="w-[min(calc(100vw-1rem),30rem)] overflow-hidden p-0">
                <div className="flex max-h-[min(88vh,56rem)] flex-col">
                    <header className="flex shrink-0 items-center gap-2 border-b border-border/70 bg-card/95 px-3 py-2.5">
                        {selected ? <Button type="button" variant="ghost" size="icon" className="size-7 shrink-0 rounded-md" aria-label={t('back')} onClick={() => setSelectedId(null)}><ArrowLeft className="size-3.5" /></Button> : <span className="flex size-7 shrink-0 items-center justify-center rounded-md bg-primary/10 text-primary"><WalletCards className="size-3.5" /></span>}
                        <div className="min-w-0 flex-1"><h2 className="truncate text-sm font-semibold">{selected ? t('details') : t('title')}</h2><p className="truncate text-[10px] text-muted-foreground">{selected ? selected.key.remark || selected.channel.name : t('description')}</p></div>
                        {!selected && <Button type="button" size="sm" variant="outline" className="h-7 shrink-0 rounded-md px-2 text-[11px]" disabled={refresh.isPending} onClick={() => refresh.mutate()}><RefreshCw className={cn('mr-1 size-3', refresh.isPending && 'animate-spin')} />{refresh.isPending ? t('refreshing') : t('refreshShort')}</Button>}
                    </header>

                    {quotaQuery.isError && <p className="p-4 text-xs text-destructive">{t('loadFailed')}</p>}
                    {channelsQuery.isLoading || quotaQuery.isLoading ? <p className="p-4 text-xs text-muted-foreground">{t('loading')}</p> : items.length ? (
                        selected ? <div className="min-h-0 overflow-y-auto bg-muted/10 p-2"><CodexQuotaCard channel={selected.channel} keyData={selected.key} quota={selected.quota} /></div> : <div className="min-h-0 space-y-1.5 overflow-y-auto bg-muted/10 p-2">{items.map(({ channel, key, quota }) => <CodexQuotaListItem key={`${channel.id}-${key.id}`} channel={channel} keyData={key} quota={quota} onClick={() => setSelectedId(`${channel.id}-${key.id}`)} />)}</div>
                    ) : <p className="p-4 text-xs text-muted-foreground">{t('empty')}</p>}
                </div>
            </PopoverContent>
        </Popover>
    );
}

function CodexQuotaListItem({ channel, keyData, quota, onClick }: { channel: Channel; keyData: ChannelKey; quota?: CodexQuota; onClick: () => void }) {
    const t = useTranslations('codexQuota');
    const usage = maxUsage(quota);
    const name = keyData.remark?.trim() || `${channel.name} #${keyData.id}`;
    return <button type="button" onClick={onClick} className="flex w-full items-center gap-2 rounded-lg border border-border/60 bg-card px-2.5 py-2 text-left transition-colors hover:border-primary/30 hover:bg-accent/30">
        <span className="size-2 shrink-0 rounded-full bg-primary/70" />
        <span className="min-w-0 flex-1"><span className="flex items-center gap-1.5"><Badge className="h-5 rounded-md bg-indigo-100 px-1.5 text-[10px] text-indigo-600 hover:bg-indigo-100 dark:bg-indigo-950/50 dark:text-indigo-300">Codex</Badge><Badge variant="outline" className="h-5 rounded-md px-1.5 text-[10px]">{keyData.enabled ? t('enabled') : t('disabled')}</Badge></span><span className="mt-1 block truncate text-xs font-medium">{name}</span></span>
        <span className={cn('shrink-0 text-xs font-semibold tabular-nums', usage >= 90 ? 'text-destructive' : usage >= 70 ? 'text-orange-600 dark:text-orange-400' : 'text-emerald-600 dark:text-emerald-400')}>{usage}%</span>
        <ChevronRight className="size-3.5 shrink-0 text-muted-foreground" />
    </button>;
}

function summarize(quotas: CodexQuota[]) {
    const maxUsed = quotas.flatMap((quota) => quotaWindows(quota)).reduce((max, value) => Math.max(max, Math.max(0, Math.min(100, Math.round(value)))), 0);
    return { limited: quotas.filter((quota) => Boolean(quota.error || quota.rate_limit?.limit_reached || quota.rate_limit?.allowed === false || quota.code_review_rate_limit?.limit_reached || quota.code_review_rate_limit?.allowed === false || quota.credits?.overage_limit_reached)).length, maxUsed };
}

function quotaWindows(quota: CodexQuota) {
    return [quota.rate_limit?.primary_window, quota.rate_limit?.secondary_window, quota.code_review_rate_limit?.primary_window, quota.code_review_rate_limit?.secondary_window, ...(quota.additional_rate_limits ?? []).flatMap((item) => [item.rate_limit?.primary_window, item.rate_limit?.secondary_window])].filter((window): window is NonNullable<typeof window> => Boolean(window)).map((window) => window.used_percent);
}

function maxUsage(quota?: CodexQuota) {
    if (!quota) return 0;
    return quotaWindows(quota).reduce((max, value) => Math.max(max, Math.max(0, Math.min(100, Math.round(value)))), 0);
}
