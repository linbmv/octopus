'use client';

import { useMemo } from 'react';
import { useChannelList } from '@/api/endpoints/channel';
import { SettingKey } from '@/api/endpoints/setting';
import { Card } from './Card';
import { useSearchStore, useToolbarViewOptionsStore } from '@/components/modules/toolbar';
import { useGlobalPinnedIDs } from '@/components/modules/toolbar/use-global-pinned-ids';
import { VirtualizedGrid } from '@/components/common/VirtualizedGrid';

export function Channel() {
    const { data: channelsData } = useChannelList();
    const pageKey = 'channel' as const;
    const searchTerm = useSearchStore((s) => s.getSearchTerm(pageKey));
    const layout = useToolbarViewOptionsStore((s) => s.getLayout(pageKey));
    const sortField = useToolbarViewOptionsStore((s) => s.getSortField(pageKey));
    const sortOrder = useToolbarViewOptionsStore((s) => s.getSortOrder(pageKey));
    const filter = useToolbarViewOptionsStore((s) => s.channelFilter);
    const { pinnedIDs: pinnedChannelIds, togglePinned: toggleChannelPinned } = useGlobalPinnedIDs(
        SettingKey.ChannelCardPinnedIDs
    );

    const sortedChannels = useMemo(() => {
        if (!channelsData) return [];
        const pinOrder = new Map(pinnedChannelIds.map((id, index) => [id, index]));
        return [...channelsData].sort((a, b) => {
            const aPin = pinOrder.get(a.raw.id);
            const bPin = pinOrder.get(b.raw.id);
            if (aPin !== undefined || bPin !== undefined) {
                if (aPin === undefined) return 1;
                if (bPin === undefined) return -1;
                return aPin - bPin;
            }
            const diff = sortField === 'name'
                ? a.raw.name.localeCompare(b.raw.name)
                : a.raw.id - b.raw.id;
            return sortOrder === 'asc' ? diff : -diff;
        });
    }, [channelsData, pinnedChannelIds, sortField, sortOrder]);

    const visibleChannels = useMemo(() => {
        const term = searchTerm.toLowerCase().trim();
        const byName = !term ? sortedChannels : sortedChannels.filter((c) => c.raw.name.toLowerCase().includes(term));

        if (filter === 'enabled') return byName.filter((c) => c.raw.enabled);
        if (filter === 'disabled') return byName.filter((c) => !c.raw.enabled);

        return byName;
    }, [sortedChannels, searchTerm, filter]);

    return (
        <VirtualizedGrid
            items={visibleChannels}
            layout={layout}
            columns={{ default: 1, md: 2, lg: 3 }}
            estimateItemHeight={216}
            getItemKey={(item) => `channel-${item.raw.id}`}
            renderItem={(item) => (
                <Card
                    channel={item.raw}
                    stats={item.formatted}
                    layout={layout}
                    pinned={pinnedChannelIds.includes(item.raw.id)}
                    onTogglePinned={toggleChannelPinned}
                />
            )}
        />
    );
}
