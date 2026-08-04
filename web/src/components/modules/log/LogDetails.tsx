'use client';

import { useEffect, useMemo, useState } from 'react';
import { AlertCircle, ArrowRight, BrainCircuit, ChevronDown, ChevronUp, Clock, Cpu, DatabaseZap, DollarSign, KeyRound, Loader2, MessageSquare, Pin, RotateCw, Send, Zap } from 'lucide-react';
import { AnimatePresence, motion } from 'motion/react';
import { useLocale, useTranslations } from 'next-intl';
import { useTheme } from 'next-themes';
import JsonView from '@uiw/react-json-view';
import { githubDarkTheme } from '@uiw/react-json-view/githubDark';
import { githubLightTheme } from '@uiw/react-json-view/githubLight';
import { type RelayLog } from '@/api/endpoints/log';
import { CopyIconButton } from '@/components/common/CopyButton';
import { Badge } from '@/components/ui/badge';
import { MorphingDialogClose, MorphingDialogContainer, MorphingDialogContent, MorphingDialogDescription, MorphingDialogTitle, useMorphingDialog } from '@/components/ui/morphing-dialog';
import { getModelIcon } from '@/lib/model-icons';
import { parseUsageCacheTokens } from '@/lib/usage-cache-tokens';
import { cn } from '@/lib/utils';
import { AttemptList, RetryBadgeWithTooltip } from './AttemptHistory';
import { formatFirstTokenMetric, formatLogDuration, formatLogTime, shouldShowReasoningTokens } from './format';

export function LogDetails({ log }: { log: RelayLog }) {
    const t = useTranslations('log.card');
    const locale = useLocale();
    const { Avatar: ModelAvatar, color } = useMemo(() => getModelIcon(log.actual_model_name), [log.actual_model_name]);
    const usage = useMemo(() => parseUsageCacheTokens(log.response_content), [log.response_content]);
    const [expanded, setExpanded] = useState(false);
    const hasError = Boolean(log.error);
    const multiple = (log.attempts?.length ?? 0) > 1;
    return (
        <MorphingDialogContainer>
            <MorphingDialogContent className="relative flex h-[calc(100vh-2rem)] w-[calc(100vw-2rem)] flex-col overflow-hidden rounded-3xl bg-card px-6 py-4 text-card-foreground md:w-[80vw]">
                <MorphingDialogClose className="top-4 right-5 text-muted-foreground transition-colors hover:text-foreground" />
                <MorphingDialogTitle className="mb-3 flex items-center gap-2 text-sm">
                    <ModelAvatar size={28} /><span className="font-semibold">{log.request_model_name}</span><ArrowRight className="size-3.5 text-muted-foreground/50" />
                    {multiple ? <RetryBadgeWithTooltip channelName={log.channel_name} brandColor={color} attempts={log.attempts} /> : <Badge variant="secondary" className="px-1.5 py-0 text-xs" style={{ backgroundColor: `${color}15`, color }}>{log.channel_name}</Badge>}
                    <span className="text-muted-foreground">{log.actual_model_name}</span>{log.attempts?.some((attempt) => attempt.sticky) && <Pin className="size-3.5 text-amber-500" />}
                </MorphingDialogTitle>
                <MorphingDialogDescription className="min-h-0 flex-1">
                    <div className="flex h-full min-h-0 flex-col gap-4">
                        {(hasError || multiple) && (
                            <section className={cn('flex max-h-[40%] min-h-0 flex-initial flex-col overflow-hidden rounded-2xl border', hasError ? 'border-destructive/20 bg-destructive/5' : 'border-border/50 bg-secondary/30')}>
                                <button type="button" onClick={() => setExpanded((value) => !value)} className="flex shrink-0 items-center gap-2 px-3 py-2.5 text-left transition-colors hover:bg-muted/50">
                                    {hasError ? <AlertCircle className="size-4 text-destructive" /> : <RotateCw className="size-4 text-muted-foreground" />}
                                    <span className={cn('text-sm font-medium', hasError ? 'text-destructive' : 'text-secondary-foreground')}>{hasError ? t('errorInfo') : t('retryDetails')}</span>
                                    <div className="ml-auto flex items-center gap-2">{multiple && <Badge variant="outline" className="border-0 text-xs">{log.total_attempts || log.attempts.length} {t('attempts')}</Badge>}{expanded ? <ChevronUp className="size-4" /> : <ChevronDown className="size-4" />}</div>
                                </button>
                                {expanded && <div className="flex-1 space-y-4 overflow-auto p-3">{hasError && <div className="relative"><CopyIconButton text={log.error} className="absolute right-0 top-0 p-1 text-destructive/60" /><p className="pr-8 text-sm whitespace-pre-wrap text-destructive">{log.error}</p></div>}{multiple && <AttemptList attempts={log.attempts} />}</div>}
                            </section>
                        )}
                        <div className="grid min-h-0 flex-1 grid-cols-1 gap-4 md:grid-cols-2">
                            <ContentPanel icon={Send} title={t('requestContent')} tokens={log.input_tokens} content={log.request_content} fallback={t('noRequestContent')} />
                            <ContentPanel icon={MessageSquare} title={t('responseContent')} tokens={log.output_tokens} reasoningTokens={log.reasoning_tokens} content={log.response_content} fallback={t('noResponseContent')} cacheRead={usage?.cachedReadTokens} cacheWrite={usage?.cachedWriteTokens} />
                        </div>
                    </div>
                </MorphingDialogDescription>
                <footer className="mt-auto flex shrink-0 flex-wrap items-center gap-3 pt-4 text-xs text-muted-foreground md:gap-4">
                    <FooterMetric icon={Clock}>{formatLogTime(log.time, locale)}</FooterMetric>
                    {log.request_api_key_name?.trim() && <FooterMetric icon={KeyRound}>{log.request_api_key_name.trim()}</FooterMetric>}
                    <FooterMetric icon={Zap}>{t('firstTokenTime')}: {formatFirstTokenMetric(log.ftut, log.error, { observed: t('firstToken'), timeout: t('firstTokenTimeout'), unavailable: t('firstTokenUnavailable') })}</FooterMetric>
                    <FooterMetric icon={Cpu}>{t('totalTime')}: {formatLogDuration(log.use_time)}</FooterMetric>
                    <FooterMetric icon={DollarSign}>{t('cost')}: {Number(log.cost).toFixed(6)}</FooterMetric>
                </footer>
            </MorphingDialogContent>
        </MorphingDialogContainer>
    );
}

function DeferredJsonContent({ content, fallback }: { content?: string; fallback: string }) {
    const { resolvedTheme } = useTheme();
    const { isOpen } = useMorphingDialog();
    const [ready, setReady] = useState(false);
    const parsed = useMemo(() => { if (!content) return null; try { return JSON.parse(content); } catch { return content; } }, [content]);
    useEffect(() => { if (!isOpen) return; const timer = setTimeout(() => setReady(true), 300); return () => clearTimeout(timer); }, [isOpen]);
    if (!isOpen) return null;
    if (!content) return <pre className="p-4 text-xs whitespace-pre-wrap text-muted-foreground">{fallback}</pre>;
    if (!ready) return <div className="flex h-full items-center justify-center p-4"><Loader2 className="size-5 animate-spin text-muted-foreground" /></div>;
    if (typeof parsed === 'string') return <motion.pre initial={{ opacity: 0 }} animate={{ opacity: 1 }} className="p-4 font-mono text-xs whitespace-pre-wrap text-muted-foreground">{parsed}</motion.pre>;
    return <AnimatePresence><motion.div initial={{ opacity: 0 }} animate={{ opacity: 1 }} className="p-4"><JsonView value={parsed as object} style={{ ...(resolvedTheme === 'dark' ? githubDarkTheme : githubLightTheme), fontSize: '12px', backgroundColor: 'transparent' }} displayDataTypes={false} displayObjectSize={false} collapsed={false} /></motion.div></AnimatePresence>;
}

function ContentPanel({ icon: Icon, title, tokens, reasoningTokens, content, fallback, cacheRead, cacheWrite }: { icon: React.ComponentType<{ className?: string }>; title: string; tokens: number; reasoningTokens?: number; content: string; fallback: string; cacheRead?: number; cacheWrite?: number }) {
    const t = useTranslations('log.card');
    return <section className="flex min-h-0 flex-col overflow-hidden rounded-2xl border border-border bg-muted/30"><header className="flex shrink-0 items-center gap-2 border-b bg-muted/50 px-3 py-2.5"><Icon className="size-4" /><span className="text-sm font-medium">{title}</span><div className="ml-auto flex flex-wrap justify-end gap-1.5">{Boolean(cacheRead) && <Badge variant="secondary" className="text-xs text-emerald-600"><DatabaseZap className="mr-1 size-3" />{cacheRead}</Badge>}{Boolean(cacheWrite) && <Badge variant="secondary" className="text-xs text-amber-600"><DatabaseZap className="mr-1 size-3" />{cacheWrite}</Badge>}{shouldShowReasoningTokens(reasoningTokens ?? 0) && <Badge variant="secondary" className="text-xs text-sky-600"><BrainCircuit className="mr-1 size-3" />{reasoningTokens?.toLocaleString()} {t('reasoning')}</Badge>}<Badge variant="secondary" className="text-xs">{tokens.toLocaleString()} {t('tokens')}</Badge></div></header><div className="min-h-0 flex-1 overflow-auto"><DeferredJsonContent content={content} fallback={fallback} /></div></section>;
}

function FooterMetric({ icon: Icon, children }: { icon: React.ComponentType<{ className?: string }>; children: React.ReactNode }) {
    return <div className="flex min-w-0 items-center gap-1.5"><Icon className="size-3.5 shrink-0" /><span className="truncate tabular-nums">{children}</span></div>;
}
