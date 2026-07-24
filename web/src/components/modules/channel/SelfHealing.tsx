'use client';

import { useMemo, useState } from 'react';
import {
    AlertTriangle,
    Eye,
    FlaskConical,
    Loader2,
    RotateCcw,
    ShieldAlert,
    Wrench,
} from 'lucide-react';
import { useTranslations } from 'next-intl';
import {
    type Channel,
    type SelfHealingRootCause,
    useApplySelfHealingPatch,
    useChannelSelfHealing,
    useCreateSelfHealingDiagnostic,
    useRollbackSelfHealingPatch,
    useSelfHealingDiagnostic,
    useSelfHealingPreview,
} from '@/api/endpoints/channel';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { cn } from '@/lib/utils';
import { GoldenSampleDiff } from './GoldenSampleDiff';
import {
    canGeneratePatch,
    deriveSelfHealingState,
    formatShapeSummary,
    isCapacityRootCause,
    latestAppliedPatch,
    latestReadyPatch,
    patchStatusClass,
    selfHealingStateClass,
    sessionStatusClass,
} from './self-healing';

const DIAGNOSTIC_ROOT_CAUSES: SelfHealingRootCause[] = [
    'protocol_drift',
    'waf_or_client_fingerprint',
    'decode',
    'capacity',
    'rate_limit',
    'auth',
    'network',
    'unknown',
];

export function SelfHealingPanel({ channel }: { channel: Channel }) {
    const t = useTranslations('channel.detail.selfHealing');
    const statusQuery = useChannelSelfHealing(channel.id);
    const preview = useSelfHealingPreview(channel.id);
    const createDiagnostic = useCreateSelfHealingDiagnostic(channel.id);
    const applyPatch = useApplySelfHealingPatch(channel.id);
    const rollbackPatch = useRollbackSelfHealingPatch(channel.id);

    const [rootCause, setRootCause] = useState<SelfHealingRootCause>('protocol_drift');
    const [selectedDiagnosticId, setSelectedDiagnosticId] = useState<string | null>(null);
    const [confirmApplyId, setConfirmApplyId] = useState<string | null>(null);
    const [confirmRollbackId, setConfirmRollbackId] = useState<string | null>(null);

    const detail = useSelfHealingDiagnostic(channel.id, selectedDiagnosticId);
    const status = statusQuery.data;
    const displayState = deriveSelfHealingState(status);
    const readyPatch = latestReadyPatch(status?.patches);
    const appliedPatch = latestAppliedPatch(status?.patches);
    const latestBaseline = status?.baselines?.[0];
    const latestSession = status?.sessions?.[0];

    const gatesOff = !status?.global_enabled || !status?.channel_enabled || !status?.worker_available;
    const busy =
        preview.isPending ||
        createDiagnostic.isPending ||
        applyPatch.isPending ||
        rollbackPatch.isPending;

    const actionError = useMemo(() => {
        const err = preview.error ?? createDiagnostic.error ?? applyPatch.error ?? rollbackPatch.error;
        if (!err) return null;
        return err instanceof Error ? err.message : t('actionFailed');
    }, [preview.error, createDiagnostic.error, applyPatch.error, rollbackPatch.error, t]);

    return (
        <section className="space-y-3">
            <div className="flex items-center justify-between gap-3">
                <h4 className="flex min-w-0 items-center gap-2 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                    <Wrench className="size-3.5 shrink-0" />
                    <span className="truncate">{t('title')}</span>
                </h4>
                <Badge variant="secondary" className={cn('h-5 shrink-0 rounded px-1.5 text-[10px]', selfHealingStateClass(displayState))}>
                    {t(`states.${displayState}`)}
                </Badge>
            </div>

            <p className="text-xs text-muted-foreground">{t('hint')}</p>

            <div className="grid gap-2 rounded-md border bg-card p-3 text-xs sm:grid-cols-2">
                <GateRow label={t('gates.global')} on={!!status?.global_enabled} onLabel={t('gates.on')} offLabel={t('gates.off')} />
                <GateRow label={t('gates.channel')} on={!!status?.channel_enabled} onLabel={t('gates.on')} offLabel={t('gates.off')} />
                <GateRow label={t('gates.worker')} on={!!status?.worker_available} onLabel={t('gates.on')} offLabel={t('gates.off')} />
                <GateRow label={t('gates.baselines')} on={!!status?.capture_success_baselines} onLabel={t('gates.on')} offLabel={t('gates.off')} />
                <div className="sm:col-span-2 text-muted-foreground">
                    {t('configVersion', { version: status?.channel_config_version ?? channel.config_version ?? 0 })}
                </div>
            </div>

            {statusQuery.isLoading && <div className="p-3 text-center text-sm text-muted-foreground">{t('loading')}</div>}
            {statusQuery.isError && (
                <div className="flex items-start gap-2 rounded-md border border-destructive/30 bg-destructive/5 p-3 text-xs text-destructive">
                    <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
                    <span>{t('loadFailed')}</span>
                </div>
            )}
            {actionError && (
                <div className="flex items-start gap-2 rounded-md border border-destructive/30 bg-destructive/5 p-3 text-xs text-destructive">
                    <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
                    <span className="break-words">{actionError}</span>
                </div>
            )}

            {latestBaseline && (
                <div className="rounded-md border bg-card p-3 text-xs space-y-1">
                    <div className="font-medium text-card-foreground">{t('baseline.title')}</div>
                    <div className="text-muted-foreground">{t('baseline.model', { model: latestBaseline.model })}</div>
                    <div className="font-mono text-muted-foreground truncate" title={latestBaseline.endpoint_fingerprint}>
                        {t('baseline.endpoint', { fingerprint: latestBaseline.endpoint_fingerprint })}
                    </div>
                    <div className="text-muted-foreground">
                        {t('baseline.capturedAt', { time: new Date(latestBaseline.captured_at).toLocaleString() })}
                    </div>
                    <div className="font-mono text-[11px] text-muted-foreground break-all">
                        {formatShapeSummary(latestBaseline.request_shape)}
                    </div>
                </div>
            )}

            {latestSession && isCapacityRootCause(latestSession.root_cause) && (
                <div className="flex items-start gap-2 rounded-md border border-amber-500/30 bg-amber-500/5 p-3 text-xs text-amber-800 dark:text-amber-300">
                    <ShieldAlert className="mt-0.5 size-3.5 shrink-0" />
                    <span>{t('capacityNoPatch', { cause: t(`rootCauses.${latestSession.root_cause}`) })}</span>
                </div>
            )}

            <div className="flex flex-wrap items-center gap-2">
                <label className="flex min-w-0 flex-1 items-center gap-2 text-xs text-muted-foreground">
                    <span className="shrink-0">{t('rootCause')}</span>
                    <select
                        className="h-8 min-w-0 flex-1 rounded-md border bg-background px-2 text-xs text-foreground"
                        value={rootCause}
                        onChange={(event) => setRootCause(event.target.value as SelfHealingRootCause)}
                        disabled={busy}
                    >
                        {DIAGNOSTIC_ROOT_CAUSES.map((cause) => (
                            <option key={cause} value={cause}>
                                {t(`rootCauses.${cause}`)}
                            </option>
                        ))}
                    </select>
                </label>
            </div>

            <div className="flex flex-wrap gap-2">
                <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    disabled={busy || gatesOff}
                    onClick={() => preview.mutate({ root_cause: rootCause })}
                    className="h-8 rounded-md"
                >
                    {preview.isPending ? <Loader2 className="size-4 animate-spin" /> : <Eye className="size-4" />}
                    {t('actions.preview')}
                </Button>
                <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    disabled={busy || gatesOff || !canGeneratePatch(rootCause)}
                    onClick={() =>
                        createDiagnostic.mutate(
                            { mode: 'live', root_cause: rootCause },
                            {
                                onSuccess: (data) => {
                                    if (data.session?.id) setSelectedDiagnosticId(data.session.id);
                                },
                            },
                        )
                    }
                    className="h-8 rounded-md"
                    title={!canGeneratePatch(rootCause) ? t('capacityNoPatch', { cause: t(`rootCauses.${rootCause}`) }) : undefined}
                >
                    {createDiagnostic.isPending ? <Loader2 className="size-4 animate-spin" /> : <FlaskConical className="size-4" />}
                    {t('actions.diagnose')}
                </Button>
            </div>

            {preview.data && (
                <div className="rounded-md border bg-card p-3 text-xs space-y-2">
                    <div className="font-medium">{t('preview.title')}</div>
                    {preview.data.plan.early_stop && (
                        <p className="text-muted-foreground">{t('preview.earlyStop', { reason: preview.data.plan.stop_reason || '—' })}</p>
                    )}
                    <p className="text-muted-foreground">
                        {t('preview.variants', { count: preview.data.plan.variants?.length ?? 0 })}
                    </p>
                    {(preview.data.artifacts ?? []).slice(0, 3).map((artifact, index) => (
                        <div key={index} className="font-mono text-[11px] text-muted-foreground break-all border-t pt-2">
                            {formatShapeSummary(artifact)}
                            {preview.data.shape_diffs?.[index]?.length ? (
                                <div className="mt-1 text-amber-700 dark:text-amber-300">
                                    {preview.data.shape_diffs[index].join(', ')}
                                </div>
                            ) : null}
                        </div>
                    ))}
                </div>
            )}

            {(status?.sessions?.length ?? 0) > 0 && (
                <div className="overflow-hidden rounded-md border bg-card">
                    <div className="border-b px-3 py-2 text-xs font-medium text-muted-foreground">{t('sessions.title')}</div>
                    {status?.sessions?.slice(0, 5).map((session) => (
                        <button
                            key={session.id}
                            type="button"
                            className={cn(
                                'flex w-full flex-col gap-1 border-b p-3 text-left text-xs last:border-0 hover:bg-accent/5',
                                selectedDiagnosticId === session.id && 'bg-accent/10',
                            )}
                            onClick={() => setSelectedDiagnosticId(session.id)}
                        >
                            <div className="flex items-center gap-2">
                                <span className="min-w-0 flex-1 truncate font-mono" title={session.id}>{session.id}</span>
                                <Badge variant="secondary" className={cn('h-5 shrink-0 rounded px-1.5 text-[10px]', sessionStatusClass(session.status))}>
                                    {t(`sessionStatuses.${session.status}`)}
                                </Badge>
                            </div>
                            <div className="text-muted-foreground">
                                {t(`rootCauses.${session.root_cause}`)} · {session.model} · {session.attempt_count}/{session.max_attempts}
                            </div>
                            {session.error_reason && <div className="truncate text-destructive" title={session.error_reason}>{session.error_reason}</div>}
                        </button>
                    ))}
                </div>
            )}

            {selectedDiagnosticId && detail.data && (
                <div className="space-y-2 rounded-md border bg-card p-3 text-xs">
                    <div className="font-medium">{t('detail.title')}</div>
                    <div className="text-muted-foreground">
                        {t('detail.summary', {
                            status: t(`sessionStatuses.${detail.data.session.status}`),
                            attempts: detail.data.attempts?.length ?? 0,
                            patches: detail.data.patches?.length ?? 0,
                        })}
                    </div>
                    {(detail.data.attempts ?? []).slice(0, 8).map((attempt) => (
                        <div key={attempt.id} className="border-t pt-2 space-y-1">
                            <div className="flex items-center gap-2">
                                <span className="font-mono">{attempt.variant_id}</span>
                                {attempt.changed_dimension && (
                                    <Badge variant="outline" className="h-5 px-1.5 text-[10px]">{attempt.changed_dimension}</Badge>
                                )}
                                <Badge variant="secondary" className={cn('h-5 px-1.5 text-[10px]', attempt.success ? 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-400' : 'bg-muted text-muted-foreground')}>
                                    {attempt.success ? t('detail.success') : t('detail.failed')}
                                </Badge>
                            </div>
                            {attempt.shape_diff?.length ? (
                                <div className="font-mono text-[11px] text-muted-foreground">{attempt.shape_diff.join(', ')}</div>
                            ) : null}
                            {attempt.error_reason && <div className="text-destructive">{attempt.error_reason}</div>}
                        </div>
                    ))}
                </div>
            )}

            {(status?.patches?.length ?? 0) > 0 && (
                <div className="overflow-hidden rounded-md border bg-card">
                    <div className="border-b px-3 py-2 text-xs font-medium text-muted-foreground">{t('patches.title')}</div>
                    {status?.patches?.slice(0, 5).map((patch) => (
                        <div key={patch.id} className="space-y-2 border-b p-3 text-xs last:border-0">
                            <div className="flex items-center gap-2">
                                <span className="min-w-0 flex-1 truncate font-mono" title={patch.id}>{patch.id}</span>
                                <Badge variant="secondary" className={cn('h-5 shrink-0 rounded px-1.5 text-[10px]', patchStatusClass(patch.status))}>
                                    {t(`patchStatuses.${patch.status}`)}
                                </Badge>
                                <Badge variant="outline" className="h-5 shrink-0 px-1.5 text-[10px]">{t(`confidence.${patch.confidence}`)}</Badge>
                            </div>
                            <div className="text-muted-foreground">
                                {(patch.changes ?? []).map((change) => change.field).join(', ') || t('patches.noChanges')}
                            </div>
                            {patch.apply_error && <div className="text-destructive">{patch.apply_error}</div>}
                            {patch.verification_reason && <div className="text-muted-foreground">{patch.verification_reason}</div>}

                            {patch.status === 'previewed' && (
                                <Button
                                    type="button"
                                    size="sm"
                                    variant={confirmApplyId === patch.id ? 'destructive' : 'default'}
                                    disabled={busy || gatesOff}
                                    className="h-8 rounded-md"
                                    onClick={() => {
                                        if (confirmApplyId !== patch.id) {
                                            setConfirmApplyId(patch.id);
                                            return;
                                        }
                                        applyPatch.mutate(
                                            { diagnosticId: patch.diagnostic_session_id, patchId: patch.id },
                                            { onSettled: () => setConfirmApplyId(null) },
                                        );
                                    }}
                                >
                                    {applyPatch.isPending && confirmApplyId === patch.id ? (
                                        <Loader2 className="size-4 animate-spin" />
                                    ) : (
                                        <Wrench className="size-4" />
                                    )}
                                    {confirmApplyId === patch.id ? t('actions.confirmApply') : t('actions.apply')}
                                </Button>
                            )}
                            {patch.status === 'applied' && (
                                <Button
                                    type="button"
                                    size="sm"
                                    variant={confirmRollbackId === patch.id ? 'destructive' : 'outline'}
                                    disabled={busy}
                                    className="h-8 rounded-md"
                                    onClick={() => {
                                        if (confirmRollbackId !== patch.id) {
                                            setConfirmRollbackId(patch.id);
                                            return;
                                        }
                                        rollbackPatch.mutate(patch.id, { onSettled: () => setConfirmRollbackId(null) });
                                    }}
                                >
                                    {rollbackPatch.isPending && confirmRollbackId === patch.id ? (
                                        <Loader2 className="size-4 animate-spin" />
                                    ) : (
                                        <RotateCcw className="size-4" />
                                    )}
                                    {confirmRollbackId === patch.id ? t('actions.confirmRollback') : t('actions.rollback')}
                                </Button>
                            )}
                        </div>
                    ))}
                </div>
            )}

            {readyPatch && displayState === 'patch_ready' && (
                <p className="text-xs text-amber-700 dark:text-amber-300">{t('patchReadyHint')}</p>
            )}
            {appliedPatch && displayState === 'rolled_back' && (
                <p className="text-xs text-muted-foreground">{t('rolledBackHint')}</p>
            )}

            <GoldenSampleDiff channelId={channel.id} disabled={busy || gatesOff} />
        </section>
    );
}

function GateRow({ label, on, onLabel, offLabel }: { label: string; on: boolean; onLabel: string; offLabel: string }) {
    return (
        <div className="flex items-center justify-between gap-2">
            <span className="text-muted-foreground">{label}</span>
            <Badge variant="secondary" className={cn('h-5 px-1.5 text-[10px]', on ? 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-400' : 'bg-muted text-muted-foreground')}>
                {on ? onLabel : offLabel}
            </Badge>
        </div>
    );
}
