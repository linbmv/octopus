'use client';

import { useCallback, useId, useMemo, useState } from 'react';
import { useTranslations } from 'next-intl';
import { KeyRound, Plus, Loader, X, Maximize2 } from 'lucide-react';
import { motion, AnimatePresence } from 'motion/react';
import {
    MorphingDialog,
    MorphingDialogContainer,
    MorphingDialogContent,
    MorphingDialogTrigger,
    useMorphingDialog,
} from '@/components/ui/morphing-dialog';
import {
    useAPIKeyList,
    useCreateAPIKey,
    useUpdateAPIKey,
    useDeleteAPIKey,
    type APIKey,
} from '@/api/endpoints/apikey';
import { toast } from '@/components/common/Toast';
import type { ApiError } from '@/api/types';
import { APIKeyForm } from './APIKeyForm';
import { type APIKeyFormValues } from './useAPIKeyFormState';
import { APIKeyStatsCard } from './APIKeyStatsCard';
import { APIKeyKeyItem } from './APIKeyKeyItem';

function APIKeyFormOverlay({
    layoutId,
    apiKey,
    isPending,
    submitLabel,
    onSubmit,
    onClose,
}: {
    layoutId: string;
    apiKey?: APIKey;
    isPending: boolean;
    submitLabel: string;
    onSubmit: (data: APIKeyFormValues) => void;
    onClose: () => void;
}) {
    return (
        <motion.div
            layoutId={layoutId}
            className="absolute left-1/2 top-1/2 z-20 w-[min(420px,calc(100vw-2rem))] -translate-x-1/2 -translate-y-1/2 bg-card p-5 rounded-3xl border border-border max-h-[80vh] overflow-auto"
            transition={{ type: 'spring', stiffness: 400, damping: 30 }}
        >
            <APIKeyForm
                apiKey={apiKey}
                isPending={isPending}
                submitLabel={submitLabel}
                onSubmit={onSubmit}
                onClose={onClose}
            />
        </motion.div>
    );
}

function APIKeyPanelBase({
    idPrefix,
    containerClassName,
    listClassName,
    renderHeaderExtra,
}: {
    idPrefix: string;
    containerClassName: string;
    listClassName: string;
    renderHeaderExtra?: (ctx: {
        disabled: boolean;
        onCloseAllOverlays: () => void;
    }) => React.ReactNode;
}) {
    const t = useTranslations('setting');
    const { data: apiKeys, isLoading: apiKeysLoading, error: apiKeysError } = useAPIKeyList();
    const createAPIKey = useCreateAPIKey();
    const updateAPIKey = useUpdateAPIKey();
    const deleteAPIKey = useDeleteAPIKey();

    const instanceId = useId();
    const addLayoutId = `add-btn-${idPrefix}-${instanceId}`;
    const statsPrefix = `${idPrefix}-stats-${instanceId}`;
    const editPrefix = `${idPrefix}-edit-${instanceId}`;
    const deletePrefix = `${idPrefix}-delete-`;

    const [isAdding, setIsAdding] = useState(false);
    const [viewingStats, setViewingStats] = useState<{ apiKey: APIKey; layoutId: string } | null>(null);
    const [editingKey, setEditingKey] = useState<{ apiKey: APIKey; layoutId: string } | null>(null);
    const [deletingId, setDeletingId] = useState<number | null>(null);

    const sortedApiKeys = useMemo(() => {
        if (!apiKeys) return [];
        return [...apiKeys].sort((a, b) => a.id - b.id);
    }, [apiKeys]);

    const handleDelete = useCallback((id: number) => {
        setDeletingId(id);
        deleteAPIKey.mutate(id, {
            onSuccess: () => {
                toast.success(t('apiKey.toast.deleteSuccess'));
            },
            onError: (error) => {
                const msg = (error as unknown as ApiError)?.message;
                toast.error(t('apiKey.toast.deleteError'), { description: msg });
            },
            onSettled: () => setDeletingId((cur) => (cur === id ? null : cur)),
        });
    }, [deleteAPIKey, t]);

    const closeAllOverlays = useCallback(() => {
        setIsAdding(false);
        setViewingStats(null);
        setEditingKey(null);
    }, []);

    const disabledHeaderActions = createAPIKey.isPending || isAdding || !!viewingStats || !!editingKey;

    const handleCreate = useCallback((data: Omit<APIKey, 'id' | 'api_key'>) => {
        createAPIKey.mutate(data, {
            onSuccess: () => {
                toast.success(t('apiKey.toast.createSuccess'));
                setIsAdding(false);
            },
            onError: (error) => {
                const msg = (error as unknown as ApiError)?.message;
                toast.error(t('apiKey.toast.createError'), { description: msg });
            },
        });
    }, [createAPIKey, t]);

    const handleUpdate = useCallback((apiKey: APIKey, data: Omit<APIKey, 'id' | 'api_key'>) => {
        updateAPIKey.mutate({ id: apiKey.id, ...data }, {
            onSuccess: () => {
                toast.success(t('apiKey.toast.updateSuccess'));
                setEditingKey(null);
            },
            onError: (error) => {
                const msg = (error as unknown as ApiError)?.message;
                toast.error(t('apiKey.toast.updateError'), { description: msg });
            },
        });
    }, [t, updateAPIKey]);

    return (
        <div className={containerClassName}>
            <div className="flex items-center justify-between gap-3">
                <h2 className="text-lg font-bold text-card-foreground flex items-center gap-2">
                    <KeyRound className="h-5 w-5" />
                    {t('apiKey.title')}
                </h2>
                <div className="flex items-center gap-2">
                    <motion.button
                        layoutId={addLayoutId}
                        type="button"
                        onClick={() => setIsAdding(true)}
                        disabled={disabledHeaderActions}
                        className="h-9 w-9 flex items-center justify-center rounded-lg bg-muted/60 text-muted-foreground transition-colors hover:bg-muted disabled:opacity-50"
                        title={t('apiKey.add')}
                    >
                        <Plus className="size-4" />
                    </motion.button>
                    {renderHeaderExtra?.({ disabled: disabledHeaderActions, onCloseAllOverlays: closeAllOverlays })}
                </div>
            </div>

            <AnimatePresence>
                {isAdding && (
                    <APIKeyFormOverlay
                        layoutId={addLayoutId}
                        isPending={createAPIKey.isPending}
                        submitLabel={t('apiKey.form.create')}
                        onSubmit={handleCreate}
                        onClose={() => setIsAdding(false)}
                    />
                )}
            </AnimatePresence>

            <AnimatePresence>
                {viewingStats && (
                    <APIKeyStatsCard
                        layoutId={viewingStats.layoutId}
                        apiKey={viewingStats.apiKey}
                        onClose={() => setViewingStats(null)}
                    />
                )}
            </AnimatePresence>

            <AnimatePresence>
                {editingKey && (
                    <APIKeyFormOverlay
                        layoutId={editingKey.layoutId}
                        apiKey={editingKey.apiKey}
                        isPending={updateAPIKey.isPending}
                        submitLabel={t('apiKey.form.save')}
                        onSubmit={(data) => handleUpdate(editingKey.apiKey, data)}
                        onClose={() => setEditingKey(null)}
                    />
                )}
            </AnimatePresence>

            <div className={listClassName}>
                {apiKeysLoading ? (
                    <div className="h-full flex items-center justify-center text-sm text-muted-foreground">
                        <Loader className="size-4 animate-spin" />
                    </div>
                ) : apiKeysError ? (
                    <div className="h-full flex items-center justify-center text-sm text-destructive">
                        {t('apiKey.loadFailed')}
                    </div>
                ) : apiKeys?.length === 0 ? (
                    <div className="h-full flex items-center justify-center text-sm text-muted-foreground">
                        {t('apiKey.empty')}
                    </div>
                ) : (
                    <AnimatePresence>
                        {sortedApiKeys.map((apiKey) => {
                            const statsLayoutId = `${statsPrefix}-${apiKey.id}`;
                            const editLayoutId = `${editPrefix}-${apiKey.id}`;
                            const deleteLayoutId = `${deletePrefix}${apiKey.id}`;
                            return (
                                <APIKeyKeyItem
                                    key={apiKey.id}
                                    apiKey={apiKey}
                                    statsLayoutId={statsLayoutId}
                                    editLayoutId={editLayoutId}
                                    deleteLayoutId={deleteLayoutId}
                                    onViewStats={() => {
                                        closeAllOverlays();
                                        setViewingStats({ apiKey, layoutId: statsLayoutId });
                                    }}
                                    onEdit={() => {
                                        closeAllOverlays();
                                        setEditingKey({ apiKey, layoutId: editLayoutId });
                                    }}
                                    onDelete={() => handleDelete(apiKey.id)}
                                    isDeleting={deleteAPIKey.isPending && deletingId === apiKey.id}
                                />
                            );
                        })}
                    </AnimatePresence>
                )}
            </div>
        </div>
    );
}

function APIKeyDialogPanel() {
    const { setIsOpen } = useMorphingDialog();
    return (
        <APIKeyPanelBase
            idPrefix="apikey-dialog"
            containerClassName="rounded-3xl border border-border bg-card p-6 space-y-5 relative w-screen max-w-full md:max-w-xl"
            listClassName="space-y-2 h-[calc(100vh-10rem)] overflow-y-auto"
            renderHeaderExtra={() => (
                <button
                    type="button"
                    onClick={() => setIsOpen(false)}
                    className="h-9 w-9 flex items-center justify-center rounded-lg bg-muted/60 text-muted-foreground transition-colors hover:bg-muted"
                    title="Close"
                >
                    <X className="size-4" />
                </button>
            )}
        />
    );
}

export function SettingAPIKey() {
    return (
        <APIKeyPanelBase
            idPrefix="apikey"
            containerClassName="rounded-3xl border border-border bg-card p-6 space-y-5 relative"
            listClassName="space-y-2 h-36 overflow-y-auto"
            renderHeaderExtra={() => (
                <MorphingDialog>
                    <MorphingDialogTrigger className="h-9 w-9 flex items-center justify-center rounded-lg bg-muted/60 text-muted-foreground transition-colors hover:bg-muted">
                        <Maximize2 className="size-4" />
                    </MorphingDialogTrigger>
                    <MorphingDialogContainer>
                        <MorphingDialogContent className="relative">
                            <APIKeyDialogPanel />
                        </MorphingDialogContent>
                    </MorphingDialogContainer>
                </MorphingDialog>
            )}
        />
    );
}
