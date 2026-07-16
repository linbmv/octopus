'use client';

import { ArrowDown, Pin, RotateCw } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { type ChannelAttempt } from '@/api/endpoints/log';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/animate-ui/components/animate/tooltip';
import { Badge } from '@/components/ui/badge';
import { cn } from '@/lib/utils';
import { formatLogDuration } from './format';

export function formatAttemptKey(attempt: ChannelAttempt): string {
    return attempt.channel_key_remark?.trim() ?? '';
}

function attemptStatusClass(status: ChannelAttempt['status']): string {
    if (status === 'success') return 'bg-primary/15 text-primary';
    if (status === 'circuit_break') return 'bg-amber-500/15 text-amber-600 dark:text-amber-400';
    if (status === 'client_canceled') return 'bg-sky-500/15 text-sky-700 dark:text-sky-300';
    if (status === 'skipped' || status === 'redirect') return 'bg-muted text-muted-foreground';
    return 'bg-destructive/15 text-destructive';
}

export function AttemptStatusBadges({ attempt }: { attempt: ChannelAttempt }) {
    const t = useTranslations('log.card');
    const tErrorLevel = useTranslations('errorObservability.levels');
    return (
        <>
            <Badge className={cn('h-5 shrink-0 border-0 px-1.5 text-[10px] font-bold uppercase shadow-none', attemptStatusClass(attempt.status))}>{t(`attemptStatus.${attempt.status}`)}</Badge>
            {attempt.error_level && <Badge variant="outline" className="h-5 shrink-0 px-1.5 text-[10px]" title={attempt.error_reason}>{tErrorLevel(attempt.error_level)}</Badge>}
        </>
    );
}

export function RetryBadgeWithTooltip({ channelName, brandColor, attempts }: { channelName: string; brandColor: string; attempts: ChannelAttempt[] }) {
    return (
        <Tooltip>
            <TooltipTrigger asChild>
                <Badge variant="secondary" className="shrink-0 cursor-help px-1.5 py-0 text-xs" style={{ backgroundColor: `${brandColor}15`, color: brandColor }}>
                    <RotateCw className="mr-1 size-3 opacity-80" />{channelName}
                </Badge>
            </TooltipTrigger>
            <TooltipContent className="flex min-w-[280px] flex-col gap-1 rounded-3xl border bg-card p-2 shadow-sm">
                {attempts.map((attempt, index) => (
                    <div key={`${attempt.attempt_num}-${index}`} className="flex w-full flex-col">
                        <div className="flex items-center gap-2 rounded-md px-2 py-1.5 transition-colors hover:bg-muted/50">
                            <AttemptStatusBadges attempt={attempt} />
                            <div className="flex min-w-0 flex-1 flex-col">
                                <div className="flex min-w-0 items-center gap-1.5"><span className="truncate text-xs font-semibold text-foreground">{attempt.channel_name}</span>{attempt.sticky && <Pin className="size-3 shrink-0 text-amber-500" />}</div>
                                <span className="text-[10px] text-muted-foreground">{[attempt.model_name, formatAttemptKey(attempt), formatLogDuration(attempt.duration)].filter(Boolean).join(' • ')}</span>
                            </div>
                        </div>
                        {index < attempts.length - 1 && <div className="flex justify-center py-0.5"><ArrowDown className="size-3 text-muted-foreground/30" /></div>}
                    </div>
                ))}
            </TooltipContent>
        </Tooltip>
    );
}

export function AttemptList({ attempts }: { attempts: ChannelAttempt[] }) {
    const t = useTranslations('log.card');
    return (
        <div className="flex flex-col gap-2">
            {attempts.map((attempt, index) => (
                <div key={`${attempt.attempt_num}-${index}`} className={cn('flex flex-col gap-2 rounded-xl border p-2.5 text-xs transition-colors', attempt.status === 'success' ? 'border-primary/20 bg-primary/5' : attempt.status === 'failed' ? 'border-destructive/20 bg-destructive/5' : 'border-border bg-muted/30')}>
                    <div className="flex items-start gap-2">
                        <AttemptStatusBadges attempt={attempt} />
                        <div className="min-w-0 flex-1">
                            <div className="flex min-w-0 items-center gap-1.5"><span className="truncate font-semibold text-foreground">{attempt.channel_name}</span>{attempt.sticky && <Pin className="size-3.5 shrink-0 text-amber-500" />}</div>
                            <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted-foreground"><span>{attempt.model_name}</span>{formatAttemptKey(attempt) && <span>{formatAttemptKey(attempt)}</span>}<span>{t('attemptNumber', { number: attempt.attempt_num })}</span></div>
                        </div>
                        <span className="font-mono tabular-nums text-muted-foreground">{formatLogDuration(attempt.duration)}</span>
                    </div>
                    {attempt.msg && <div className="border-l-2 border-destructive/30 pl-2 text-[11px] leading-relaxed text-destructive/90">{attempt.msg}</div>}
                </div>
            ))}
        </div>
    );
}
