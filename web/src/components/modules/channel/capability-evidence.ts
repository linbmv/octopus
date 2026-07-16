import type { CapabilityEvidence, CapabilityStatus } from '@/api/endpoints/channel';

export type CapabilityDisplayStatus = CapabilityStatus | 'stale';

export function capabilityDisplayStatus(evidence: Pick<CapabilityEvidence, 'fresh' | 'status'>): CapabilityDisplayStatus {
    return evidence.fresh ? evidence.status : 'stale';
}

export function capabilityStatusClass(status: CapabilityDisplayStatus): string {
    switch (status) {
    case 'supported':
        return 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-400';
    case 'transient':
        return 'bg-amber-500/15 text-amber-700 dark:text-amber-400';
    case 'stale':
        return 'bg-muted text-muted-foreground';
    default:
        return 'bg-red-500/15 text-red-700 dark:text-red-400';
    }
}
