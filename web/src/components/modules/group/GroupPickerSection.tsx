'use client';

import { useMemo, useState } from 'react';
import { Check, Layers, Plus, Search } from 'lucide-react';
import { cn } from '@/lib/utils';
import { Input } from '@/components/ui/input';
import type { Group } from '@/api/endpoints/group';
import type { SelectedMember } from './ItemList';

export function GroupPickerSection({
    groups,
    selectedMembers,
    currentGroupName,
    onAdd,
}: {
    groups: Group[];
    selectedMembers: SelectedMember[];
    currentGroupName: string;
    onAdd: (group: Group) => void;
}) {
    const [searchKeyword, setSearchKeyword] = useState('');

    const selectedGroupIds = useMemo(
        () => new Set(
            selectedMembers
                .filter((m): m is Extract<SelectedMember, { type: 'group' }> => m.type === 'group')
                .map(m => m.target_group_id)
        ),
        [selectedMembers]
    );

    const normalizedSearch = searchKeyword.trim().toLowerCase();

    const filteredGroups = useMemo(() => {
        return groups.filter(g => {
            // 过滤当前分组（避免自引用）
            if (g.name === currentGroupName) return false;
            // 搜索过滤
            if (normalizedSearch && !g.name.toLowerCase().includes(normalizedSearch)) return false;
            return true;
        });
    }, [groups, currentGroupName, normalizedSearch]);

    return (
        <div className="rounded-xl border border-border/50 bg-muted/30 flex flex-col min-h-0">
            <div className="grid grid-cols-[1fr_auto] items-center gap-2 px-3 py-2 border-b border-border/30 bg-muted/50">
                <span className="min-w-0 justify-self-start text-sm font-medium text-foreground">
                    添加分组
                </span>

                <div className="relative justify-self-end w-30">
                    <Search className="pointer-events-none absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
                    <Input
                        value={searchKeyword}
                        onChange={(event) => setSearchKeyword(event.target.value)}
                        className="h-6 rounded-lg border-border/60 bg-background/70 pl-7 pr-2 text-xs shadow-none focus-visible:border-border/60 focus-visible:ring-0"
                        aria-label="search groups"
                    />
                </div>
            </div>

            <div className="flex-1 min-h-0 overflow-y-auto p-2">
                <div className="flex flex-col gap-1.5">
                    {filteredGroups.map((group) => {
                        const isSelected = selectedGroupIds.has(group.id!);
                        const memberCount = group.items?.length ?? 0;

                        return (
                            <button
                                key={group.id}
                                type="button"
                                onClick={() => !isSelected && onAdd(group)}
                                disabled={isSelected}
                                className={cn(
                                    'w-full flex items-center justify-between gap-2 rounded-lg border border-border/50 bg-background px-2.5 py-2 text-left transition-colors',
                                    isSelected ? 'opacity-60 cursor-not-allowed' : 'hover:bg-muted'
                                )}
                            >
                                <span className="flex items-center gap-2 min-w-0">
                                    <Layers className="size-4 shrink-0 text-primary" />
                                    <span className="text-sm font-medium truncate">{group.name}</span>
                                </span>

                                <span className="flex items-center gap-2 shrink-0">
                                    <span className="text-xs text-muted-foreground">
                                        {memberCount}
                                    </span>
                                    {isSelected ? (
                                        <Check className="size-4 text-primary" />
                                    ) : (
                                        <Plus className="size-4 text-muted-foreground" />
                                    )}
                                </span>
                            </button>
                        );
                    })}
                </div>
            </div>
        </div>
    );
}
