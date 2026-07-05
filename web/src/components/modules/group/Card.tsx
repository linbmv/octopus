'use client';

import { useState, useMemo, useCallback, useEffect, useRef } from 'react';
import { Trash2, X, Pencil, Power } from 'lucide-react';
import { motion, AnimatePresence } from 'motion/react';
import { type Group, useDeleteGroup, useUpdateGroup, useGroupList } from '@/api/endpoints/group';
import { useModelChannelList } from '@/api/endpoints/model';
import { useTranslations } from 'next-intl';
import { cn } from '@/lib/utils';
import { toast } from '@/components/common/Toast';
import { CopyIconButton } from '@/components/common/CopyButton';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/animate-ui/components/animate/tooltip';
import type { SelectedMember } from './MemberTypes';
import { MemberList } from './ItemList';
import type { GroupEditorValues } from './Editor';
import { MODE_LABELS } from './utils';
import { GroupMode } from '@/api/endpoints/group';
import { buildDisplayMembers, buildGroupEditorUpdatePayload, buildPriorityByItemId } from './CardLogic';
import { EditDialogContent } from './EditDialogContent';
import {
    MorphingDialog,
    MorphingDialogContainer,
    MorphingDialogContent,
    MorphingDialogTrigger,
} from '@/components/ui/morphing-dialog';

export function GroupCard({ group }: { group: Group }) {
    const t = useTranslations('group');
    const updateGroup = useUpdateGroup();
    const deleteGroup = useDeleteGroup();
    const { data: modelChannels = [] } = useModelChannelList();
    const { data: allGroups = [] } = useGroupList();

    const [confirmDelete, setConfirmDelete] = useState(false);
    const [members, setMembers] = useState<SelectedMember[]>([]);
    // 整组启用的乐观覆盖值：点击 Power 立即反馈，成功后清空回落到 props，失败也清空回滚。
    // 用 null 表示“以服务端 group.enabled 为准”，避免用 effect 同步 props 触发级联渲染。
    const [enabledOverride, setEnabledOverride] = useState<boolean | null>(null);
    const isDragging = useRef(false);
    const weightTimerRef = useRef<NodeJS.Timeout | null>(null);
    const membersRef = useRef<SelectedMember[]>([]);

    const displayMembers = useMemo(() => buildDisplayMembers(group, modelChannels, allGroups), [group, modelChannels, allGroups]);

    /* eslint-disable react-hooks/set-state-in-effect */
    useEffect(() => {
        if (!isDragging.current) {
            // 拖拽列表需要把远端分组成员同步到本地排序状态；拖拽中跳过同步以免打断手势。
            setMembers([...displayMembers]);
        }
    }, [displayMembers]);
    /* eslint-enable react-hooks/set-state-in-effect */

    useEffect(() => {
        membersRef.current = members;
    }, [members]);

    useEffect(() => {
        return () => { if (weightTimerRef.current) clearTimeout(weightTimerRef.current); };
    }, []);

    const onSuccess = useCallback(() => toast.success(t('toast.updated')), [t]);
    const onError = useCallback((error: Error) => toast.error(t('toast.updateFailed'), { description: error.message }), [t]);

    // Avoid UI flicker: drag-reorder also uses the same mutation, so only "mode switch" should lock mode buttons.
    const isUpdatingMode = (() => {
        if (!updateGroup.isPending) return false;
        const v = updateGroup.variables;
        if (typeof v !== 'object' || v === null) return false;
        return 'mode' in v && typeof (v as { mode?: unknown }).mode === 'number';
    })();

    // 仅当本次 mutation 是整组启用切换时才锁 Power 按钮，避免被成员/模式更新影响视觉。
    const isUpdatingEnabled = (() => {
        if (!updateGroup.isPending) return false;
        const v = updateGroup.variables;
        if (typeof v !== 'object' || v === null) return false;
        return 'enabled' in v && typeof (v as { enabled?: unknown }).enabled === 'boolean';
    })();

    // 显示值：乐观覆盖优先，否则取服务端 props；旧响应缺 enabled 时按启用处理。
    const groupEnabled = enabledOverride ?? group.enabled ?? true;

    const handleToggleGroupEnabled = useCallback(() => {
        if (!group.id || isUpdatingEnabled) return;
        const next = !groupEnabled;
        setEnabledOverride(next); // 乐观更新
        updateGroup.mutate(
            { id: group.id, enabled: next },
            {
                onSuccess: () => {
                    setEnabledOverride(null); // 回落到刷新后的 props
                    onSuccess();
                },
                onError: (error: Error) => {
                    setEnabledOverride(null); // 回滚到 props
                    onError(error);
                },
            }
        );
    }, [group.id, groupEnabled, isUpdatingEnabled, updateGroup, onSuccess, onError]);


    const priorityByItemId = useMemo(() => buildPriorityByItemId(group), [group]);

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

    const handleToggleDisabled = useCallback((id: string, disabled: boolean) => {
        const member = members.find((m) => m.id === id);
        if (!member?.item_id) return;
        const priority = priorityByItemId.get(member.item_id);
        if (!priority) return;
        // 乐观更新本地状态，禁用切换即时反馈；priority/weight 一并回传以匹配后端批量更新契约。
        setMembers((prev) => prev.map((m) => m.id === id ? { ...m, disabled } : m));
        updateGroup.mutate(
            { id: group.id!, items_to_update: [{ id: member.item_id, priority, weight: member.weight ?? 1, disabled }] },
            { onSuccess, onError }
        );
    }, [members, group.id, priorityByItemId, updateGroup, onSuccess, onError]);

    const handleWeightChange = useCallback((id: string, weight: number) => {
        setMembers((prev) => prev.map((m) => m.id === id ? { ...m, weight } : m));
        if (weightTimerRef.current) clearTimeout(weightTimerRef.current);
        weightTimerRef.current = setTimeout(() => {
            const member = membersRef.current.find((m) => m.id === id);
            if (!member?.item_id) return;
            const priority = priorityByItemId.get(member.item_id);
            if (!priority) return;
            updateGroup.mutate(
                { id: group.id!, items_to_update: [{ id: member.item_id, priority, weight }] },
                { onSuccess, onError }
            );
        }, 500);
    }, [group.id, priorityByItemId, updateGroup, onSuccess, onError]);

    const handleSubmitEdit = useCallback((values: GroupEditorValues, onDone?: () => void) => {
        if (!group.id) return;

        const payload = buildGroupEditorUpdatePayload(group, values);
        if (!payload) {
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
    }, [group, onSuccess, onError, updateGroup]);

    return (
        <article className="flex flex-col rounded-3xl border border-border bg-card text-card-foreground p-4 custom-shadow">
            <header className="flex items-start justify-between mb-3 relative overflow-visible rounded-xl -mx-1 px-1 -my-1 py-1">
                <div className="relative flex flex-1 items-center gap-2 mr-2 min-w-0 group/title">
                    <Tooltip side="top" sideOffset={10} align="center">
                        <TooltipTrigger asChild>
                            <button
                                type="button"
                                aria-pressed={groupEnabled}
                                aria-label={groupEnabled ? t('detail.actions.disableGroup') : t('detail.actions.enableGroup')}
                                disabled={!group.id || isUpdatingEnabled}
                                onClick={handleToggleGroupEnabled}
                                className={cn(
                                    'flex size-7 shrink-0 items-center justify-center rounded-lg transition-colors',
                                    'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40',
                                    'disabled:cursor-not-allowed disabled:opacity-50',
                                    groupEnabled
                                        ? 'bg-primary/10 text-primary hover:bg-primary/20'
                                        : 'bg-muted text-muted-foreground hover:bg-muted/80'
                                )}
                            >
                                <Power className="size-4" />
                            </button>
                        </TooltipTrigger>
                        <TooltipContent>{groupEnabled ? t('detail.actions.disableGroup') : t('detail.actions.enableGroup')}</TooltipContent>
                    </Tooltip>
                    <div className="relative min-w-0 flex-1 group/title">
                        <Tooltip side="top" sideOffset={10} align="center">
                            <TooltipTrigger asChild>
                                <h3 className={cn('text-lg font-bold truncate', !groupEnabled && 'text-muted-foreground')}>{group.name}</h3>
                            </TooltipTrigger>
                            <TooltipContent key={group.name}>{group.name}</TooltipContent>
                        </Tooltip>
                    </div>
                </div>

                <div className="flex items-center gap-1 shrink-0">
                    <MorphingDialog>
                        <MorphingDialogTrigger className="p-1.5 rounded-lg transition-colors hover:bg-muted text-muted-foreground hover:text-foreground">
                            <Tooltip side="top" sideOffset={10} align="center">
                                <TooltipTrigger asChild>
                                    <Pencil className="size-4" />
                                </TooltipTrigger>
                                <TooltipContent>{t('detail.actions.edit')}</TooltipContent>
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

                    <Tooltip side="top" sideOffset={10} align="center">
                        <TooltipTrigger>
                            <CopyIconButton
                                text={group.name}
                                className="p-1.5 rounded-lg transition-colors hover:bg-muted text-muted-foreground hover:text-foreground"
                                copyIconClassName="size-4"
                                checkIconClassName="size-4 text-primary"
                            />
                        </TooltipTrigger>
                        <TooltipContent>{t('detail.actions.copyName')}</TooltipContent>
                    </Tooltip>
                    {!confirmDelete && (
                        <Tooltip side="top" sideOffset={10} align="center">
                            <TooltipTrigger>
                                <motion.button layoutId={`delete-btn-group-${group.id}`} type="button" onClick={() => setConfirmDelete(true)} className="p-1.5 rounded-lg hover:bg-destructive/10 text-muted-foreground hover:text-destructive transition-colors">
                                    <Trash2 className="size-4" />
                                </motion.button>
                            </TooltipTrigger>
                            <TooltipContent>{t('detail.actions.delete')}</TooltipContent>
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

            {/* Mode: quick switch (no need to enter Edit) */}
            <div className="flex gap-1 mb-3">
                {([GroupMode.RoundRobin, GroupMode.Random, GroupMode.Failover, GroupMode.Weighted] as const).map((m) => (
                    <button
                        key={m}
                        type="button"
                        aria-disabled={isUpdatingMode || !group.id}
                        onClick={() => {
                            if (isUpdatingMode || !group.id) return;
                            if (m === group.mode) return;
                            updateGroup.mutate({ id: group.id!, mode: m }, { onSuccess, onError });
                        }}
                        className={cn(
                            'flex-1 py-1 text-xs rounded-lg transition-colors',
                            group.mode === m ? 'bg-primary text-primary-foreground' : 'bg-muted hover:bg-muted/80',
                            // Keep visuals stable (no opacity/disabled flicker) while still preventing double-submit via onClick guard.
                            (!group.id) && 'cursor-not-allowed opacity-50'
                        )}
                    >
                        {t(`mode.${MODE_LABELS[m]}`)}
                    </button>
                ))}
            </div>

            <section className="rounded-xl border border-border/50 bg-muted/30 overflow-hidden relative h-101">
                <MemberList
                    members={members}
                    onReorder={setMembers}
                    onRemove={handleRemoveMember}
                    onWeightChange={handleWeightChange}
                    onToggleDisabled={handleToggleDisabled}
                    onDragStart={handleDragStart}
                    onDrop={handleDropReorder}
                    onDragFinish={handleDragFinish}
                    autoScrollOnAdd={false}
                    showWeight={group.mode === GroupMode.Weighted}
                    layoutScope={`card-${group.id ?? 'unknown'}`}
                />
            </section>
        </article >
    );
}
