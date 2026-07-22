'use client';

import { useMemo } from 'react';
import { GroupCard } from './Card';
import { useGroupList } from '@/api/endpoints/group';
import { SettingKey } from '@/api/endpoints/setting';
import { useSearchStore, useToolbarViewOptionsStore } from '@/components/modules/toolbar';
import { useGlobalPinnedIDs } from '@/components/modules/toolbar/use-global-pinned-ids';
import { VirtualizedGrid } from '@/components/common/VirtualizedGrid';

export function Group() {
    const { data: groups } = useGroupList();
    const pageKey = 'group' as const;
    const searchTerm = useSearchStore((s) => s.getSearchTerm(pageKey));
    const sortField = useToolbarViewOptionsStore((s) => s.getSortField(pageKey));
    const sortOrder = useToolbarViewOptionsStore((s) => s.getSortOrder(pageKey));
    const filter = useToolbarViewOptionsStore((s) => s.groupFilter);
    const { pinnedIDs: pinnedGroupIds, togglePinned: toggleGroupPinned } = useGlobalPinnedIDs(
        SettingKey.GroupCardPinnedIDs
    );

    const sortedGroups = useMemo(() => {
        if (!groups) return [];
        const pinOrder = new Map(pinnedGroupIds.map((id, index) => [id, index]));
        return [...groups].sort((a, b) => {
            const aPin = a.id ? pinOrder.get(a.id) : undefined;
            const bPin = b.id ? pinOrder.get(b.id) : undefined;
            if (aPin !== undefined || bPin !== undefined) {
                if (aPin === undefined) return 1;
                if (bPin === undefined) return -1;
                return aPin - bPin;
            }
            const diff = sortField === 'name'
                ? a.name.localeCompare(b.name)
                : (a.id || 0) - (b.id || 0);
            return sortOrder === 'asc' ? diff : -diff;
        });
    }, [groups, pinnedGroupIds, sortField, sortOrder]);

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
