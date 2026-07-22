import { describe, expect, it } from 'vitest';
import { formatStatsMetrics } from '@/api/endpoints/stats';
import type { StatsMetrics } from '@/api/contracts';

describe('stats metric formatting', () => {
    it('keeps reasoning tokens as an output breakdown without double counting totals', () => {
        const metrics: StatsMetrics = {
            input_token: 100,
            output_token: 40,
            reasoning_token: 30,
            input_cost: 0.1,
            output_cost: 0.2,
            wait_time: 500,
            request_success: 2,
            request_failed: 1,
        };

        const formatted = formatStatsMetrics(metrics);
        expect(formatted.input_token.raw).toBe(100);
        expect(formatted.output_token.raw).toBe(40);
        expect(formatted.reasoning_token.raw).toBe(30);
        expect(formatted.total_token.raw).toBe(140);
        expect(formatted.total_cost.raw).toBeCloseTo(0.3);
    });
});
