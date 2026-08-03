'use client';

import { Download, KeyRound, RefreshCw } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { useState } from 'react';
import { useUpdateChannel, type Channel, type ChannelKey, type CodexQuota, type CodexQuotaWindow } from '@/api/endpoints/channel';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Switch } from '@/components/ui/switch';
import { cn, formatMoney } from '@/lib/utils';

type QuotaCardTranslator = ReturnType<typeof useTranslations>;

export function CodexQuotaCard({
    channel,
    keyData,
    quota,
    t,
}: {
    channel: Channel;
    keyData: ChannelKey;
    quota?: CodexQuota;
    t: QuotaCardTranslator;
}) {
    const updateChannel = useUpdateChannel();
    const [pending, setPending] = useState(false);
    const rows = quota ? quotaWindowRows(quota, t) : [];
    const displayName = credentialFileName(channel, keyData);
    const limited = Boolean(quota?.error || quota?.rate_limit?.limit_reached || quota?.rate_limit?.allowed === false || quota?.code_review_rate_limit?.limit_reached || quota?.code_review_rate_limit?.allowed === false || quota?.additional_rate_limits?.some((item) => item.rate_limit?.limit_reached || item.rate_limit?.allowed === false) || quota?.credits?.overage_limit_reached);
    const statusClass = keyData.status_code === 200 ? 'text-emerald-600 dark:text-emerald-400' : keyData.status_code >= 400 ? 'text-destructive' : 'text-muted-foreground';

    const toggleKey = (enabled: boolean) => {
        setPending(true);
        updateChannel.mutate(
            { id: channel.id, keys_to_update: [{ id: keyData.id, enabled }] },
            { onSettled: () => setPending(false) },
        );
    };

    return (
        <article className="overflow-hidden rounded-2xl border border-border/70 bg-card text-card-foreground shadow-sm transition-colors hover:border-primary/30 hover:bg-accent/[0.03]">
            <header className="flex items-start gap-3 p-3 pb-2.5 sm:p-4 sm:pb-3">
                <span className="mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary"><KeyRound className="size-4" /></span>
                <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-1.5">
                        <Badge className="h-5 bg-indigo-500/10 px-1.5 text-[10px] text-indigo-700 hover:bg-indigo-500/10 dark:text-indigo-300">Codex</Badge>
                        <Badge variant="outline" className={cn('h-5 px-1.5 text-[10px]', keyData.enabled ? 'border-emerald-500/30 text-emerald-700 dark:text-emerald-400' : 'border-muted-foreground/30 text-muted-foreground')}>
                            {keyData.enabled ? t('enabled') : t('disabled')}
                        </Badge>
                        {quota?.plan_type && <Badge variant="outline" className="h-5 px-1.5 text-[10px]">{quota.plan_type}</Badge>}
                        {quota && <Badge variant="secondary" className={cn('h-5 px-1.5 text-[10px]', limited ? 'bg-orange-500/15 text-orange-700 dark:text-orange-400' : 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-400')}>{limited ? t('limited') : t('available')}</Badge>}
                    </div>
                    <h3 className="mt-2 break-all text-sm font-semibold leading-5" title={displayName}>{displayName}</h3>
                    <p className="mt-1 truncate text-[11px] text-muted-foreground" title={channel.name}>{channel.name}{keyData.remark && keyData.remark !== displayName ? ` · ${keyData.remark}` : ''}</p>
                </div>
            </header>

            <div className="grid grid-cols-3 gap-2 px-3 text-[11px] sm:px-4">
                <CredentialMeta label={t('size')} value={formatBytes(keyData.channel_key.length)} />
                <CredentialMeta label={t('lastUse')} value={keyData.last_use_time_stamp > 0 ? formatDate(keyData.last_use_time_stamp * 1000) : t('never')} />
                <CredentialMeta label={t('statusCode')} value={keyData.status_code > 0 ? String(keyData.status_code) : '—'} valueClass={statusClass} />
            </div>

            <div className="mx-3 mt-3 border-t border-border/60 sm:mx-4" />
            <div className="space-y-2.5 p-3 sm:p-4 sm:pt-3">
                <div className="flex items-center justify-between gap-2 text-xs">
                    <span className="font-semibold text-muted-foreground">{t('healthStatus')}</span>
                    <span className={cn('font-semibold', statusClass)}>{statusLabel(keyData.status_code, t)}</span>
                </div>
                <div className="h-1.5 overflow-hidden rounded-full bg-muted" aria-label={t('healthStatus')}><div className={cn('h-full rounded-full', keyData.status_code === 200 ? 'w-full bg-emerald-500' : keyData.status_code >= 400 ? 'w-1/4 bg-destructive' : 'w-1/2 bg-muted-foreground/40')} /></div>

                <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
                    <span>{t('credits')}: <strong className="text-foreground">{quota?.credits?.unlimited ? t('unlimited') : quota?.credits?.balance ?? '—'}</strong></span>
                    <span>{t('cost')}: <strong className="text-foreground">{formatMoney(keyData.total_cost).formatted.value}{formatMoney(keyData.total_cost).formatted.unit}</strong></span>
                    {quota?.fetched_at && <span>{t('updatedAt', { time: formatDate(new Date(quota.fetched_at).getTime()) })}</span>}
                </div>

                {quota?.error ? <div className="rounded-lg border border-destructive/25 bg-destructive/5 p-2 text-xs text-destructive">{quota.error}</div> : rows.length ? <div className="grid gap-2 sm:grid-cols-2">{rows.map((row) => <QuotaWindow key={`${row.label}-${row.kind}-${row.window.reset_at}`} row={row} t={t} />)}</div> : <div className="rounded-lg border border-border/60 bg-muted/20 p-2 text-xs text-muted-foreground">{keyData.enabled ? t('unavailable') : t('disabledHint')}</div>}
            </div>

            <footer className="flex items-center justify-between gap-3 border-t border-border/60 bg-muted/20 px-3 py-2.5 sm:px-4">
                <Button type="button" variant="outline" size="icon" className="size-8 rounded-lg" aria-label={t('download')} title={t('download')} onClick={() => downloadCredential(channel, keyData)}>
                    <Download className="size-4" />
                </Button>
                <label className="flex items-center gap-2 text-xs font-medium text-muted-foreground">
                    <span>{keyData.enabled ? t('enabled') : t('disabled')}</span>
                    {pending ? <RefreshCw className="size-4 animate-spin" /> : <Switch checked={keyData.enabled} disabled={pending || updateChannel.isPending} onCheckedChange={toggleKey} aria-label={t('toggleEnable')} />}
                </label>
            </footer>
        </article>
    );
}

type QuotaRow = { label: string; kind: 'primary' | 'secondary'; window: CodexQuotaWindow };

function quotaWindowRows(quota: CodexQuota, t: QuotaCardTranslator): QuotaRow[] {
    const rows: QuotaRow[] = [];
    const add = (label: string, rateLimit?: CodexQuota['rate_limit']) => {
        if (rateLimit?.primary_window) rows.push({ label, kind: 'primary', window: rateLimit.primary_window });
        if (rateLimit?.secondary_window) rows.push({ label, kind: 'secondary', window: rateLimit.secondary_window });
    };
    add('', quota.rate_limit);
    add(t('codeReview'), quota.code_review_rate_limit);
    for (const [index, item] of (quota.additional_rate_limits ?? []).entries()) add(item.limit_name || item.metered_feature || t('additional', { id: index + 1 }), item.rate_limit);
    return rows;
}

export function countCodexQuotaWindows(quota: CodexQuota) {
    return [quota.rate_limit, quota.code_review_rate_limit, ...(quota.additional_rate_limits ?? []).map((item) => item.rate_limit)].reduce((count, rateLimit) => count + Number(Boolean(rateLimit?.primary_window)) + Number(Boolean(rateLimit?.secondary_window)), 0);
}

function QuotaWindow({ row, t }: { row: QuotaRow; t: QuotaCardTranslator }) {
    const used = clampPercent(row.window.used_percent);
    const label = row.window.limit_window_seconds === 18_000 ? t('fiveHours') : row.window.limit_window_seconds === 604_800 ? t('sevenDays') : t('windowDays', { count: Math.max(1, Math.round(row.window.limit_window_seconds / 86_400)) });
    const reset = row.window.reset_at > 0 ? formatDate(row.window.reset_at * 1000) : t('unknown');
    return (
        <div className="rounded-lg border border-border/60 bg-background/60 p-2.5">
            <div className="flex items-center justify-between gap-2"><span className="truncate text-[11px] font-semibold">{row.label ? `${row.label} · ` : ''}{label}</span><span className={cn('text-sm font-bold tabular-nums', used >= 90 ? 'text-destructive' : used >= 70 ? 'text-orange-600 dark:text-orange-400' : 'text-emerald-600 dark:text-emerald-400')}>{used}%</span></div>
            <span className="text-[10px] text-muted-foreground">{row.kind === 'primary' ? t('primaryWindow') : t('secondaryWindow')}</span>
            <div className="mt-1.5 h-1.5 overflow-hidden rounded-full bg-muted"><div className={cn('h-full rounded-full', used >= 90 ? 'bg-destructive' : used >= 70 ? 'bg-orange-500' : 'bg-emerald-500')} style={{ width: `${used}%` }} /></div>
            <div className="mt-1 flex justify-between gap-2 text-[10px] text-muted-foreground"><span>{t('remaining', { remaining: 100 - used })}</span><span className="truncate">{t('reset', { time: reset })}</span></div>
        </div>
    );
}

function CredentialMeta({ label, value, valueClass }: { label: string; value: string; valueClass?: string }) {
    return <div className="min-w-0"><div className="text-muted-foreground">{label}</div><div className={cn('mt-0.5 truncate font-semibold', valueClass)} title={value}>{value}</div></div>;
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

function formatDate(timestamp: number) {
    if (!Number.isFinite(timestamp) || timestamp <= 0) return '—';
    return new Date(timestamp).toLocaleString([], { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' });
}

function clampPercent(value: number) {
    return Math.max(0, Math.min(100, Math.round(value)));
}

function statusLabel(status: number, t: QuotaCardTranslator) {
    if (status === 200) return t('healthy');
    if (status >= 400) return t('failed');
    return t('unknown');
}
