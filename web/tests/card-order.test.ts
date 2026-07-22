import { describe, expect, it } from 'vitest';
import {
    parseCardIDs,
    parseCardSortMode,
    reconcileCardIDs,
    sortCardItems,
    type CardSortMode,
} from '@/components/modules/toolbar/card-order';

type Item = { id: number; name: string };
const items: Item[] = [
    { id: 1, name: 'Zulu' },
    { id: 2, name: 'Alpha' },
    { id: 3, name: 'Mike' },
];

const sort = (mode: CardSortMode, pinnedIDs: number[] = [], orderedIDs: number[] = []) =>
    sortCardItems(items, {
        getID: (item) => item.id,
        getName: (item) => item.name,
        mode,
        pinnedIDs,
        orderedIDs,
    }).map((item) => item.id);

describe('card order', () => {
    it('parses valid IDs and rejects malformed values', () => {
        expect(parseCardIDs('[3, 3, 1]')).toEqual([3, 1]);
        expect(parseCardIDs('[0, -1]')).toEqual([]);
        expect(parseCardIDs('not-json')).toEqual([]);
    });

    it('reconciles stale and new IDs while preserving saved order', () => {
        expect(reconcileCardIDs([3, 99, 3], [1, 2, 3, 4])).toEqual([3, 1, 2, 4]);
        expect(reconcileCardIDs([3, 2], [2, 3])).toEqual([3, 2]);
    });

    it('supports automatic name and creation sorting', () => {
        expect(sort('name-asc')).toEqual([2, 3, 1]);
        expect(sort('name-desc')).toEqual([1, 3, 2]);
        expect(sort('created-asc')).toEqual([1, 2, 3]);
        expect(sort('created-desc')).toEqual([3, 2, 1]);
        expect(parseCardSortMode('unknown')).toBe('name-asc');
    });

    it('keeps pinned items ahead of manual order', () => {
        expect(sort('manual', [3], [2, 1, 3])).toEqual([3, 2, 1]);
        expect(sort('manual', [3, 1], [1, 2, 3])).toEqual([1, 3, 2]);
    });
});
