'use client';

import { useEffect, useMemo } from 'react';
import { GripVertical, Pin } from 'lucide-react';
import {
    DragDropContext,
    Draggable,
    Droppable,
    type DropResult,
} from '@hello-pangea/dnd';
import { useTranslations } from 'next-intl';
import { cn } from '@/lib/utils';
import { reconcileCardIDs, sortCardItems } from './card-order';

function reorder<T>(items: T[], startIndex: number, endIndex: number): T[] {
    const result = [...items];
    const [removed] = result.splice(startIndex, 1);
    result.splice(endIndex, 0, removed);
    return result;
}

export type ManualOrderItem = {
    id: number;
    name: string;
};

export function ManualOrderList({
    page,
    items,
    orderedIDs,
    pinnedIDs,
    onSave,
}: {
    page: 'channel' | 'group';
    items: ManualOrderItem[];
    orderedIDs: number[];
    pinnedIDs: number[];
    onSave: (nextIDs: number[]) => void;
}) {
    const t = useTranslations('toolbar');
    const reconciledIDs = useMemo(
        () => reconcileCardIDs(orderedIDs, items.map((item) => item.id)),
        [items, orderedIDs]
    );
    const sortedItems = sortCardItems(items, {
        getID: (item) => item.id,
        getName: (item) => item.name,
        mode: 'manual',
        pinnedIDs,
        orderedIDs: reconciledIDs,
    });

    useEffect(() => {
        if (reconciledIDs.length !== orderedIDs.length || reconciledIDs.some((id, index) => id !== orderedIDs[index])) {
            onSave(reconciledIDs);
        }
    }, [onSave, orderedIDs, reconciledIDs]);

    const handleDragEnd = ({ destination, source }: DropResult) => {
        if (!destination || destination.index === source.index) return;
        const nextItems = reorder(sortedItems, source.index, destination.index);
        onSave(reconcileCardIDs(nextItems.map((item) => item.id), items.map((item) => item.id)));
    };

    return (
        <div className="grid gap-2">
            <p className="text-xs font-medium text-muted-foreground">{t('popover.manualOrder')}</p>
            {sortedItems.length === 0 ? (
                <p className="rounded-lg border border-dashed border-border px-2 py-3 text-center text-xs text-muted-foreground">
                    {t(`popover.manualOrderEmpty.${page}`)}
                </p>
            ) : (
                <DragDropContext onDragEnd={handleDragEnd}>
                    <Droppable droppableId={`card-order-${page}`}>
                        {(droppableProvided) => (
                            <div
                                ref={droppableProvided.innerRef}
                                {...droppableProvided.droppableProps}
                                className="max-h-64 space-y-1 overflow-y-auto pr-1"
                            >
                                {sortedItems.map((item, index) => {
                                    const id = item.id;
                                    const pinned = pinnedIDs.includes(id);
                                    return (
                                        <Draggable key={`${page}-${id}`} draggableId={`${page}-${id}`} index={index}>
                                            {(draggableProvided, snapshot) => (
                                                <div
                                                    ref={draggableProvided.innerRef}
                                                    {...draggableProvided.draggableProps}
                                                    className={cn(
                                                        'flex min-w-0 items-center gap-2 rounded-lg border border-border/60 bg-background px-2 py-1.5 text-xs',
                                                        snapshot.isDragging && 'border-primary/40 bg-primary/5 shadow-md'
                                                    )}
                                                >
                                                    <span className="w-4 shrink-0 text-center font-semibold text-muted-foreground">{index + 1}</span>
                                                    <span
                                                        {...draggableProvided.dragHandleProps}
                                                        className="shrink-0 cursor-grab text-muted-foreground active:cursor-grabbing"
                                                        aria-label={t('popover.dragToReorder')}
                                                    >
                                                        <GripVertical className="size-3.5" />
                                                    </span>
                                                    <span className="min-w-0 flex-1 truncate" title={item.name}>{item.name}</span>
                                                    {pinned && <Pin className="size-3.5 shrink-0 text-primary" />}
                                                </div>
                                            )}
                                        </Draggable>
                                    );
                                })}
                                {droppableProvided.placeholder}
                            </div>
                        )}
                    </Droppable>
                </DragDropContext>
            )}
        </div>
    );
}
