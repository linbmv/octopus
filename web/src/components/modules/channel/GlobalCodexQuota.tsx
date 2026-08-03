'use client';

import { RefreshCw, WalletCards } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { ChannelType, useChannelList, useGlobalCodexQuota, useRefreshGlobalCodexQuota, type CodexQuota } from '@/api/endpoints/channel';
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
    const channelsQuery = useChannelList();
    const quotaQuery = useGlobalCodexQuota();
    const refresh = useRefreshGlobalCodexQuota();
    const channels = (channelsQuery.data ?? []).map((item) => item.raw).filter((channel) => channel.type === ChannelType.OpenAICodex);
    const quotas = quotaQuery.data ?? [];
    const items = channels.flatMap((channel) => channel.keys.map((key) => ({ channel, key, quota: quotas.find((item) => item.channel_id === channel.id && item.channel_key_id === key.id) })));
    const summary = summarize(items.map((item) => item.quota).filter((quota): quota is CodexQuota => Boolean(quota)));

    if (!channelsQuery.isLoading && !channels.length && !quotaQuery.isLoading && !quotaQuery.isError) return null;

    return (
        <Popover>
            <PopoverTrigger asChild>
                <Button type="button" variant="ghost" className="h-9 max-w-[12rem] shrink-0 gap-2 rounded-xl border border-border/70 bg-card/70 px-2.5 text-xs shadow-sm hover:bg-accent sm:max-w-none" aria-label={t('openPanel')}>
                    <span className="relative flex size-5 shrink-0 items-center justify-center rounded-md bg-primary/10 text-primary">
                        <WalletCards className="size-3.5" />
                        <span className={cn('absolute -right-0.5 -top-0.5 size-1.5 rounded-full ring-2 ring-card', summary.limited ? 'bg-orange-500' : 'bg-emerald-500')} />
                    </span>
                    <span className="hidden truncate font-medium sm:inline">{t('shortTitle')}</span>
                    {quotaQuery.isLoading || channelsQuery.isLoading ? <RefreshCw className="size-3.5 animate-spin text-muted-foreground" /> : <span className={cn('font-semibold tabular-nums', summary.maxUsed >= 90 ? 'text-destructive' : summary.maxUsed >= 70 ? 'text-orange-600 dark:text-orange-400' : 'text-emerald-600 dark:text-emerald-400')}>{summary.maxUsed}%</span>}
                </Button>
            </PopoverTrigger>
            <PopoverContent align="end" sideOffset={10} className="w-[min(calc(100vw-1rem),36rem)] overflow-hidden p-0">
                <div className="flex max-h-[min(88vh,56rem)] flex-col">
                    <header className="flex shrink-0 items-start gap-3 border-b border-border/70 bg-card/95 p-4">
                        <span className="flex size-9 shrink-0 items-center justify-center rounded-xl bg-primary/10 text-primary"><WalletCards className="size-4" /></span>
                        <div className="min-w-0 flex-1"><h2 className="text-sm font-semibold">{t('title')}</h2><p className="mt-0.5 text-xs text-muted-foreground">{t('description')}</p></div>
                        <Button type="button" size="sm" variant="outline" className="h-8 shrink-0 rounded-lg px-2 text-xs" disabled={refresh.isPending} onClick={() => refresh.mutate()}><RefreshCw className={cn('mr-1.5 size-3.5', refresh.isPending && 'animate-spin')} /><span className="hidden sm:inline">{refresh.isPending ? t('refreshing') : t('refresh')}</span><span className="sm:hidden">{refresh.isPending ? '…' : t('refreshShort')}</span></Button>
                    </header>

                    {quotaQuery.isError && <p className="p-4 text-xs text-destructive">{t('loadFailed')}</p>}
                    {channelsQuery.isLoading || quotaQuery.isLoading ? <p className="p-4 text-xs text-muted-foreground">{t('loading')}</p> : items.length ? (
                        <div className="min-h-0 space-y-3 overflow-y-auto bg-muted/10 p-3">{items.map(({ channel, key, quota }) => <CodexQuotaCard key={`${channel.id}-${key.id}`} channel={channel} keyData={key} quota={quota} />)}</div>
                    ) : <p className="p-4 text-xs text-muted-foreground">{t('empty')}</p>}
                </div>
            </PopoverContent>
        </Popover>
    );
}

function summarize(quotas: CodexQuota[]) {
    const maxUsed = quotas.flatMap((quota) => quotaWindows(quota)).reduce((max, value) => Math.max(max, Math.max(0, Math.min(100, Math.round(value)))), 0);
    return { limited: quotas.filter((quota) => Boolean(quota.error || quota.rate_limit?.limit_reached || quota.rate_limit?.allowed === false || quota.code_review_rate_limit?.limit_reached || quota.code_review_rate_limit?.allowed === false || quota.credits?.overage_limit_reached)).length, maxUsed };
}

function quotaWindows(quota: CodexQuota) {
    return [quota.rate_limit?.primary_window, quota.rate_limit?.secondary_window, quota.code_review_rate_limit?.primary_window, quota.code_review_rate_limit?.secondary_window, ...(quota.additional_rate_limits ?? []).flatMap((item) => [item.rate_limit?.primary_window, item.rate_limit?.secondary_window])].filter((window): window is NonNullable<typeof window> => Boolean(window)).map((window) => window.used_percent);
}
