import { describe, expect, it } from 'vitest';
import {
    ERROR_LEVEL_STATS_MAX_WINDOW_HOURS,
    buildStatsErrorLevelsPath,
} from '@/api/endpoints/stats';
import {
    ERROR_LEVEL_ORDER,
    errorLevelPercent,
    errorLevelTotal,
} from '@/components/modules/error-observability/utils';

describe('error-level observability contracts', () => {
    it('builds bounded global and channel statistics queries', () => {
        expect(buildStatsErrorLevelsPath()).toBe('/api/v1/stats/error-levels?window_hours=24');
        expect(buildStatsErrorLevelsPath(48, 17)).toBe('/api/v1/stats/error-levels?window_hours=48&channel_id=17');

        for (const invalid of [0, ERROR_LEVEL_STATS_MAX_WINDOW_HOURS + 1, 1.5]) {
            expect(() => buildStatsErrorLevelsPath(invalid)).toThrow(RangeError);
        }
        for (const invalidChannel of [0, -1, 1.5]) {
            expect(() => buildStatsErrorLevelsPath(24, invalidChannel)).toThrow(RangeError);
        }
    });

    it('keeps the UI distribution exhaustive and zero-safe', () => {
        const counts = { key: 2, channel: 1, client: 1 };
        expect(ERROR_LEVEL_ORDER).toEqual(['key', 'channel', 'client']);
        expect(errorLevelTotal(counts)).toBe(4);
        expect(errorLevelPercent(counts.key, counts)).toBe(50);
        expect(errorLevelPercent(0, { key: 0, channel: 0, client: 0 })).toBe(0);
        expect(ERROR_LEVEL_ORDER.reduce((sum, level) => sum + errorLevelPercent(counts[level], counts), 0)).toBe(100);
    });
});
