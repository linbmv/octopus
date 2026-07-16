import { describe, expect, it } from 'vitest';
import type { CapabilityStatus } from '@/api/endpoints/channel';
import { capabilityDisplayStatus } from '@/components/modules/channel/capability-evidence';

describe('capability evidence presentation', () => {
    it('does not turn provider negatives into support', () => {
        const fakeProviderResults: CapabilityStatus[] = [
            'supported',
            'not_implemented',
            'unauthorized',
            'supported',
        ];
        expect(fakeProviderResults.map((status) => capabilityDisplayStatus({ fresh: true, status }))).toEqual([
            'supported',
            'not_implemented',
            'unauthorized',
            'supported',
        ]);
    });

    it('labels expired evidence as stale regardless of its old result', () => {
        expect(capabilityDisplayStatus({ fresh: false, status: 'supported' })).toBe('stale');
        expect(capabilityDisplayStatus({ fresh: false, status: 'unauthorized' })).toBe('stale');
    });
});
