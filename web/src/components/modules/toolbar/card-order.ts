export const CARD_SORT_MODES = ['name-asc', 'name-desc', 'created-asc', 'created-desc', 'manual'] as const;
export type CardSortMode = (typeof CARD_SORT_MODES)[number];
export type CardOrderPage = 'channel' | 'group';

export function parseCardIDs(value: string | undefined): number[] {
    if (!value) return [];
    try {
        const parsed: unknown = JSON.parse(value);
        return Array.isArray(parsed) && parsed.every((id): id is number => Number.isInteger(id) && id > 0)
            ? [...new Set(parsed)]
            : [];
    } catch {
        return [];
    }
}

export function parseCardSortMode(value: string | undefined): CardSortMode {
    return CARD_SORT_MODES.includes(value as CardSortMode) ? value as CardSortMode : 'name-asc';
}

export function reconcileCardIDs(orderedIDs: number[], itemIDs: number[]): number[] {
    const available = new Set(itemIDs);
    const known = new Set<number>();
    const result: number[] = [];

    for (const id of orderedIDs) {
        if (available.has(id) && !known.has(id)) {
            known.add(id);
            result.push(id);
        }
    }
    for (const id of itemIDs) {
        if (!known.has(id)) {
            known.add(id);
            result.push(id);
        }
    }
    return result;
}

export function sortCardItems<T>(
    items: T[],
    options: {
        getID: (item: T) => number;
        getName: (item: T) => string;
        mode: CardSortMode;
        pinnedIDs: number[];
        orderedIDs: number[];
    }
): T[] {
    const pinOrder = new Map(options.pinnedIDs.map((id, index) => [id, index]));
    const manualOrder = new Map(options.orderedIDs.map((id, index) => [id, index]));
    const compareFallback = (a: T, b: T) => options.getID(a) - options.getID(b);

    return [...items].sort((a, b) => {
        const aID = options.getID(a);
        const bID = options.getID(b);
        const aPinned = pinOrder.has(aID);
        const bPinned = pinOrder.has(bID);

        if (aPinned !== bPinned) return aPinned ? -1 : 1;

        if (options.mode === 'manual') {
            const aOrder = manualOrder.get(aID);
            const bOrder = manualOrder.get(bID);
            if (aOrder !== undefined || bOrder !== undefined) {
                if (aOrder === undefined) return 1;
                if (bOrder === undefined) return -1;
                if (aOrder !== bOrder) return aOrder - bOrder;
            }
            return compareFallback(a, b);
        }

        if (aPinned && bPinned) {
            const aPin = pinOrder.get(aID)!;
            const bPin = pinOrder.get(bID)!;
            if (aPin !== bPin) return aPin - bPin;
        }

        if (options.mode === 'name-asc' || options.mode === 'name-desc') {
            const diff = options.getName(a).localeCompare(options.getName(b));
            return diff === 0 ? compareFallback(a, b) : options.mode === 'name-asc' ? diff : -diff;
        }

        const diff = compareFallback(a, b);
        return options.mode === 'created-asc' ? diff : -diff;
    });
}
