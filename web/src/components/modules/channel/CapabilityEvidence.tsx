'use client';

import { AlertTriangle, FlaskConical, Loader2, ShieldCheck } from 'lucide-react';
import { useTranslations } from 'next-intl';
import {
    type CapabilityEvidence,
    type Channel,
    useChannelCapabilities,
    useProbeChannelCapabilities,
} from '@/api/endpoints/channel';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { cn } from '@/lib/utils';
import { capabilityDisplayStatus, capabilityStatusClass } from './capability-evidence';

export function CapabilityEvidencePanel({ channel }: { channel: Channel }) {
    const t = useTranslations('channel.detail.capabilities');
    const evidence = useChannelCapabilities(channel.id);
    const probe = useProbeChannelCapabilities(channel.id);

    return (
        <section className="space-y-3">
            <div className="flex items-center justify-between gap-3">
                <h4 className="flex min-w-0 items-center gap-2 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                    <ShieldCheck className="size-3.5 shrink-0" />
                    <span className="truncate">{t('title')}</span>
                </h4>
                <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    disabled={probe.isPending}
                    onClick={() => probe.mutate({})}
                    className="h-8 shrink-0 rounded-md"
                >
                    {probe.isPending ? <Loader2 className="size-4 animate-spin" /> : <FlaskConical className="size-4" />}
                    {probe.isPending ? t('probing') : t('probe')}
                </Button>
            </div>

            {probe.isError && (
                <div className="flex items-start gap-2 rounded-md border border-destructive/30 bg-destructive/5 p-3 text-xs text-destructive">
                    <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
                    <span className="break-words">{probe.error instanceof Error ? probe.error.message : t('probeFailed')}</span>
                </div>
            )}
            {probe.data && (
                <p className="text-xs text-muted-foreground">
                    {t('queued', { accepted: probe.data.accepted, requested: probe.data.requested })}
                </p>
            )}

            <div className="overflow-hidden rounded-md border bg-card">
                {evidence.isLoading && <div className="p-4 text-center text-sm text-muted-foreground">{t('loading')}</div>}
                {evidence.isError && <div className="p-4 text-center text-sm text-destructive">{t('loadFailed')}</div>}
                {!evidence.isLoading && !evidence.isError && !evidence.data?.length && (
                    <div className="p-4 text-center text-sm text-muted-foreground">{t('empty')}</div>
                )}
                {evidence.data?.map((item) => <CapabilityRow key={item.id} item={item} />)}
            </div>
        </section>
    );
}

function CapabilityRow({ item }: { item: CapabilityEvidence }) {
    const t = useTranslations('channel.detail.capabilities');
    const scope = item.key_remark ? `${t('keyScope', { id: item.channel_key_id })} · ${item.key_remark}` : t('keyScope', { id: item.channel_key_id });
    const displayStatus = capabilityDisplayStatus(item);
    return (
        <div className="space-y-2 border-b p-3 last:border-0 sm:p-4">
            <div className="flex min-w-0 items-center gap-2">
                <span className="min-w-0 flex-1 truncate font-mono text-sm font-medium" title={item.model}>{item.model}</span>
                <Badge variant="secondary" className="h-5 shrink-0 rounded px-1.5 text-[10px]">{t(`kinds.${item.capability}`)}</Badge>
                <Badge variant="secondary" className={cn('h-5 shrink-0 rounded px-1.5 text-[10px]', capabilityStatusClass(displayStatus))}>
                    {displayStatus === 'stale' ? t('stale') : t(`statuses.${displayStatus}`)}
                </Badge>
            </div>
            <div className="grid gap-1 text-xs text-muted-foreground sm:grid-cols-2">
                <span className="truncate" title={scope}>{scope}</span>
                <span className="truncate font-mono" title={item.wire_protocol}>{item.wire_protocol}</span>
                {item.endpoint && <span className="truncate font-mono" title={item.endpoint}>{item.endpoint}</span>}
                <time dateTime={item.probed_at}>{t('probedAt', { time: new Date(item.probed_at).toLocaleString() })}</time>
                {item.error_class && <span className="truncate text-destructive" title={item.error_message}>{t('errorClass', { classification: item.error_class })}</span>}
            </div>
        </div>
    );
}
