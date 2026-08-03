'use client';

import { RefreshCw, WalletCards } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { useGlobalCodexQuota, useRefreshGlobalCodexQuota, type CodexQuota, type CodexQuotaWindow } from '@/api/endpoints/channel';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { cn } from '@/lib/utils';

type QuotaRow = { label: string; window: CodexQuotaWindow };

export function GlobalCodexQuota() {
    const t = useTranslations('codexQuota');
    const query = useGlobalCodexQuota();
    const refresh = useRefreshGlobalCodexQuota();

    if (!query.isLoading && !query.isError && !query.data?.length) return null;
    const groups = groupQuotas(query.data ?? []);

    return (
        <section className="mx-2 mb-4 shrink-0 rounded-2xl border border-border/80 bg-card/95 p-3 text-card-foreground shadow-sm sm:p-4">
            <header className="flex items-center gap-3">
                <span className="flex size-9 shrink-0 items-center justify-center rounded-xl bg-primary/10 text-primary">
                    <WalletCards className="size-4" />
                </span>
                <div className="min-w-0 flex-1">
                    <h2 className="truncate text-sm font-semibold">{t('title')}</h2>
                    <p className="truncate text-xs text-muted-foreground">{t('description')}</p>
                </div>
                <Button type="button" size="sm" variant="outline" className="h-8 shrink-0 rounded-xl px-2 text-xs" disabled={refresh.isPending} onClick={() => refresh.mutate()}>
                    <RefreshCw className={cn('mr-1.5 size-3.5', refresh.isPending && 'animate-spin')} />
                    {refresh.isPending ? t('refreshing') : t('refresh')}
                </Button>
            </header>

            {query.isLoading && <p className="mt-3 text-xs text-muted-foreground">{t('loading')}</p>}
            {query.isError && <p className="mt-3 text-xs text-destructive">{t('loadFailed')}</p>}
            {groups.length > 0 && (
                <div className="mt-3 grid gap-2 lg:grid-cols-2">
                    {groups.map((group) => <GlobalQuotaChannel key={group.channelID} group={group} t={t} />)}
                </div>
            )}
        </section>
    );
}

type QuotaGroup = {
    channelID: number;
    channelName: string;
    quotas: CodexQuota[];
};

function groupQuotas(quotas: CodexQuota[]) {
    const groups = new Map<number, QuotaGroup>();
    for (const quota of quotas) {
        const existing = groups.get(quota.channel_id);
        if (existing) existing.quotas.push(quota);
        else groups.set(quota.channel_id, {
            channelID: quota.channel_id,
            channelName: quota.channel_name || `Channel ${quota.channel_id}`,
            quotas: [quota],
        });
    }
    return [...groups.values()];
}

function GlobalQuotaChannel({ group, t }: { group: QuotaGroup; t: ReturnType<typeof useTranslations> }) {
    const limited = group.quotas.some((quota) => quota.error || quota.rate_limit?.limit_reached || quota.rate_limit?.allowed === false);
    return (
        <div className="min-w-0 rounded-xl border border-border/70 bg-background/60 p-3">
            <div className="mb-2 flex items-center gap-2">
                <span className="min-w-0 flex-1 truncate text-sm font-semibold" title={group.channelName}>{group.channelName}</span>
                <Badge variant="secondary" className={cn('h-5 shrink-0 px-1.5 text-[10px]', limited ? 'bg-red-500/15 text-red-700 dark:text-red-400' : 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-400')}>
                    {limited ? t('limited') : t('available')}
                </Badge>
            </div>
            <div className="space-y-2">
                {group.quotas.map((quota) => (
                    <div key={`${quota.channel_key_id}-${quota.fetched_at}`} className="space-y-1.5">
                        <div className="flex items-center gap-2 text-xs">
                            <span className="min-w-0 flex-1 truncate text-muted-foreground">{quota.key_remark || t('key', { id: quota.channel_key_id })}</span>
                            {quota.plan_type && <Badge variant="outline" className="h-5 px-1.5 text-[10px]">{quota.plan_type}</Badge>}
                        </div>
                        {quota.error ? <p className="text-xs text-destructive">{quota.error}</p> : quotaRows(quota, t).map((row) => <GlobalQuotaRow key={`${row.label}-${row.window.reset_at}`} row={row} t={t} />)}
                    </div>
                ))}
            </div>
        </div>
    );
}

function quotaRows(quota: CodexQuota, t: ReturnType<typeof useTranslations>): QuotaRow[] {
    const rows: QuotaRow[] = [];
    const add = (label: string, rateLimit?: CodexQuota['rate_limit']) => {
        if (rateLimit?.primary_window) rows.push({ label, window: rateLimit.primary_window });
        if (rateLimit?.secondary_window) rows.push({ label, window: rateLimit.secondary_window });
    };
    add('', quota.rate_limit);
    add(t('codeReview'), quota.code_review_rate_limit);
    for (const [index, item] of (quota.additional_rate_limits ?? []).entries()) {
        add(item.limit_name || item.metered_feature || t('additional', { id: index + 1 }), item.rate_limit);
    }
    return rows;
}

function GlobalQuotaRow({ row, t }: { row: QuotaRow; t: ReturnType<typeof useTranslations> }) {
    const used = Math.max(0, Math.min(100, row.window.used_percent));
    const label = row.window.limit_window_seconds === 18_000 ? t('fiveHours') : row.window.limit_window_seconds === 604_800 ? t('sevenDays') : t('windowDays', { count: Math.max(1, Math.round(row.window.limit_window_seconds / 86_400)) });
    const reset = row.window.reset_at > 0 ? new Date(row.window.reset_at * 1000).toLocaleString() : t('unknown');
    return (
        <div className="grid gap-1 text-[11px] sm:grid-cols-[minmax(88px,auto)_1fr_auto] sm:items-center sm:gap-2">
            <span className="truncate text-muted-foreground">{row.label ? `${row.label} · ` : ''}{label}</span>
            <div className="h-1.5 overflow-hidden rounded-full bg-muted"><div className={cn('h-full rounded-full', used >= 90 ? 'bg-destructive' : used >= 70 ? 'bg-orange-500' : 'bg-emerald-500')} style={{ width: `${used}%` }} /></div>
            <span className="whitespace-nowrap text-muted-foreground">{t('usage', { used, remaining: 100 - used })} · {t('reset', { time: reset })}</span>
        </div>
    );
}
