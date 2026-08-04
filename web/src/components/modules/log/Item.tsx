'use client';

import { useMemo } from 'react';
import { ArrowDownToLine, ArrowRight, ArrowUpFromLine, BrainCircuit, Clock, Cpu, DatabaseZap, DollarSign, KeyRound, Pin, Zap } from 'lucide-react';
import { useLocale, useTranslations } from 'next-intl';
import { type RelayLog } from '@/api/endpoints/log';
import { TooltipProvider } from '@/components/animate-ui/components/animate/tooltip';
import { Badge } from '@/components/ui/badge';
import { MorphingDialog, MorphingDialogTrigger } from '@/components/ui/morphing-dialog';
import { getModelIcon } from '@/lib/model-icons';
import { parseUsageCacheTokens } from '@/lib/usage-cache-tokens';
import { cn } from '@/lib/utils';
import { RetryBadgeWithTooltip } from './AttemptHistory';
import { formatFirstTokenMetric, formatLogDuration, formatLogTime, shouldShowReasoningTokens } from './format';
import { LogDetails } from './LogDetails';

export function LogCard({ log }: { log: RelayLog }) {
    const t = useTranslations('log.card');
    const locale = useLocale();
    const { Avatar: ModelAvatar, color } = useMemo(() => getModelIcon(log.actual_model_name), [log.actual_model_name]);
    const usage = useMemo(() => parseUsageCacheTokens(log.response_content), [log.response_content]);
    const requestKey = log.request_api_key_name?.trim() ?? '';
    const attempts = log.attempts ?? [];
    const multiple = attempts.length > 1;
    const hasError = Boolean(log.error);
    const successfulAttempt = attempts.find((attempt) => attempt.status === 'success') ?? attempts.at(-1);
    const keyRemark = successfulAttempt?.channel_key_remark?.trim() ?? '';

    return (
        <TooltipProvider>
            <MorphingDialog>
                <MorphingDialogTrigger className={cn('w-full rounded-3xl border bg-card text-left', hasError ? 'border-destructive/40' : 'border-border')}>
                    <article className={cn('grid grid-cols-[auto_1fr] gap-4 p-4', hasError ? 'items-start' : 'items-center')}>
                        <ModelAvatar size={40} />
                        <div className="flex min-w-0 flex-col gap-3">
                            <header className="flex min-w-0 items-center gap-2 text-sm">
                                <span className="truncate font-semibold text-card-foreground" title={log.request_model_name}>{log.request_model_name}</span>
                                <ArrowRight className="size-3.5 shrink-0 text-muted-foreground/50" />
                                {multiple ? <RetryBadgeWithTooltip channelName={log.channel_name} brandColor={color} attempts={attempts} /> : <Badge variant="secondary" className="shrink-0 px-1.5 py-0 text-xs" style={{ backgroundColor: `${color}15`, color }}>{log.channel_name}</Badge>}
                                <span className="truncate text-muted-foreground" title={log.actual_model_name}>{log.actual_model_name}</span>
                                {!multiple && keyRemark && <span className="flex max-w-[8rem] shrink-0 items-center gap-1 truncate text-xs text-muted-foreground" title={keyRemark}><KeyRound className="size-3 text-sky-500" />{keyRemark}</span>}
                                {attempts.some((attempt) => attempt.sticky) && <Pin className="size-3.5 shrink-0 text-amber-500" />}
                            </header>
                            <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-xs tabular-nums text-muted-foreground">
                                <Metric icon={Clock}>{formatLogTime(log.time, locale)}</Metric>
                                {requestKey && <Metric icon={KeyRound}>{requestKey}</Metric>}
                                <Metric icon={Zap}>{formatFirstTokenMetric(log.ftut, log.error, { observed: t('firstToken'), timeout: t('firstTokenTimeout'), unavailable: t('firstTokenUnavailable') })}</Metric>
                                <Metric icon={Cpu}>{t('totalTime')} {formatLogDuration(log.use_time)}</Metric>
                                <Metric icon={ArrowDownToLine}>{t('input')} {log.input_tokens.toLocaleString()}</Metric>
                                <Metric icon={ArrowUpFromLine}>{t('output')} {log.output_tokens.toLocaleString()}</Metric>
                                {shouldShowReasoningTokens(log.reasoning_tokens) && <Metric icon={BrainCircuit}>{t('reasoning')} {log.reasoning_tokens.toLocaleString()}</Metric>}
                                {usage?.cachedReadTokens ? <Metric icon={DatabaseZap}>{t('cacheRead')} {usage.cachedReadTokens.toLocaleString()}</Metric> : null}
                                {usage?.cachedWriteTokens ? <Metric icon={DatabaseZap}>{t('cacheWrite')} {usage.cachedWriteTokens.toLocaleString()}</Metric> : null}
                                <Metric icon={DollarSign}>{t('cost')} {Number(log.cost).toFixed(6)}</Metric>
                            </div>
                            {hasError && <div className="overflow-hidden rounded-xl border border-destructive/20 bg-destructive/10 p-2.5"><p className="line-clamp-2 text-xs text-destructive">{log.error}</p></div>}
                        </div>
                    </article>
                </MorphingDialogTrigger>
                <LogDetails log={log} />
            </MorphingDialog>
        </TooltipProvider>
    );
}

function Metric({ icon: Icon, children }: { icon: React.ComponentType<{ className?: string }>; children: React.ReactNode }) {
    return <div className="flex items-center gap-1.5"><Icon className="size-3.5 shrink-0" /><span>{children}</span></div>;
}
