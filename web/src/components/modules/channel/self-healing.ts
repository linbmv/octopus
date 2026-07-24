import type {
    RequestShapeArtifact,
    SelfHealingPatch,
    SelfHealingPatchStatus,
    SelfHealingRootCause,
    SelfHealingSession,
    SelfHealingSessionStatus,
    SelfHealingStatus,
} from '@/api/endpoints/channel';

/** Capacity-class root causes never produce configuration patches. */
const CAPACITY_ROOT_CAUSES: ReadonlySet<SelfHealingRootCause> = new Set([
    'capacity',
    'rate_limit',
    'auth',
    'network',
    'model_access',
]);

export type SelfHealingDisplayState =
    | 'disabled'
    | 'healthy'
    | 'watching'
    | 'capacity'
    | 'suspected_drift'
    | 'diagnosing'
    | 'patch_ready'
    | 'rolled_back';

export function isCapacityRootCause(rootCause: SelfHealingRootCause | undefined): boolean {
    return !!rootCause && CAPACITY_ROOT_CAUSES.has(rootCause);
}

export function canGeneratePatch(rootCause: SelfHealingRootCause | undefined): boolean {
    return rootCause === 'protocol_drift' || rootCause === 'waf_or_client_fingerprint' || rootCause === 'decode';
}

export function deriveSelfHealingState(status: SelfHealingStatus | undefined): SelfHealingDisplayState {
    if (!status) return 'disabled';
    if (!status.global_enabled || !status.channel_enabled) return 'disabled';

    const sessions = status.sessions ?? [];
    const patches = status.patches ?? [];

    if (patches.some((patch) => patch.status === 'previewed')) return 'patch_ready';
    if (patches.some((patch) => patch.status === 'rolled_back' || patch.status === 'rollback_failed')) {
        return 'rolled_back';
    }
    if (sessions.some((session) => session.status === 'queued' || session.status === 'running')) {
        return 'diagnosing';
    }

    const latestSession = sessions[0];
    if (latestSession && isCapacityRootCause(latestSession.root_cause)) return 'capacity';
    if (latestSession && canGeneratePatch(latestSession.root_cause)) return 'suspected_drift';
    if ((status.baselines?.length ?? 0) > 0) return 'watching';
    return 'healthy';
}

export function selfHealingStateClass(state: SelfHealingDisplayState): string {
    switch (state) {
    case 'healthy':
    case 'watching':
        return 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-400';
    case 'diagnosing':
    case 'patch_ready':
        return 'bg-amber-500/15 text-amber-700 dark:text-amber-400';
    case 'capacity':
    case 'suspected_drift':
    case 'rolled_back':
        return 'bg-red-500/15 text-red-700 dark:text-red-400';
    default:
        return 'bg-muted text-muted-foreground';
    }
}

export function sessionStatusClass(status: SelfHealingSessionStatus): string {
    switch (status) {
    case 'completed':
        return 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-400';
    case 'queued':
    case 'running':
        return 'bg-amber-500/15 text-amber-700 dark:text-amber-400';
    case 'failed':
    case 'canceled':
    case 'expired':
        return 'bg-red-500/15 text-red-700 dark:text-red-400';
    default:
        return 'bg-muted text-muted-foreground';
    }
}

export function patchStatusClass(status: SelfHealingPatchStatus): string {
    switch (status) {
    case 'applied':
        return 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-400';
    case 'previewed':
    case 'applying':
        return 'bg-amber-500/15 text-amber-700 dark:text-amber-400';
    default:
        return 'bg-red-500/15 text-red-700 dark:text-red-400';
    }
}

/** Returns true when a response payload appears to leak secrets (for tests/UI guards). */
export function containsSensitiveSelfHealingLeak(value: unknown): boolean {
    const text = JSON.stringify(value ?? {}).toLowerCase();
    return (
        text.includes('"authorization"') ||
        text.includes('"channel_key"') ||
        text.includes('bearer ') ||
        text.includes('sk-') ||
        text.includes('api-key')
    );
}

export function latestReadyPatch(patches: SelfHealingPatch[] | undefined): SelfHealingPatch | undefined {
    return (patches ?? []).find((patch) => patch.status === 'previewed');
}

export function latestAppliedPatch(patches: SelfHealingPatch[] | undefined): SelfHealingPatch | undefined {
    return (patches ?? []).find((patch) => patch.status === 'applied');
}

export function formatShapeSummary(shape: RequestShapeArtifact | undefined): string {
    if (!shape) return '—';
    const headers = Object.keys(shape.headers ?? {}).slice(0, 6).join(', ');
    const body = (shape.body?.top_level_keys ?? []).slice(0, 6).join(', ');
    const path = [shape.method, shape.url].filter(Boolean).join(' ');
    return [path, headers && `headers: ${headers}`, body && `body: ${body}`].filter(Boolean).join(' · ') || '—';
}

export function defaultDiagnosticRootCause(session?: SelfHealingSession): SelfHealingRootCause {
    if (session && canGeneratePatch(session.root_cause)) return session.root_cause;
    return 'protocol_drift';
}
