'use client';

import { useCallback, useMemo, useState } from 'react';
import { useSetSetting, useSettingList } from '@/api/endpoints/setting';

function parsePinnedIDs(value: string | undefined): number[] {
    if (!value) return [];
    try {
        const parsed: unknown = JSON.parse(value);
        return Array.isArray(parsed) && parsed.every((id): id is number => Number.isInteger(id) && id > 0)
            ? parsed
            : [];
    } catch {
        return [];
    }
}

export function useGlobalPinnedIDs(settingKey: string) {
    const { data: settings = [] } = useSettingList();
    const setSetting = useSetSetting();
    const [pinnedOverride, setPinnedOverride] = useState<number[] | null>(null);

    const serverPinnedIDs = useMemo(
        () => parsePinnedIDs(settings.find((setting) => setting.key === settingKey)?.value),
        [settingKey, settings]
    );
    const pinnedIDs = pinnedOverride ?? serverPinnedIDs;

    const togglePinned = useCallback((id: number) => {
        const next = pinnedIDs.includes(id)
            ? pinnedIDs.filter((pinnedID) => pinnedID !== id)
            : [id, ...pinnedIDs];

        setPinnedOverride(next);
        setSetting.mutate(
            { key: settingKey, value: JSON.stringify(next) },
            {
                onSuccess: () => setPinnedOverride(null),
                onError: () => setPinnedOverride(null),
            }
        );
    }, [pinnedIDs, setSetting, settingKey]);

    return { pinnedIDs, togglePinned };
}
