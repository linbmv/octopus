'use client';

import { Activity, CheckCircle2, Clock, DollarSign, FileText, Globe, Key, ShieldAlert, Trash2, TrendingUp, WalletCards, XCircle } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { ChannelType, useChannelCircuit, useChannelQuota, useChannelRuntimeURLs, useResetChannelCircuit, type Channel, type CodexQuota } from '@/api/endpoints/channel';
import { type StatsMetricsFormatted } from '@/api/endpoints/stats';
import { ChannelErrorOverview } from '@/components/modules/error-observability';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { cn, formatMoney } from '@/lib/utils';
import { CapabilityEvidencePanel } from './CapabilityEvidence';
import { CodexQuotaCard, countCodexQuotaWindows } from './CodexQuotaCard';
import { SelfHealingPanel } from './SelfHealing';

export function ChannelOverview({
    channel,
    stats,
    confirmingDelete,
    deletePending,
    onEdit,
    onDelete,
}: {
    channel: Channel;
    stats: StatsMetricsFormatted;
    confirmingDelete: boolean;
    deletePending: boolean;
    onEdit: () => void;
    onDelete: () => void;
}) {
    const t = useTranslations('channel.detail');
    return (
        <>
            <div className="max-h-[68vh] space-y-4 overflow-y-auto sm:max-h-[72vh] sm:space-y-5">
                <ChannelQuotaPanel channel={channel} />
                {!isCodexChannel(channel) && <>
                <dl className="grid grid-cols-1 gap-3 sm:grid-cols-3">
                    <SummaryMetric icon={Activity} label={t('metrics.totalRequests')} value={stats.request_count.formatted.value} unit={stats.request_count.formatted.unit} color="text-chart-1" />
                    <SummaryMetric icon={FileText} label={t('metrics.totalToken')} value={stats.total_token.formatted.value} unit={stats.total_token.formatted.unit} color="text-chart-3" />
                    <SummaryMetric icon={DollarSign} label={t('metrics.totalCost')} value={stats.total_cost.formatted.value} unit={stats.total_cost.formatted.unit} color="text-chart-5" />
                </dl>
                <div className="flex items-center gap-2 text-xs text-muted-foreground">
                    <ShieldAlert className="size-3.5" />
                    <span>{t('policyProfile')}</span>
                    <Badge variant="outline" className="h-5 px-1.5 text-[10px]">{t(`policyProfiles.${channel.policy_profile}`)}</Badge>
                </div>

                <ChannelErrorOverview channelId={channel.id} />
                <ChannelCircuitPanel channelId={channel.id} />
                <SelfHealingPanel channel={channel} />
                <CapabilityEvidencePanel channel={channel} />
                <MetricSection title={t('sections.requests')} icon={TrendingUp}>
                    <DetailMetric icon={CheckCircle2} label={t('metrics.successRequests')} value={stats.request_success.formatted.value} unit={stats.request_success.formatted.unit} color="text-accent" />
                    <DetailMetric icon={XCircle} label={t('metrics.failedRequests')} value={stats.request_failed.formatted.value} unit={stats.request_failed.formatted.unit} color="text-destructive" />
                </MetricSection>
                <MetricSection title={t('sections.tokens')} icon={FileText}>
                    <DetailMetric label={t('metrics.inputToken')} value={stats.input_token.formatted.value} unit={stats.input_token.formatted.unit} />
                    <DetailMetric label={t('metrics.outputToken')} value={stats.output_token.formatted.value} unit={stats.output_token.formatted.unit} />
                    <DetailMetric label={t('metrics.reasoningToken')} value={stats.reasoning_token.formatted.value} unit={stats.reasoning_token.formatted.unit} />
                </MetricSection>
                <MetricSection title={t('sections.costs')} icon={DollarSign}>
                    <DetailMetric label={t('metrics.inputCost')} value={stats.input_cost.formatted.value} unit={stats.input_cost.formatted.unit} />
                    <DetailMetric label={t('metrics.outputCost')} value={stats.output_cost.formatted.value} unit={stats.output_cost.formatted.unit} />
                </MetricSection>
                <MetricSection title={t('sections.limits')} icon={Activity}>
                    <DetailMetric label={t('metrics.rpmLimit')} value={(channel.rpm_limit ?? 0) > 0 ? channel.rpm_limit : t('metrics.unlimited')} />
                    <DetailMetric label={t('metrics.maxConcurrency')} value={(channel.max_concurrency ?? 0) > 0 ? channel.max_concurrency : t('metrics.unlimited')} />
                </MetricSection>

                <RuntimeLists channel={channel} />
                <dl className="rounded-2xl border bg-card p-3 transition-colors hover:bg-accent/5 sm:p-4">
                    <dt className="mb-2 flex items-center gap-2 text-xs text-muted-foreground"><Clock className="size-4 text-primary" />{t('metrics.avgWaitTime')}</dt>
                    <dd className="text-2xl font-bold text-primary">{stats.wait_time.formatted.value}<span className="ml-1 text-sm font-normal text-muted-foreground">{stats.wait_time.formatted.unit}</span></dd>
                </dl>
                </>}
            </div>
            {!isCodexChannel(channel) && <div className="grid gap-3 pt-2 sm:grid-cols-2">
                <Button onClick={onEdit} variant={confirmingDelete ? 'secondary' : 'default'} className="h-12 w-full rounded-2xl">
                    {confirmingDelete ? t('actions.cancel') : t('actions.edit')}
                </Button>
                <Button onClick={onDelete} disabled={deletePending} variant="destructive" className="h-12 w-full rounded-2xl">
                    <Trash2 className={`size-4 transition-transform ${confirmingDelete ? 'scale-110' : ''}`} />
                    {deletePending ? t('actions.deleting') : confirmingDelete ? t('actions.confirmDelete') : t('actions.delete')}
                </Button>
            </div>}
        </>
    );
}

function ChannelQuotaPanel({ channel }: { channel: Channel }) {
    const t = useTranslations('channel.detail.quota');
    const isCodex = isCodexChannel(channel);
    const { data: quotas, isLoading, isError } = useChannelQuota(channel.id, isCodex);
    if (!isCodex) return null;

    const limited = quotas?.filter(isQuotaLimited).length ?? 0;
    const quotaByKey = new Map((quotas ?? []).map((quota) => [quota.channel_key_id, quota]));
    const windows = quotas?.reduce((count, quota) => count + countCodexQuotaWindows(quota), 0) ?? 0;

    return (
        <section className="overflow-hidden rounded-3xl border border-primary/20 bg-gradient-to-br from-primary/[0.08] via-card to-card p-3 shadow-sm sm:p-4">
            <header className="flex items-start gap-3">
                <span className="flex size-10 shrink-0 items-center justify-center rounded-2xl bg-primary/15 text-primary">
                    <WalletCards className="size-5" />
                </span>
                <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                        <SectionTitle icon={WalletCards}>{t('title')}</SectionTitle>
                        {!isLoading && !isError && channel.keys?.length ? <Badge variant="outline" className="h-5 border-primary/25 bg-primary/5 px-1.5 text-[10px]">{t('credentialCount', { count: channel.keys.length })}</Badge> : null}
                    </div>
                    <p className="mt-1 text-xs text-muted-foreground">{t('description')}</p>
                </div>
            </header>
            {isLoading && <div className="rounded-2xl border bg-card p-4 text-sm text-muted-foreground">{t('loading')}</div>}
            {isError && <div className="rounded-2xl border border-destructive/30 bg-destructive/5 p-4 text-sm text-destructive">{t('loadFailed')}</div>}
            {!isLoading && !isError && !channel.keys?.length && <div className="rounded-2xl border bg-card p-4 text-sm text-muted-foreground">{t('empty')}</div>}
            {!isLoading && !isError && channel.keys?.length ? (
                <div className="mt-3 space-y-3">
                    <div className="grid grid-cols-3 gap-2">
                        <QuotaSummary label={t('credentials')} value={channel.keys.length} />
                        <QuotaSummary label={t('windows')} value={windows} />
                        <QuotaSummary label={t('limitedAccounts')} value={limited} tone={limited ? 'warning' : 'success'} />
                    </div>
                    <div className="space-y-2">
                        {channel.keys.map((key) => <CodexQuotaCard key={key.id} channel={channel} keyData={key} quota={quotaByKey.get(key.id)} t={t} />)}
                    </div>
                </div>
            ) : null}
        </section>
    );
}

function isCodexChannel(channel: Pick<Channel, 'type'>) {
    return String(channel.type).trim().toLowerCase() === ChannelType.OpenAICodex;
}

function isQuotaLimited(quota: CodexQuota) {
    return Boolean(quota.error || quota.rate_limit?.limit_reached || quota.rate_limit?.allowed === false || quota.code_review_rate_limit?.limit_reached || quota.code_review_rate_limit?.allowed === false || quota.additional_rate_limits?.some((item) => item.rate_limit?.limit_reached || item.rate_limit?.allowed === false) || quota.credits?.overage_limit_reached);
}

function QuotaSummary({ label, value, tone = 'default' }: { label: string; value: string | number; tone?: 'default' | 'success' | 'warning' }) {
    return <div className="rounded-xl border border-border/60 bg-card/70 px-2.5 py-2"><div className="truncate text-[10px] text-muted-foreground">{label}</div><div className={cn('mt-0.5 text-base font-semibold tabular-nums', tone === 'success' && 'text-emerald-600 dark:text-emerald-400', tone === 'warning' && 'text-orange-600 dark:text-orange-400')}>{value}</div></div>;
}

// ChannelCircuitPanel 展示当前被熔断冻结的 key×模型条目及剩余冷却，
// 并提供一键"立即恢复"（清除熔断，无需等待冷却）。无冻结条目时不渲染。
function ChannelCircuitPanel({ channelId }: { channelId: number }) {
    const t = useTranslations('channel.detail.circuit');
    const { data: entries } = useChannelCircuit(channelId);
    const resetCircuit = useResetChannelCircuit(channelId);
    if (!entries?.length) return null;
    return (
        <section className="space-y-3">
            <div className="flex items-center justify-between">
                <SectionTitle icon={ShieldAlert}>{t('title')}</SectionTitle>
                <Button size="sm" variant="outline" className="h-7 rounded-xl px-2 text-xs" disabled={resetCircuit.isPending} onClick={() => resetCircuit.mutate(undefined)}>
                    {resetCircuit.isPending ? t('resetting') : t('resetAll')}
                </Button>
            </div>
            <div className="overflow-hidden rounded-2xl border bg-card">
                {entries.map((entry) => (
                    <div key={`${entry.channel_key_id}-${entry.model_name}`} className="flex items-center gap-3 border-b p-3 transition-colors last:border-0 hover:bg-accent/5 sm:p-4">
                        <div className={cn('size-2 shrink-0 rounded-full', entry.state === 'open' ? 'bg-destructive' : 'bg-orange-500')} />
                        <span className="min-w-0 flex-1 truncate font-mono text-sm" title={entry.model_name}>{entry.model_name}</span>
                        <div className="flex shrink-0 items-center gap-2">
                            <Badge variant="secondary" className={cn('h-5 px-1.5 text-[10px]', entry.state === 'open' ? 'bg-red-500/15 text-red-700 dark:text-red-400' : 'bg-orange-500/15 text-orange-700 dark:text-orange-400')}>
                                {entry.state === 'open' ? t('stateOpen') : t('stateHalfOpen')}
                            </Badge>
                            {entry.remaining_cooldown_seconds > 0 && (
                                <span className="whitespace-nowrap text-xs text-muted-foreground">{t('remaining', { seconds: entry.remaining_cooldown_seconds })}</span>
                            )}
                            <Button size="sm" variant="ghost" className="h-6 rounded-lg px-2 text-xs" disabled={resetCircuit.isPending} onClick={() => resetCircuit.mutate(entry.model_name)}>
                                {t('reset')}
                            </Button>
                        </div>
                    </div>
                ))}
            </div>
        </section>
    );
}

function RuntimeLists({ channel }: { channel: Channel }) {
    const t = useTranslations('channel.detail');
    const { data: runtimeURLs } = useChannelRuntimeURLs(channel.id);
    return (
        <>
            <section className="space-y-3">
                <SectionTitle icon={Globe}>{t('sections.baseUrls')}</SectionTitle>
                <div className="overflow-hidden rounded-2xl border bg-card">
                    {runtimeURLs?.length ? runtimeURLs.map((url) => (
                        <div key={url.url} className="flex items-center gap-3 border-b p-3 transition-colors last:border-0 hover:bg-accent/5 sm:p-4">
                            <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-muted text-[10px] font-semibold">{url.rank || '-'}</span>
                            <span className="min-w-0 flex-1 truncate font-mono text-sm select-all" title={url.url}>{url.url}</span>
                            <span className="hidden shrink-0 text-[10px] text-muted-foreground sm:inline">{t(`urlReasons.${url.selection_reason}`)}</span>
                            {url.cooldown_remaining_seconds ? <Badge variant="secondary" className="h-5 shrink-0 bg-red-500/15 px-1.5 text-[10px] text-red-700 dark:text-red-400">{t('urlCooldown', { seconds: url.cooldown_remaining_seconds })}</Badge> : null}
                            {url.latency_ms ? <Badge variant="secondary" className={cn('h-5 shrink-0 px-1.5 text-[10px]', url.latency_ms < 300 ? 'bg-green-500/15 text-green-700 dark:text-green-400' : url.latency_ms < 1000 ? 'bg-orange-500/15 text-orange-700 dark:text-orange-400' : 'bg-red-500/15 text-red-700 dark:text-red-400')}>{url.latency_ms}ms</Badge> : <Badge variant="outline" className="h-5 shrink-0 px-1.5 text-[10px]">{t('urlUnmeasured')}</Badge>}
                        </div>
                    )) : channel.base_urls?.map((url, index) => (
                        <div key={`${url.url}-${index}`} className="flex items-center justify-between border-b p-3 transition-colors last:border-0 hover:bg-accent/5 sm:p-4">
                            <span className="min-w-0 truncate font-mono text-sm select-all">{url.url}</span>
                            <Badge variant="secondary" className="h-5 px-1.5 text-xs">{url.delay}ms</Badge>
                        </div>
                    ))}
                    {!channel.base_urls?.length && <div className="p-4 text-center text-sm text-muted-foreground">{t('noBaseUrls')}</div>}
                </div>
            </section>
            <section className="space-y-3">
                <SectionTitle icon={Key}>{t('sections.keys')}</SectionTitle>
                <div className="overflow-hidden rounded-2xl border bg-card">
                    {channel.keys?.map((key) => (
                        <div key={key.id} className="flex items-center gap-3 border-b p-3 transition-colors last:border-0 hover:bg-accent/5 sm:p-4">
                            <div className={cn('size-2 shrink-0 rounded-full', key.enabled ? 'bg-emerald-500' : 'bg-destructive')} />
                            <span className="min-w-0 flex-1 truncate font-mono text-sm">{isCodexChannel(channel) ? t('codexOAuthMasked') : maskChannelKey(key.channel_key)}</span>
                            {key.remark && <span className="max-w-24 truncate text-xs text-muted-foreground" title={key.remark}>{key.remark}</span>}
                            <div className="flex shrink-0 items-center gap-2">
                                {key.last_use_time_stamp > 0 && <span className="hidden whitespace-nowrap text-xs text-muted-foreground sm:inline-block">{new Date(key.last_use_time_stamp * 1000).toLocaleString()}</span>}
                                {key.status_code !== 0 && <Badge variant="secondary" className={cn('h-5 px-1.5 text-[10px]', key.status_code === 200 ? 'bg-green-500/15 text-green-700 dark:text-green-400' : key.status_code === 401 || key.status_code === 403 || key.status_code === 429 || key.status_code >= 500 ? 'bg-red-500/15 text-red-700 dark:text-red-400' : 'bg-orange-500/15 text-orange-700 dark:text-orange-400')}>{key.status_code}</Badge>}
                                <Badge variant="secondary" className="h-5 px-1.5 text-[10px]">{formatMoney(key.total_cost).formatted.value}{formatMoney(key.total_cost).formatted.unit}</Badge>
                            </div>
                        </div>
                    ))}
                    {!channel.keys?.length && <div className="p-4 text-center text-sm text-muted-foreground">{t('noKeys')}</div>}
                </div>
            </section>
        </>
    );
}

function maskChannelKey(key: string): string {
    return key.length > 10 ? `${key.slice(0, 4)}...${key.slice(-4)}` : key;
}

function MetricSection({ title, icon, children }: { title: string; icon: React.ComponentType<{ className?: string }>; children: React.ReactNode }) {
    return <section className="space-y-3"><SectionTitle icon={icon}>{title}</SectionTitle><dl className="grid grid-cols-1 gap-3 sm:grid-cols-2">{children}</dl></section>;
}

function SectionTitle({ icon: Icon, children }: { icon: React.ComponentType<{ className?: string }>; children: React.ReactNode }) {
    return <h4 className="flex items-center gap-2 text-xs font-semibold uppercase tracking-wider text-muted-foreground"><Icon className="size-3.5" />{children}</h4>;
}

function SummaryMetric({ icon: Icon, label, value, unit, color }: { icon: React.ComponentType<{ className?: string }>; label: string; value: string | number; unit?: string; color: string }) {
    return <div className="rounded-2xl border bg-card p-3 sm:p-4"><dt className="mb-2 flex items-center gap-2 text-xs font-medium text-muted-foreground"><Icon className={`size-4 ${color}`} />{label}</dt><dd className={`text-xl font-bold sm:text-2xl ${color}`}>{value}<span className="ml-1 text-xs font-normal text-muted-foreground">{unit}</span></dd></div>;
}

function DetailMetric({ icon: Icon, label, value, unit, color = 'text-card-foreground' }: { icon?: React.ComponentType<{ className?: string }>; label: string; value: string | number; unit?: string; color?: string }) {
    return <div className="rounded-2xl border bg-card p-3 transition-colors hover:bg-accent/5 sm:p-4"><dt className="mb-2 flex items-center gap-2 text-xs text-muted-foreground">{Icon && <Icon className={`size-4 ${color}`} />}{label}</dt><dd className={`text-2xl font-bold ${color}`}>{value}<span className="ml-1 text-sm font-normal text-muted-foreground">{unit}</span></dd></div>;
}
