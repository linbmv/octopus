'use client';

import { Download, Info, RefreshCw } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { useState } from 'react';
import { useRefreshChannelQuota, useUpdateChannel, type Channel, type ChannelKey, type CodexQuota, type CodexQuotaWindow } from '@/api/endpoints/channel';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Switch } from '@/components/ui/switch';
import { cn } from '@/lib/utils';

type QuotaCardTranslator = ReturnType<typeof useTranslations>;
type QuotaRow = { label: string; kind: 'primary' | 'secondary'; window: CodexQuotaWindow };

export function CodexQuotaCard({
    channel,
    keyData,
    quota,
}: {
    channel: Channel;
    keyData: ChannelKey;
    quota?: CodexQuota;
}) {
    const t = useTranslations('codexQuota');
    const updateChannel = useUpdateChannel();
    const refreshQuota = useRefreshChannelQuota(channel.id);
    const [pending, setPending] = useState(false);
    const displayName = credentialFileName(channel, keyData);
    const accountHint = quota?.account_hint || t('credentialFallback', { id: keyData.id });
    const rows = quota ? quotaWindowRows(quota, t) : [];
    const health = healthState(keyData.status_code, t);
    const resetCount = rows.filter((row) => row.window.reset_at > 0).length;
    const credits = quota?.credits?.unlimited ? t('unlimited') : quota?.credits?.balance ?? '—';

    const toggleKey = (enabled: boolean) => {
        setPending(true);
        updateChannel.mutate(
            { id: channel.id, keys_to_update: [{ id: keyData.id, enabled }] },
            { onSettled: () => setPending(false) },
        );
    };

    return (
        <article className="overflow-hidden rounded-2xl border border-slate-200/90 bg-white text-slate-700 shadow-sm dark:border-border dark:bg-card dark:text-card-foreground">
            <header className="flex items-start gap-3 px-4 pb-2 pt-4">
                <span className="mt-0.5 size-8 shrink-0 rounded-lg border border-sky-100 bg-sky-50 dark:border-sky-900 dark:bg-sky-950/30" aria-hidden="true" />
                <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                        <Badge className="h-6 rounded-lg bg-indigo-100 px-2 text-xs font-semibold text-indigo-600 hover:bg-indigo-100 dark:bg-indigo-950/50 dark:text-indigo-300">Codex</Badge>
                        <Badge variant="outline" className={cn('h-6 rounded-lg px-2 text-xs font-semibold', keyData.enabled ? 'border-emerald-200 bg-emerald-50 text-emerald-600 dark:border-emerald-900 dark:bg-emerald-950/30 dark:text-emerald-300' : 'border-slate-200 bg-slate-50 text-slate-400 dark:border-border dark:bg-muted dark:text-muted-foreground')}>
                            {keyData.enabled ? t('enabled') : t('disabled')}
                        </Badge>
                    </div>
                    <h3 className="mt-2 break-all text-lg font-bold leading-6 text-slate-700 dark:text-foreground" title={displayName}>{displayName}</h3>
                    <div className="mt-1 flex min-w-0 items-center gap-1 text-[11px] text-slate-500 dark:text-muted-foreground">
                        <span className="shrink-0">{t('account')}:</span>
                        <code className="truncate rounded bg-slate-100 px-1.5 py-0.5 font-mono text-[10px] text-slate-600 dark:bg-muted dark:text-muted-foreground" title={accountHint}>{accountHint}</code>
                    </div>
                </div>
            </header>

            <div className="flex flex-wrap items-center gap-x-6 gap-y-1 px-4 text-sm text-slate-500 dark:text-muted-foreground">
                <span><span className="font-medium">{t('size')}:</span> <strong className="text-slate-700 dark:text-foreground">{formatBytes(keyData.channel_key.length)}</strong></span>
                <span><span className="font-medium">{t('modified')}:</span> <strong className="text-slate-700 dark:text-foreground">{keyData.last_use_time_stamp > 0 ? formatDate(keyData.last_use_time_stamp * 1000) : t('never')}</strong></span>
            </div>

            <div className="flex items-center gap-2 px-4 pb-3 pt-2 text-xs font-semibold">
                <Badge variant="secondary" className="rounded-full bg-emerald-50 px-2.5 py-1 text-emerald-600 hover:bg-emerald-50 dark:bg-emerald-950/30 dark:text-emerald-300">{t('success')} {formatCount(keyData.stats?.request_success)}</Badge>
                <Badge variant="secondary" className="rounded-full bg-rose-50 px-2.5 py-1 text-rose-500 hover:bg-rose-50 dark:bg-rose-950/30 dark:text-rose-300">{t('failed')} {formatCount(keyData.stats?.request_failed)}</Badge>
            </div>

            <section className="border-t border-slate-100 px-4 py-3 dark:border-border">
                <div className="flex items-center justify-between gap-3 text-sm font-semibold">
                    <span>{t('healthStatus')}</span>
                    <span className={cn('rounded-full px-3 py-1 text-xs', health.pillClass)}>{health.label}</span>
                </div>
                <div className="mt-3 flex gap-1" aria-label={t('healthStatus')}>
                    {Array.from({ length: 18 }, (_, index) => <span key={index} className={cn('h-2 flex-1 rounded-full', index < health.filled ? health.segmentClass : health.emptyClass)} />)}
                </div>
            </section>

            <section className="px-4 pb-4 pt-1">
                <div className="flex flex-wrap items-center gap-x-2 text-sm text-slate-600 dark:text-muted-foreground">
                    <span>{t('plan')} <strong className="text-slate-700 dark:text-foreground">{quota?.plan_type || '—'}</strong></span>
                    <span aria-hidden="true">|</span>
                    <span>{t('resetCount')} <strong className="text-slate-700 dark:text-foreground">{resetCount}</strong></span>
                    <span className="ml-auto">{t('credits')} <strong className="text-slate-700 dark:text-foreground">{credits}</strong></span>
                </div>

                {quota?.error ? <div className="mt-3 rounded-lg border border-destructive/20 bg-destructive/5 p-2 text-xs text-destructive">{quota.error}</div> : rows.length ? (
                    <div className="mt-3 space-y-3">
                        {rows.map((row) => <QuotaWindow key={`${row.label}-${row.kind}-${row.window.reset_at}`} row={row} t={t} />)}
                    </div>
                ) : <div className="mt-3 rounded-lg bg-slate-50 p-3 text-xs text-slate-500 dark:bg-muted/40 dark:text-muted-foreground">{keyData.enabled ? t('unavailable') : t('disabledHint')}</div>}
            </section>

            <footer className="flex items-center justify-between border-t border-slate-100 bg-slate-50/70 px-4 py-2.5 dark:border-border dark:bg-muted/20">
                <div className="flex items-center gap-1">
                    <Button type="button" variant="ghost" className="h-7 rounded-md px-1.5 text-[11px] text-slate-600 hover:bg-white dark:text-muted-foreground dark:hover:bg-muted" aria-label={t('download')} title={t('download')} onClick={() => downloadCredential(channel, keyData)}>
                        <Download className="mr-1.5 size-3.5" />{t('downloadShort')}
                    </Button>
                    <Button type="button" variant="ghost" className="h-7 rounded-md px-1.5 text-[11px] text-slate-600 hover:bg-white dark:text-muted-foreground dark:hover:bg-muted" disabled={!keyData.enabled || refreshQuota.isPending} aria-label={`${t('refreshSingle')}: ${displayName}`} title={t('refreshSingle')} onClick={() => refreshQuota.mutate(keyData.id)}>
                        <RefreshCw className={cn('mr-1.5 size-3.5', refreshQuota.isPending && 'animate-spin')} />{refreshQuota.isPending ? t('refreshing') : t('refreshShort')}
                    </Button>
                </div>
                <label className="flex items-center gap-2 text-xs font-medium text-slate-600 dark:text-muted-foreground">
                    <span>{keyData.enabled ? t('enabled') : t('disabled')}</span>
                    {pending ? <RefreshCw className="size-4 animate-spin" /> : <Switch checked={keyData.enabled} disabled={pending || updateChannel.isPending} onCheckedChange={toggleKey} aria-label={t('toggleEnable')} />}
                </label>
            </footer>
        </article>
    );
}

function quotaWindowRows(quota: CodexQuota, t: QuotaCardTranslator): QuotaRow[] {
    const rows: QuotaRow[] = [];
    const add = (label: string, rateLimit?: CodexQuota['rate_limit']) => {
        if (rateLimit?.primary_window) rows.push({ label, kind: 'primary', window: rateLimit.primary_window });
        if (rateLimit?.secondary_window) rows.push({ label, kind: 'secondary', window: rateLimit.secondary_window });
    };
    add(t('monthlyLimit'), quota.rate_limit);
    add(t('codeReview'), quota.code_review_rate_limit);
    for (const [index, item] of (quota.additional_rate_limits ?? []).entries()) add(item.limit_name || item.metered_feature || t('additional', { id: index + 1 }), item.rate_limit);
    return rows;
}

function QuotaWindow({ row, t }: { row: QuotaRow; t: QuotaCardTranslator }) {
    const used = clampPercent(row.window.used_percent);
    const period = row.window.limit_window_seconds === 18_000 ? t('fiveHours') : row.window.limit_window_seconds === 604_800 ? t('sevenDays') : t('windowDays', { count: Math.max(1, Math.round(row.window.limit_window_seconds / 86_400)) });
    const reset = row.window.reset_at > 0 ? formatResetTime(row.window.reset_at * 1000) : t('unknown');
    return (
        <div>
            <div className="flex items-center gap-2 text-sm font-bold">
                <span>{row.label || period}</span>
                <Info className="size-3.5 text-slate-400" aria-hidden="true" />
                <span className="ml-auto text-base text-slate-500 dark:text-muted-foreground">{used}%</span>
                <span className="text-xs font-normal text-slate-400">{reset}</span>
            </div>
            <div className="mt-2 h-2 overflow-hidden rounded-full bg-slate-100 dark:bg-muted"><div className={cn('h-full rounded-full transition-all', used >= 90 ? 'bg-rose-400' : used >= 70 ? 'bg-amber-400' : 'bg-lime-400')} style={{ width: `${used}%` }} /></div>
        </div>
    );
}

function healthState(status: number, t: QuotaCardTranslator) {
    if (status === 200) return { label: t('healthy'), filled: 18, segmentClass: 'bg-lime-400', emptyClass: 'bg-slate-100', pillClass: 'bg-lime-50 text-lime-600 dark:bg-lime-950/30 dark:text-lime-300' };
    if (status >= 400) return { label: t('failed'), filled: 4, segmentClass: 'bg-rose-300', emptyClass: 'bg-slate-100', pillClass: 'bg-rose-50 text-rose-500 dark:bg-rose-950/30 dark:text-rose-300' };
    return { label: t('healthUnknown'), filled: 0, segmentClass: 'bg-slate-200', emptyClass: 'bg-sky-100', pillClass: 'bg-slate-50 text-slate-400 dark:bg-muted dark:text-muted-foreground' };
}

function credentialFileName(channel: Channel, key: ChannelKey) {
    const remark = key.remark?.trim();
    if (remark) return remark.endsWith('.json') ? remark : `${remark}.json`;
    return `codex-${channel.name.replace(/[^a-zA-Z0-9_-]+/g, '-').replace(/-+/g, '-').replace(/^-|-$/g, '') || channel.id}-${key.id}.json`;
}

function downloadCredential(channel: Channel, key: ChannelKey) {
    let content = key.channel_key;
    try { content = JSON.stringify(JSON.parse(key.channel_key), null, 2); } catch { /* Preserve legacy/raw credential text. */ }
    const blob = new Blob([content], { type: 'application/json;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement('a');
    anchor.href = url;
    anchor.download = credentialFileName(channel, key);
    anchor.click();
    URL.revokeObjectURL(url);
}

function formatBytes(bytes: number) {
    if (bytes < 1024) return `${bytes} B`;
    return `${(bytes / 1024).toFixed(2)} KB`;
}

function formatCount(value?: number) {
    return (value ?? 0).toLocaleString();
}

function formatDate(timestamp: number) {
    if (!Number.isFinite(timestamp) || timestamp <= 0) return '—';
    return new Date(timestamp).toLocaleString([], { month: 'numeric', day: 'numeric', year: 'numeric', hour: 'numeric', minute: '2-digit', second: '2-digit' });
}

function formatResetTime(timestamp: number) {
    return new Date(timestamp).toLocaleString([], { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' });
}

function clampPercent(value: number) {
    return Math.max(0, Math.min(100, Math.round(value)));
}
