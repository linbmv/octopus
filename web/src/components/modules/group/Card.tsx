import { memo, useState, useMemo, useCallback, useEffect, useRef } from 'react';
import { Trash2, X, Pencil, Power } from 'lucide-react';
import { motion, AnimatePresence } from 'motion/react';
import { type Group, type GroupUpdateRequest, useDeleteGroup, useUpdateGroup, useUpdateGroupActiveItem } from '@/api/group';
import { useChannelList } from '@/api/channel';
import { useTranslations } from 'use-intl';
import { toast } from 'sonner';
import { CopyIconButton } from '@/components/common/CopyButton';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip';
import type { SelectedMember } from './ItemList';
import { MemberList } from './ItemList';
import { GroupEditor, type GroupEditorValues } from './Editor';
import { cn } from '@/lib/utils';
import {
    MorphingDialog,
    MorphingDialogClose,
    MorphingDialogContainer,
    MorphingDialogContent,
    MorphingDialogDescription,
    MorphingDialogTitle,
    MorphingDialogTrigger,
    useMorphingDialog,
} from '@/components/ui/morphing-dialog';

interface EditDialogContentProps {
    group: Group;
    displayMembers: SelectedMember[];
    isSubmitting: boolean;
    onSubmit: (values: GroupEditorValues, onDone?: () => void) => void;
}

function EditDialogContent({ group, displayMembers, isSubmitting, onSubmit }: EditDialogContentProps) {
    const { setIsOpen } = useMorphingDialog();
    const t = useTranslations('group');
    return (
        <>
            <MorphingDialogTitle className="shrink-0">
                <header className="mb-3 flex items-center justify-between">
                    <h2 className="text-2xl font-bold text-card-foreground">
                        {t('detail.actions.edit')}
                    </h2>
                    <MorphingDialogClose className="relative right-0 top-0" />
                </header>
            </MorphingDialogTitle>
            <MorphingDialogDescription className="flex-1 min-h-0 overflow-hidden">
                <GroupEditor
                    key={`edit-group-${group.id}`}
                    initial={{
                        id: group.id,
                        name: group.name,
                        mode: group.mode,
                        relay_config: group.relay_config,
                        members: displayMembers,
                    }}
                    submitText={t('detail.actions.save')}
                    submittingText={t('create.submitting')}
                    isSubmitting={isSubmitting}
                    onCancel={() => setIsOpen(false)}
                    onSubmit={(v) => onSubmit(v, () => setIsOpen(false))}
                />
            </MorphingDialogDescription>
        </>
    );
}

export const GroupCard = memo(function GroupCard({ group, allGroups, now }: { group: Group; allGroups: Group[]; now: number }) {
    const t = useTranslations('group');
    const updateGroup = useUpdateGroup();
    const updateActiveItem = useUpdateGroupActiveItem();
    const deleteGroup = useDeleteGroup();
    const { data: channelsData = [] } = useChannelList();

    const [confirmDelete, setConfirmDelete] = useState(false);
    const [members, setMembers] = useState<SelectedMember[]>([]);
    const isDragging = useRef(false);

    const channelByModelID = useMemo(() => {
        const map = new Map<number, { channel_name: string; enabled: boolean }>();
        channelsData.forEach(({ raw: channel }) => {
            channel.models.forEach((channelModel) => {
                map.set(channelModel.id, { channel_name: channel.name, enabled: channel.enabled });
            });
        });
        return map;
    }, [channelsData]);
    const groupByID = useMemo(() => new Map(
        allGroups.filter((candidate) => candidate.id !== undefined).map((candidate) => [candidate.id!, candidate])
    ), [allGroups]);

    const displayMembers = useMemo((): SelectedMember[] =>
        (group.items || []).map((item) => {
            if (item.type === 'group') {
                const target = item.target_group_id === undefined ? undefined : groupByID.get(item.target_group_id);
                return {
                    id: `group:${item.target_group_id ?? item.id ?? 0}`,
                    type: 'group',
                    target_group_id: item.target_group_id,
                    name: target?.name ?? `Group ${item.target_group_id ?? '?'}`,
                    enabled: target?.enabled ?? false,
                    disabled: item.disabled,
                    weight: item.weight,
                    channel_name: t('form.nestedGroup'),
                    item_id: item.id,
                };
            }
            const channelModel = item.channel_model;
            const channel = item.channel_model_id === undefined ? undefined : channelByModelID.get(item.channel_model_id);
            return {
                id: `model:${item.channel_model_id ?? item.id ?? 0}`,
                type: 'channel_model',
                channel_model_id: item.channel_model_id,
                name: channelModel?.name ?? `Model ${item.channel_model_id}`,
                enabled: channel?.enabled ?? true,
                disabled: item.disabled,
                weight: item.weight,
                channel_id: channelModel?.channel_id ?? 0,
                channel_name: channel?.channel_name ?? 'Unknown channel',
                item_id: item.id,
            };
        }),
        [group.items, channelByModelID, groupByID, t]
    );

    useEffect(() => {
        if (!isDragging.current) setMembers([...displayMembers]);
    }, [displayMembers]);

    const onSuccess = useCallback(() => toast.success(t('toast.updated')), [t]);
    const onError = useCallback((error: Error) => toast.error(t('toast.updateFailed'), { description: error.message }), [t]);

    const priorityByItemId = useMemo(() => {
        const map = new Map<number, number>();
        (group.items || []).forEach((item) => {
            if (item.id !== undefined) map.set(item.id, item.priority);
        });
        return map;
    }, [group.items]);

    const handleDragStart = useCallback(() => { isDragging.current = true; }, []);
    const handleDragFinish = useCallback(() => { isDragging.current = false; }, []);

    const handleDropReorder = useCallback((nextMembers: SelectedMember[]) => {
        const itemsToUpdate = nextMembers
            .map((m, i) => ({ member: m, newPriority: i + 1 }))
            .filter(({ member, newPriority }) => {
                if (!member.item_id) return false;
                const origPriority = priorityByItemId.get(member.item_id);
                return origPriority !== undefined && origPriority !== newPriority;
            })
            .map(({ member, newPriority }) => ({ id: member.item_id!, priority: newPriority, weight: member.weight ?? 1 }));
        if (itemsToUpdate.length > 0) updateGroup.mutate({ id: group.id!, items_to_update: itemsToUpdate }, { onSuccess, onError });
    }, [group.id, priorityByItemId, updateGroup, onSuccess, onError]);

    const handleRemoveMember = useCallback((id: string) => {
        const member = members.find((m) => m.id === id);
        if (member?.item_id !== undefined) updateGroup.mutate({ id: group.id!, items_to_delete: [member.item_id] }, { onSuccess, onError });
    }, [members, group.id, updateGroup, onSuccess, onError]);

    const handleActivate = useCallback((itemId: number) => {
        if (!group.id || group.mode !== 'manual' || itemId === group.active_item_id || updateActiveItem.isPending) return;
        updateActiveItem.mutate({ groupId: group.id, itemId }, { onSuccess, onError });
    }, [group.active_item_id, group.id, group.mode, onError, onSuccess, updateActiveItem]);

    const handleToggleGroup = useCallback(() => {
        if (!group.id || updateGroup.isPending) return;
        updateGroup.mutate({ id: group.id, enabled: !group.enabled }, { onSuccess, onError });
    }, [group.enabled, group.id, onError, onSuccess, updateGroup]);

    const handleToggleMember = useCallback((itemId: number, disabled: boolean) => {
        if (!group.id || updateGroup.isPending) return;
        updateGroup.mutate({ id: group.id, items_to_update: [{ id: itemId, disabled }] }, { onSuccess, onError });
    }, [group.id, onError, onSuccess, updateGroup]);

    const handleSubmitEdit = useCallback((values: GroupEditorValues, onDone?: () => void) => {
        if (!group.id) return;

        const originalById = new Map<number, { priority: number; weight: number }>();
        const originalIds = new Set<number>();
        (group.items || []).forEach((it) => {
            if (typeof it.id === 'number') {
                originalIds.add(it.id);
                originalById.set(it.id, { priority: it.priority, weight: it.weight ?? 1 });
            }
        });

        const newIds = new Set<number>();
        values.members.forEach((m) => { if (typeof m.item_id === 'number') newIds.add(m.item_id); });

        const items_to_delete = Array.from(originalIds).filter((id) => !newIds.has(id));

        const items_to_add = values.members
            .map((m, idx) => ({ m, priority: idx + 1 }))
            .filter(({ m }) => typeof m.item_id !== 'number')
            .map(({ m, priority }) => ({
                type: m.type,
                channel_model_id: m.channel_model_id,
                target_group_id: m.target_group_id,
                priority,
                weight: m.weight ?? 1,
            }));

        const items_to_update = values.members
            .map((m, idx) => ({ m, priority: idx + 1 }))
            .filter(({ m }) => typeof m.item_id === 'number')
            .map(({ m, priority }) => {
                const id = m.item_id!;
                const original = originalById.get(id);
                const weight = m.weight ?? 1;
                if (original === undefined || (original.priority === priority && original.weight === weight)) return null;
                return { id, priority, weight };
            })
            .filter((item): item is { id: number; priority: number; weight: number } => item !== null);

        const payload: GroupUpdateRequest = { id: group.id };

        if (values.name !== group.name) payload.name = values.name;
        if (values.mode !== group.mode) payload.mode = values.mode;
        if (
            values.relay_config.member_max_attempts !== group.relay_config.member_max_attempts ||
            values.relay_config.member_retry_interval_seconds !== group.relay_config.member_retry_interval_seconds ||
            values.relay_config.member_non_stream_response_timeout_seconds !== group.relay_config.member_non_stream_response_timeout_seconds ||
            values.relay_config.member_stream_first_event_timeout_seconds !== group.relay_config.member_stream_first_event_timeout_seconds ||
            values.relay_config.member_cooldown_seconds !== group.relay_config.member_cooldown_seconds ||
            values.relay_config.member_affinity_seconds !== group.relay_config.member_affinity_seconds
        ) payload.relay_config = values.relay_config;
        if (items_to_add.length) payload.items_to_add = items_to_add;
        if (items_to_update.length) payload.items_to_update = items_to_update;
        if (items_to_delete.length) payload.items_to_delete = items_to_delete;

        if (Object.keys(payload).length === 1) {
            onDone?.();
            return;
        }

        updateGroup.mutate(payload, {
            onSuccess: () => {
                onSuccess();
                onDone?.();
            },
            onError,
        });
    }, [group.id, group.items, group.mode, group.name, group.relay_config, onSuccess, onError, updateGroup]);

    return (
    <article className="flex flex-col rounded-3xl border border-border bg-card text-card-foreground p-4">
            <header className="flex items-start justify-between mb-3 relative overflow-visible rounded-xl -mx-1 px-1 -my-1 py-1">
                <div className="relative flex flex-1 items-center gap-2 mr-2 min-w-0 group/title">
                    <Tooltip>
                        <TooltipTrigger asChild>
                            <button
                                type="button"
                                aria-pressed={group.enabled}
                                aria-label={group.enabled ? t('detail.actions.disableGroup') : t('detail.actions.enableGroup')}
                                disabled={!group.id || updateGroup.isPending}
                                onClick={handleToggleGroup}
                                className={cn(
                                    'flex size-7 shrink-0 items-center justify-center rounded-lg transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40 disabled:opacity-50',
                                    group.enabled ? 'bg-primary/10 text-primary hover:bg-primary/20' : 'bg-muted text-muted-foreground hover:bg-muted/80'
                                )}
                            >
                                <Power className="size-4" />
                            </button>
                        </TooltipTrigger>
                        <TooltipContent side="top" sideOffset={10} align="center">
                            {group.enabled ? t('detail.actions.disableGroup') : t('detail.actions.enableGroup')}
                        </TooltipContent>
                    </Tooltip>
                    <Tooltip>
                        <TooltipTrigger asChild>
                            <h3 className={cn('text-lg font-bold truncate', !group.enabled && 'text-muted-foreground')}>{group.name}</h3>
                        </TooltipTrigger>
                        <TooltipContent key={group.name} side="top" sideOffset={10} align="center">{group.name}</TooltipContent>
                    </Tooltip>
                </div>

                <div className="flex items-center gap-1 shrink-0">
                    <MorphingDialog>
                        <MorphingDialogTrigger className="p-1.5 rounded-lg transition-colors hover:bg-muted text-muted-foreground hover:text-foreground">
                            <Tooltip>
                                <TooltipTrigger asChild>
                                    <Pencil className="size-4" />
                                </TooltipTrigger>
                                <TooltipContent side="top" sideOffset={10} align="center">
                                    {t('detail.actions.edit')}
                                </TooltipContent>
                            </Tooltip>
                        </MorphingDialogTrigger>

                        <MorphingDialogContainer>
                            <MorphingDialogContent className="relative w-screen max-w-full md:max-w-4xl bg-card text-card-foreground px-6 py-4 rounded-3xl h-[calc(100vh-2rem)] flex flex-col overflow-hidden">
                                <EditDialogContent
                                    group={group}
                                    displayMembers={displayMembers}
                                    isSubmitting={updateGroup.isPending}
                                    onSubmit={handleSubmitEdit}
                                />
                            </MorphingDialogContent>
                        </MorphingDialogContainer>
                    </MorphingDialog>

                    <Tooltip>
                        <TooltipTrigger asChild>
                            <span className="inline-flex">
                                <CopyIconButton
                                    text={group.name}
                                    className="p-1.5 rounded-lg transition-colors hover:bg-muted text-muted-foreground hover:text-foreground"
                                    copyIconClassName="size-4"
                                    checkIconClassName="size-4 text-primary"
                                />
                            </span>
                        </TooltipTrigger>
                        <TooltipContent side="top" sideOffset={10} align="center">
                            {t('detail.actions.copyName')}
                        </TooltipContent>
                    </Tooltip>
                    {!confirmDelete && (
                        <Tooltip>
                            <TooltipTrigger asChild>
                                <motion.button layoutId={`delete-btn-group-${group.id}`} type="button" onClick={() => setConfirmDelete(true)} className="p-1.5 rounded-lg hover:bg-destructive/10 text-muted-foreground hover:text-destructive transition-colors">
                                    <Trash2 className="size-4" />
                                </motion.button>
                            </TooltipTrigger>
                            <TooltipContent side="top" sideOffset={10} align="center">
                                {t('detail.actions.delete')}
                            </TooltipContent>
                        </Tooltip>
                    )}
                </div>

                <AnimatePresence>
                    {confirmDelete && (
                        <motion.div layoutId={`delete-btn-group-${group.id}`} className="absolute inset-0 flex items-center justify-center gap-2 bg-destructive p-2 rounded-xl" transition={{ type: 'spring', stiffness: 400, damping: 30 }}>
                            <button type="button" onClick={() => setConfirmDelete(false)} className="flex h-7 w-7 items-center justify-center rounded-lg bg-destructive-foreground/20 text-destructive-foreground transition-all hover:bg-destructive-foreground/30 active:scale-95">
                                <X className="size-4" />
                            </button>
                            <button type="button" onClick={() => group.id && deleteGroup.mutate(group.id, { onSuccess: () => toast.success(t('toast.deleted')) })} disabled={deleteGroup.isPending} className="flex-1 h-7 flex items-center justify-center gap-2 rounded-lg bg-destructive-foreground text-destructive text-sm font-semibold transition-all hover:bg-destructive-foreground/90 active:scale-[0.98] disabled:opacity-50 disabled:cursor-not-allowed">
                                <Trash2 className="size-3.5" />
                                {t('detail.actions.confirmDelete')}
                            </button>
                        </motion.div>
                    )}
                </AnimatePresence>
            </header>

            <section className="rounded-xl border border-border/50 bg-muted/30 overflow-hidden relative h-101">
                <MemberList
                    members={members}
                    onReorder={setMembers}
                    onRemove={handleRemoveMember}
                    onActivate={group.mode === 'manual' ? handleActivate : undefined}
                    onToggleDisabled={handleToggleMember}
                    activeItemId={group.mode === 'manual' ? group.active_item_id : group.runtime?.current_item_id}
                    group={group}
                    now={now}
                    onDragStart={handleDragStart}
                    onDrop={handleDropReorder}
                    onDragFinish={handleDragFinish}
                    autoScrollOnAdd={false}
                    layoutScope={`card-${group.id ?? 'unknown'}`}
                />
            </section>
        </article >
    );
});
