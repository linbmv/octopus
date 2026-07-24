'use client';

import { useMemo, useState } from 'react';
import { AlertTriangle, Diff, Loader2, Upload } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { type SelfHealingCompareResult, useSelfHealingCompare } from '@/api/endpoints/channel';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { cn } from '@/lib/utils';
import { parseGoldenSampleInput, splitDiffEntry } from './golden-sample';

export function GoldenSampleDiff({ channelId, disabled }: { channelId: number; disabled?: boolean }) {
    const t = useTranslations('channel.detail.selfHealing.goldenSample');
    const compare = useSelfHealingCompare(channelId);
    const [raw, setRaw] = useState('');
    const [parseError, setParseError] = useState<string | null>(null);

    const result = compare.data?.compare;
    const requestError = useMemo(() => {
        if (!compare.error) return null;
        return compare.error instanceof Error ? compare.error.message : t('compareFailed');
    }, [compare.error, t]);

    const submit = () => {
        const parsed = parseGoldenSampleInput(raw);
        if ('error' in parsed) {
            setParseError(t(`parseErrors.${parsed.error}`));
            return;
        }
        setParseError(null);
        compare.mutate(parsed.sample);
    };

    return (
        <div className="space-y-2 rounded-md border bg-card p-3 text-xs">
            <div className="flex items-center gap-2 font-medium text-card-foreground">
                <Diff className="size-3.5 shrink-0" />
                {t('title')}
            </div>
            <p className="text-muted-foreground">{t('hint')}</p>
            <textarea
                className="h-28 w-full resize-y rounded-md border bg-background p-2 font-mono text-[11px] text-foreground"
                placeholder={t('placeholder')}
                value={raw}
                onChange={(event) => setRaw(event.target.value)}
                disabled={disabled || compare.isPending}
            />
            <div className="flex items-center gap-2">
                <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    className="h-8 rounded-md"
                    disabled={disabled || compare.isPending || !raw.trim()}
                    onClick={submit}
                >
                    {compare.isPending ? <Loader2 className="size-4 animate-spin" /> : <Upload className="size-4" />}
                    {t('compare')}
                </Button>
            </div>
            {(parseError || requestError) && (
                <div className="flex items-start gap-2 rounded-md border border-destructive/30 bg-destructive/5 p-2 text-destructive">
                    <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
                    <span className="break-words">{parseError ?? requestError}</span>
                </div>
            )}
            {result && <CompareResultView result={result} />}
        </div>
    );
}

function CompareResultView({ result }: { result: SelfHealingCompareResult }) {
    const t = useTranslations('channel.detail.selfHealing.goldenSample');
    const diffs = [
        ...result.url_diff.map((entry) => ({ kind: 'url' as const, entry })),
        ...result.header_diff.map((entry) => ({ kind: 'header' as const, entry })),
        ...result.body_diff.map((entry) => ({ kind: 'body' as const, entry })),
    ];
    return (
        <div className="space-y-2 border-t pt-2">
            <div className="text-muted-foreground">
                {t('sampleSummary', { method: result.sample.method, path: result.sample.path || '/' })}
            </div>
            {diffs.length === 0 ? (
                <div className="text-emerald-700 dark:text-emerald-400">{t('noDiff')}</div>
            ) : (
                <ul className="space-y-1">
                    {diffs.map(({ kind, entry }) => {
                        const { action, subject } = splitDiffEntry(entry);
                        return (
                            <li key={`${kind}:${entry}`} className="flex items-center gap-2">
                                <Badge variant="outline" className="h-5 shrink-0 px-1.5 text-[10px]">
                                    {t(`kinds.${kind}`)}
                                </Badge>
                                <Badge
                                    variant="secondary"
                                    className={cn(
                                        'h-5 shrink-0 px-1.5 text-[10px]',
                                        action === 'added'
                                            ? 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-400'
                                            : action === 'removed'
                                              ? 'bg-red-500/15 text-red-700 dark:text-red-400'
                                              : 'bg-amber-500/15 text-amber-700 dark:text-amber-400',
                                    )}
                                >
                                    {t(`actions.${action}`)}
                                </Badge>
                                <span className="min-w-0 truncate font-mono" title={subject}>
                                    {subject}
                                </span>
                            </li>
                        );
                    })}
                </ul>
            )}
            {(result.suggested_variants?.length ?? 0) > 0 && (
                <div className="space-y-1 border-t pt-2">
                    <div className="font-medium">{t('suggestions')}</div>
                    {result.suggested_variants?.map((variant) => (
                        <div key={variant.variant_id} className="text-muted-foreground">
                            <span className="font-mono">{variant.dimension}</span> · {variant.description}
                        </div>
                    ))}
                    <p className="text-muted-foreground">{t('suggestionsHint')}</p>
                </div>
            )}
        </div>
    );
}
