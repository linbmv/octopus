'use client';

import { useMemo } from 'react';
import { AlertTriangle } from 'lucide-react';
import { Bar, BarChart, CartesianGrid, XAxis, YAxis } from 'recharts';
import { useTranslations } from 'next-intl';
import { useStatsErrorLevels, type AttemptErrorLevel, type StatsErrorLevelCounts } from '@/api/endpoints/stats';
import { ChartContainer, ChartTooltip, ChartTooltipContent } from '@/components/ui/chart';
import { ERROR_LEVEL_ORDER, errorLevelPercent, errorLevelTotal, formatTrendBucket } from './utils';

const levelStyles: Record<AttemptErrorLevel, { bar: string; text: string; dot: string }> = {
    key: { bar: 'bg-amber-500', text: 'text-amber-600 dark:text-amber-400', dot: 'var(--chart-4)' },
    channel: { bar: 'bg-destructive', text: 'text-destructive', dot: 'var(--destructive)' },
    client: { bar: 'bg-sky-500', text: 'text-sky-600 dark:text-sky-400', dot: 'var(--chart-1)' },
};

function ErrorCounts({ counts }: { counts: StatsErrorLevelCounts }) {
    const t = useTranslations('errorObservability');
    const total = errorLevelTotal(counts);
    return (
        <>
            <dl className="grid grid-cols-3 gap-2">
                {ERROR_LEVEL_ORDER.map((level) => (
                    <div key={level} className="rounded-2xl border bg-background/60 p-3">
                        <dt className="text-xs text-muted-foreground">{t(`levels.${level}`)}</dt>
                        <dd className={`mt-1 text-xl font-semibold ${levelStyles[level].text}`}>{counts[level]}</dd>
                    </div>
                ))}
            </dl>
            <div
                className="flex h-2 overflow-hidden rounded-full bg-muted"
                role="img"
                aria-label={t('distributionAria', { total })}
            >
                {ERROR_LEVEL_ORDER.map((level) => (
                    <span
                        key={level}
                        className={levelStyles[level].bar}
                        style={{ width: `${errorLevelPercent(counts[level], counts)}%` }}
                        title={`${t(`levels.${level}`)}: ${counts[level]}`}
                    />
                ))}
            </div>
        </>
    );
}

function BoundedNotice({ truncated, capacity }: { truncated: boolean; capacity: number }) {
    const t = useTranslations('errorObservability');
    if (!truncated) return null;
    return (
        <p className="flex items-center gap-1.5 text-xs text-amber-600 dark:text-amber-400">
            <AlertTriangle className="size-3.5" />
            {t('truncated', { capacity })}
        </p>
    );
}

export function ErrorLevelDistribution() {
    const { data, isLoading, isError } = useStatsErrorLevels();
    const t = useTranslations('errorObservability');
    const total = data ? errorLevelTotal(data.counts) : 0;

    return (
        <section className="space-y-4 rounded-3xl border border-card-border bg-card p-4 text-card-foreground custom-shadow">
            <header className="flex items-start justify-between gap-3">
                <div>
                    <h3 className="font-semibold">{t('title')}</h3>
                    <p className="text-xs text-muted-foreground">{t('description')}</p>
                </div>
                <span className="whitespace-nowrap text-xs text-muted-foreground">{t('last24Hours')}</span>
            </header>
            {isLoading ? (
                <p className="text-sm text-muted-foreground">{t('loading')}</p>
            ) : isError ? (
                <p className="text-sm text-destructive">{t('loadError')}</p>
            ) : data && total > 0 ? (
                <>
                    <ErrorCounts counts={data.counts} />
                    <BoundedNotice truncated={data.truncated} capacity={data.capacity} />
                </>
            ) : (
                <p className="text-sm text-muted-foreground">{t('noData')}</p>
            )}
        </section>
    );
}

export function ChannelErrorOverview({ channelId }: { channelId: number }) {
    const { data, isLoading, isError } = useStatsErrorLevels(channelId);
    const t = useTranslations('errorObservability');
    const chartData = useMemo(() => data?.trend.map((point) => ({
        ...point,
        time: formatTrendBucket(point.bucket_start),
    })) ?? [], [data]);
    const total = data ? errorLevelTotal(data.counts) : 0;

    return (
        <section className="space-y-3">
            <header>
                <h4 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">{t('channelTitle')}</h4>
                <p className="text-xs text-muted-foreground">{t('channelDescription')}</p>
            </header>
            {isLoading ? (
                <p className="text-sm text-muted-foreground">{t('loading')}</p>
            ) : isError ? (
                <p className="text-sm text-destructive">{t('loadError')}</p>
            ) : data && total > 0 ? (
                <div className="space-y-3 rounded-2xl border bg-card p-3">
                    <ErrorCounts counts={data.counts} />
                    {chartData.length > 0 && (
                        <ChartContainer
                            className="h-36 w-full"
                            config={{
                                key: { label: t('levels.key'), color: levelStyles.key.dot },
                                channel: { label: t('levels.channel'), color: levelStyles.channel.dot },
                            }}
                        >
                            <BarChart accessibilityLayer data={chartData}>
                                <CartesianGrid vertical={false} />
                                <XAxis dataKey="time" tickLine={false} axisLine={false} minTickGap={24} />
                                <YAxis allowDecimals={false} tickLine={false} axisLine={false} width={24} />
                                <ChartTooltip content={<ChartTooltipContent />} />
                                <Bar dataKey="key" fill="var(--color-key)" radius={[3, 3, 0, 0]} />
                                <Bar dataKey="channel" fill="var(--color-channel)" radius={[3, 3, 0, 0]} />
                            </BarChart>
                        </ChartContainer>
                    )}
                    <BoundedNotice truncated={data.truncated} capacity={data.capacity} />
                </div>
            ) : (
                <p className="rounded-2xl border bg-card p-3 text-sm text-muted-foreground">{t('noData')}</p>
            )}
        </section>
    );
}
