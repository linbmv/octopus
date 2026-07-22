'use client';

import { useMemo } from 'react';
import { GroupCard } from './Card';
import { useGroupList } from '@/api/endpoints/group';
import { SettingKey } from '@/api/endpoints/setting';
import { useSearchStore, useToolbarViewOptionsStore } from '@/components/modules/toolbar';
import { sortCardItems } from '@/components/modules/toolbar/card-order';
import { useGlobalCardOrder } from '@/components/modules/toolbar/use-global-card-order';
import { useGlobalPinnedIDs } from '@/components/modules/toolbar/use-global-pinned-ids';
import { VirtualizedGrid } from '@/components/common/VirtualizedGrid';

export function Group() {
    const { data: groups } = useGroupList();
    const pageKey = 'group' as const;
    const searchTerm = useSearchStore((s) => s.getSearchTerm(pageKey));
    const filter = useToolbarViewOptionsStore((s) => s.groupFilter);
    const { mode: sortMode, orderedIDs } = useGlobalCardOrder(pageKey);
    const { pinnedIDs: pinnedGroupIds, togglePinned: toggleGroupPinned } = useGlobalPinnedIDs(
        SettingKey.GroupCardPinnedIDs
    );

    const sortedGroups = useMemo(() => {
        if (!groups) return [];
        return sortCardItems(groups, {
            getID: (group) => group.id,
            getName: (group) => group.name,
            mode: sortMode,
            pinnedIDs: pinnedGroupIds,
            orderedIDs,
        });
    }, [groups, orderedIDs, pinnedGroupIds, sortMode]);

    const visibleGroups = useMemo(() => {
        const term = searchTerm.toLowerCase().trim();
        const byName = !term ? sortedGroups : sortedGroups.filter((g) => g.name.toLowerCase().includes(term));

        if (filter === 'with-members') return byName.filter((g) => (g.items?.length || 0) > 0);
        if (filter === 'empty') return byName.filter((g) => (g.items?.length || 0) === 0);

        return byName;
    }, [sortedGroups, searchTerm, filter]);

    return (
        <VirtualizedGrid
            items={visibleGroups}
            columns={{ default: 1, md: 2, lg: 3 }}
            estimateItemHeight={520}
            getItemKey={(group, index) => group.id ?? `group-${index}`}
            renderItem={(group) => (
                <GroupCard
                    group={group}
                    pinned={group.id ? pinnedGroupIds.includes(group.id) : false}
                    onTogglePinned={toggleGroupPinned}
                />
            )}
        />
    );
}
