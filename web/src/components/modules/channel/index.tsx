'use client';

import { useMemo } from 'react';
import { useChannelList } from '@/api/endpoints/channel';
import { SettingKey } from '@/api/endpoints/setting';
import { Card } from './Card';
import { useSearchStore, useToolbarViewOptionsStore } from '@/components/modules/toolbar';
import { sortCardItems } from '@/components/modules/toolbar/card-order';
import { useGlobalCardOrder } from '@/components/modules/toolbar/use-global-card-order';
import { useGlobalPinnedIDs } from '@/components/modules/toolbar/use-global-pinned-ids';
import { VirtualizedGrid } from '@/components/common/VirtualizedGrid';

export function Channel() {
    const { data: channelsData } = useChannelList();
    const pageKey = 'channel' as const;
    const searchTerm = useSearchStore((s) => s.getSearchTerm(pageKey));
    const layout = useToolbarViewOptionsStore((s) => s.getLayout(pageKey));
    const filter = useToolbarViewOptionsStore((s) => s.channelFilter);
    const { mode: sortMode, orderedIDs } = useGlobalCardOrder(pageKey);
    const { pinnedIDs: pinnedChannelIds, togglePinned: toggleChannelPinned } = useGlobalPinnedIDs(
        SettingKey.ChannelCardPinnedIDs
    );

    const sortedChannels = useMemo(() => {
        if (!channelsData) return [];
        return sortCardItems(channelsData, {
            getID: (item) => item.raw.id,
            getName: (item) => item.raw.name,
            mode: sortMode,
            pinnedIDs: pinnedChannelIds,
            orderedIDs,
        });
    }, [channelsData, orderedIDs, pinnedChannelIds, sortMode]);

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
