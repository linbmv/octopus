'use client';

import { useMemo } from 'react';
import { Activity, AlertTriangle, Gauge, Timer } from 'lucide-react';
import { useHealthStatus, type HealthState } from '@/api/endpoints/health';
import { Badge } from '@/components/ui/badge';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { cn } from '@/lib/utils';

function formatMs(value: number) {
    if (!Number.isFinite(value) || value <= 0) return '-';
    if (value >= 1000) return `${(value / 1000).toFixed(1)}s`;
    return `${Math.round(value)}ms`;
}

function percent(value: number) {
    if (!Number.isFinite(value)) return '-';
    return `${Math.round(value * 100)}%`;
}

function scoreTone(score: number) {
    if (score >= 0.8) return 'text-emerald-600';
    if (score >= 0.5) return 'text-amber-600';
    return 'text-destructive';
}

function stateRisk(state: HealthState) {
    if (state.timeout_policy.timeout_rate_backoff || state.stats.consecutive_timeout > 0) return 'watch';
    if (state.score < 0.5) return 'risk';
    return 'normal';
}

export function Health() {
    const { data, isLoading } = useHealthStatus();
    const states = useMemo(() => data?.states ?? [], [data?.states]);
    const sortedStates = useMemo(() => {
        return [...states].sort((a, b) => a.score - b.score || b.stats.auto_first_token_timeout_count - a.stats.auto_first_token_timeout_count);
    }, [states]);

    const summary = useMemo(() => {
        const total = states.length;
        const autoTimeouts = states.reduce((sum, item) => sum + item.stats.auto_first_token_timeout_count, 0);
        const backoff = states.filter((item) => item.timeout_policy.timeout_rate_backoff).length;
        const lowScore = states.filter((item) => item.score < 0.5).length;
        const avgScore = total === 0 ? 0 : states.reduce((sum, item) => sum + item.score, 0) / total;
        return { total, autoTimeouts, backoff, lowScore, avgScore };
    }, [states]);

    return (
        <div className="space-y-4 pb-24 md:pb-0">
            <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
                <SummaryCard icon={Activity} label="States" value={String(summary.total)} />
                <SummaryCard icon={Gauge} label="Avg Score" value={percent(summary.avgScore)} />
                <SummaryCard icon={Timer} label="Auto Timeouts" value={String(summary.autoTimeouts)} />
                <SummaryCard icon={AlertTriangle} label="Backoff" value={String(summary.backoff + summary.lowScore)} />
            </div>

            <Card className="rounded-lg">
                <CardHeader className="py-4">
                    <CardTitle className="text-base">Channel Health</CardTitle>
                </CardHeader>
                <CardContent className="p-0">
                    <Table>
                        <TableHeader>
                            <TableRow>
                                <TableHead>Channel</TableHead>
                                <TableHead>Model</TableHead>
                                <TableHead>Score</TableHead>
                                <TableHead>Success</TableHead>
                                <TableHead>P95</TableHead>
                                <TableHead>Timeout</TableHead>
                                <TableHead>Policy</TableHead>
                                <TableHead>Auto</TableHead>
                            </TableRow>
                        </TableHeader>
                        <TableBody>
                            {isLoading ? (
                                <TableRow><TableCell colSpan={8} className="text-muted-foreground">Loading</TableCell></TableRow>
                            ) : sortedStates.length === 0 ? (
                                <TableRow><TableCell colSpan={8} className="text-muted-foreground">No health states</TableCell></TableRow>
                            ) : sortedStates.map((state) => (
                                <TableRow key={`${state.channel_id}:${state.key_id}:${state.model}`}>
                                    <TableCell className="font-medium">{state.channel_id}:{state.key_id}</TableCell>
                                    <TableCell className="max-w-[220px] truncate">{state.model}</TableCell>
                                    <TableCell className={cn('font-semibold', scoreTone(state.score))}>{percent(state.score)}</TableCell>
                                    <TableCell>{percent(state.stats.success_rate)}</TableCell>
                                    <TableCell>{formatMs(state.stats.first_token_p95_ms)}</TableCell>
                                    <TableCell>{formatMs(state.adaptive_timeout_ms)}</TableCell>
                                    <TableCell>
                                        <div className="flex flex-wrap gap-1">
                                            <Badge variant="outline">{state.timeout_policy.source}</Badge>
                                            {state.timeout_policy.slow_model_profile && <Badge variant="secondary">slow</Badge>}
                                            {state.timeout_policy.timeout_rate_backoff && <Badge variant="destructive">backoff</Badge>}
                                        </div>
                                    </TableCell>
                                    <TableCell>
                                        <span className={cn(stateRisk(state) === 'normal' ? 'text-muted-foreground' : 'text-amber-600')}>
                                            {state.stats.auto_first_token_timeout_count}
                                        </span>
                                    </TableCell>
                                </TableRow>
                            ))}
                        </TableBody>
                    </Table>
                </CardContent>
            </Card>
        </div>
    );
}

function SummaryCard({ icon: Icon, label, value }: { icon: typeof Activity; label: string; value: string }) {
    return (
        <Card className="rounded-lg">
            <CardContent className="flex items-center justify-between p-4">
                <div>
                    <div className="text-xs text-muted-foreground">{label}</div>
                    <div className="mt-1 text-2xl font-semibold tracking-normal">{value}</div>
                </div>
                <Icon className="h-5 w-5 text-muted-foreground" />
            </CardContent>
        </Card>
    );
}
