'use client';

import { useState } from 'react';
import { GripVertical, Layers, Trash2, X } from 'lucide-react';
import type { DraggableProvided } from '@hello-pangea/dnd';
import { AnimatePresence, motion } from 'motion/react';
import { useTranslations } from 'next-intl';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/animate-ui/components/animate/tooltip';
import { cn } from '@/lib/utils';
import { getModelIcon } from '@/lib/model-icons';
import type { SelectedMember } from './MemberTypes';

export type MemberItemDnd = {
    innerRef: DraggableProvided['innerRef'];
    draggableProps: DraggableProvided['draggableProps'];
    dragHandleProps: DraggableProvided['dragHandleProps'];
    isDragging: boolean;
};

export function MemberItem({
    member,
    onRemove,
    onWeightChange,
    onToggleDisabled,
    isRemoving,
    index,
    showWeight = false,
    showConfirmDelete = true,
    layoutScope,
    dnd,
}: {
    member: SelectedMember;
    onRemove: (id: string) => void;
    onWeightChange?: (id: string, weight: number) => void;
    onToggleDisabled?: (id: string, disabled: boolean) => void;
    isRemoving?: boolean;
    index: number;
    showWeight?: boolean;
    showConfirmDelete?: boolean;
    layoutScope?: string;
    dnd: MemberItemDnd;
}) {
    const t = useTranslations('group');
    const [confirmDelete, setConfirmDelete] = useState(false);
    const isChannelMember = member.type === 'channel';
    const { Avatar: ModelAvatar } = isChannelMember ? getModelIcon(member.name) : { Avatar: Layers };
    const memberDisabled = member.disabled === true;
    const channelDisabled = isChannelMember ? member.enabled === false : false;
    const isDisabled = memberDisabled || channelDisabled;

    return (
        <div
            // DnD libraries provide imperative refs/props; the hook lint rule (`react-hooks/refs`)
            // flags this pattern, but it's safe and required for correct drag behavior.
            // eslint-disable-next-line react-hooks/refs
            ref={dnd.innerRef}
            // eslint-disable-next-line react-hooks/refs
            {...dnd.draggableProps}
            className={cn('rounded-lg grid transition-[grid-template-rows] duration-200', isRemoving ? 'grid-rows-[0fr]' : 'grid-rows-[1fr]')}
            // eslint-disable-next-line react-hooks/refs
            style={{
                /* eslint-disable-next-line react-hooks/refs */
                ...(dnd.draggableProps?.style ?? {}),
                /* eslint-disable-next-line react-hooks/refs */
                ...(dnd.isDragging ? { zIndex: 50, boxShadow: '0 8px 32px rgba(0,0,0,0.15)' } : null),
            }}
        >
            <div className={cn(
                'flex items-center gap-2 rounded-lg bg-background border border-border/50 px-2.5 py-2 select-none transition-opacity duration-200 relative overflow-hidden',
                isRemoving && 'opacity-0',
                isDisabled && 'opacity-60 grayscale'
            )}>
                {onToggleDisabled ? (
                    <Tooltip side="top" sideOffset={10} align="center">
                        <TooltipTrigger asChild>
                            <button
                                type="button"
                                // 数字编号兼作启用/禁用开关：点击切换成员禁用状态，替代原先紧挨删除键的电源按钮以减少误触。
                                // 渠道级禁用时不允许在此切换：成员级开关无法覆盖渠道整体禁用状态。
                                disabled={channelDisabled}
                                aria-pressed={!memberDisabled}
                                aria-label={
                                    channelDisabled
                                        ? t('item.channelDisabled')
                                        : memberDisabled
                                            ? t('item.enable')
                                            : t('item.disable')
                                }
                                onClick={() => onToggleDisabled(member.id, !memberDisabled)}
                                className={cn(
                                    'size-5 rounded-md text-xs font-bold grid place-items-center shrink-0 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40',
                                    channelDisabled
                                        ? 'bg-muted text-muted-foreground/50 cursor-not-allowed'
                                        : memberDisabled
                                            ? 'bg-muted text-muted-foreground hover:bg-muted/80'
                                            : 'bg-primary/10 text-primary hover:bg-primary/20'
                                )}
                            >
                                {index + 1}
                            </button>
                        </TooltipTrigger>
                        <TooltipContent>
                            {channelDisabled
                                ? t('item.channelDisabled')
                                : memberDisabled
                                    ? t('item.enable')
                                    : t('item.disable')}
                        </TooltipContent>
                    </Tooltip>
                ) : (
                    <span className={cn(
                        'size-5 rounded-md text-xs font-bold grid place-items-center shrink-0',
                        isDisabled ? 'bg-muted text-muted-foreground' : 'bg-primary/10 text-primary'
                    )}>
                        {index + 1}
                    </span>
                )}

                <div
                    className={cn(
                        'p-0.5 rounded touch-none transition-colors',
                        isDisabled
                            ? 'cursor-grab active:cursor-grabbing hover:bg-muted/60'
                            : 'cursor-grab active:cursor-grabbing hover:bg-muted'
                    )}
                    // eslint-disable-next-line react-hooks/refs
                    {...dnd.dragHandleProps}
                >
                    <GripVertical className="size-3.5 text-muted-foreground" />
                </div>

                <span className={cn(isDisabled && 'opacity-70')}>
                    <ModelAvatar size={18} />
                </span>

                <div className="flex flex-col min-w-0 flex-1">
                    <Tooltip side="top" sideOffset={10} align="start">
                        <TooltipTrigger className={cn(
                            'text-sm font-medium truncate leading-tight',
                            isDisabled && 'text-muted-foreground'
                        )}>
                            {isChannelMember ? member.name : member.target_group_name}
                        </TooltipTrigger>
                        <TooltipContent key={member.id}>
                            {isChannelMember ? member.name : member.target_group_name}
                        </TooltipContent>
                    </Tooltip>
                    <span className="text-[10px] text-muted-foreground truncate leading-tight">
                        {isChannelMember ? member.channel_name : t('item.group')}
                    </span>
                </div>

                {showWeight && (
                    <input
                        type="number"
                        min={1}
                        value={member.weight ?? 1}
                        onChange={(e) => onWeightChange?.(member.id, Math.max(1, parseInt(e.target.value) || 1))}
                        className={cn(
                            'w-12 h-6 text-xs text-center rounded border border-border bg-muted/50 focus:outline-none focus:ring-1 focus:ring-primary',
                            isDisabled && 'text-muted-foreground'
                        )}
                    />
                )}

                {(!showConfirmDelete || !confirmDelete) && (
                    <motion.button
                        layoutId={`delete-btn-member-${layoutScope ?? 'default'}-${member.id}`}
                        type="button"
                        onClick={() => showConfirmDelete ? setConfirmDelete(true) : onRemove(member.id)}
                        className="p-1 rounded hover:bg-destructive/10 hover:text-destructive transition-colors"
                        initial={false}
                        animate={{ opacity: 1, x: 0 }}
                        transition={{ duration: 0.15 }}
                        style={{ pointerEvents: 'auto' }}
                    >
                        <X className="size-3" />
                    </motion.button>
                )}

                <AnimatePresence>
                    {showConfirmDelete && confirmDelete && (
                        <motion.div
                            layoutId={`delete-btn-member-${layoutScope ?? 'default'}-${member.id}`}
                            className="absolute inset-0 flex items-center justify-center gap-2 bg-destructive p-1.5 rounded-lg"
                            transition={{ type: 'spring', stiffness: 400, damping: 30 }}
                        >
                            <button
                                type="button"
                                onClick={() => setConfirmDelete(false)}
                                className="flex h-6 w-6 items-center justify-center rounded-md bg-destructive-foreground/20 text-destructive-foreground transition-all hover:bg-destructive-foreground/30 active:scale-95"
                            >
                                <X className="h-3 w-3" />
                            </button>
                            <button
                                type="button"
                                onClick={() => onRemove(member.id)}
                                className="flex-1 h-6 flex items-center justify-center gap-1.5 rounded-md bg-destructive-foreground text-destructive text-xs font-semibold transition-all hover:bg-destructive-foreground/90 active:scale-[0.98]"
                            >
                                <Trash2 className="h-3 w-3" />
                            </button>
                        </motion.div>
                    )}
                </AnimatePresence>
            </div>
        </div>
    );
}
