'use client';

import { useCallback, useMemo, useState } from 'react';
import { SettingKey, useSetSetting, useSettingList } from '@/api/endpoints/setting';
import {
    parseCardIDs,
    parseCardSortMode,
    type CardOrderPage,
    type CardSortMode,
} from './card-order';

const PAGE_SETTINGS: Record<CardOrderPage, { orderedIDs: string; sortMode: string }> = {
    channel: {
        orderedIDs: SettingKey.ChannelCardOrderedIDs,
        sortMode: SettingKey.ChannelCardSortMode,
    },
    group: {
        orderedIDs: SettingKey.GroupCardOrderedIDs,
        sortMode: SettingKey.GroupCardSortMode,
    },
};

export function useGlobalCardOrder(page: CardOrderPage) {
    const { data: settings = [] } = useSettingList();
    const setOrderedSetting = useSetSetting();
    const setModeSetting = useSetSetting();
    const pageSettings = PAGE_SETTINGS[page];
    const [modeOverride, setModeOverride] = useState<CardSortMode | null>(null);
    const [orderedOverride, setOrderedOverride] = useState<number[] | null>(null);

    const serverValues = useMemo(() => ({
        mode: parseCardSortMode(settings.find((setting) => setting.key === pageSettings.sortMode)?.value),
        orderedIDs: parseCardIDs(settings.find((setting) => setting.key === pageSettings.orderedIDs)?.value),
    }), [pageSettings.orderedIDs, pageSettings.sortMode, settings]);

    const mode = modeOverride ?? serverValues.mode;
    const orderedIDs = orderedOverride ?? serverValues.orderedIDs;

    const saveOrderedIDs = useCallback((nextIDs: number[]) => {
        const next = [...new Set(nextIDs.filter((id) => Number.isInteger(id) && id > 0))];
        setOrderedOverride(next);
        setOrderedSetting.mutate(
            { key: pageSettings.orderedIDs, value: JSON.stringify(next) },
            {
                onSuccess: () => setOrderedOverride(null),
                onError: () => setOrderedOverride(null),
            }
        );
    }, [pageSettings.orderedIDs, setOrderedSetting]);

    const setSortMode = useCallback((nextMode: CardSortMode, initialIDs?: number[]) => {
        if (initialIDs) saveOrderedIDs(initialIDs);
        setModeOverride(nextMode);
        setModeSetting.mutate(
            { key: pageSettings.sortMode, value: nextMode },
            {
                onSuccess: () => setModeOverride(null),
                onError: () => setModeOverride(null),
            }
        );
    }, [pageSettings.sortMode, saveOrderedIDs, setModeSetting]);

    return {
        mode,
        orderedIDs,
        saveOrderedIDs,
        setSortMode,
        isSaving: setOrderedSetting.isPending || setModeSetting.isPending,
    };
}
