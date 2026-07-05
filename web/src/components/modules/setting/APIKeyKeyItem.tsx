'use client';

import { useState } from 'react';
import { useTranslations } from 'next-intl';
import { Info, Pencil, Trash2, X } from 'lucide-react';
import { AnimatePresence, motion } from 'motion/react';
import { type APIKey } from '@/api/endpoints/apikey';
import { CopyIconButton } from '@/components/common/CopyButton';

export function APIKeyKeyItem({
    apiKey,
    statsLayoutId,
    editLayoutId,
    deleteLayoutId,
    onViewStats,
    onEdit,
    onDelete,
    isDeleting,
}: {
    apiKey: APIKey;
    statsLayoutId: string;
    editLayoutId: string;
    deleteLayoutId: string;
    onViewStats: () => void;
    onEdit: () => void;
    onDelete: () => void;
    isDeleting: boolean;
}) {
    const t = useTranslations('setting');
    const [confirmDelete, setConfirmDelete] = useState(false);

    return (
        <motion.div
            layout
            initial={{ opacity: 0, scale: 0.95 }}
            animate={{ opacity: 1, scale: 1 }}
            exit={{ opacity: 0, scale: 0.95, transition: { duration: 0.2 } }}
            transition={{ type: 'spring', stiffness: 500, damping: 30 }}
            className="group relative flex items-center justify-between gap-3 p-3 rounded-xl bg-muted/50 overflow-hidden origin-top"
        >
            <span className="text-sm font-medium truncate">{apiKey.name}</span>

            <div className="flex items-center gap-1.5">
                <motion.button
                    type="button"
                    layoutId={statsLayoutId}
                    onClick={onViewStats}
                    className="flex size-8 items-center justify-center rounded-lg bg-muted/60 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground active:scale-95"
                    title="Stats"
                >
                    <Info className="size-4" />
                </motion.button>
                <motion.button
                    type="button"
                    layoutId={editLayoutId}
                    onClick={onEdit}
                    className="flex size-8 items-center justify-center rounded-lg bg-muted/60 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground active:scale-95"
                    title="Edit"
                >
                    <Pencil className="size-4" />
                </motion.button>
                <CopyIconButton
                    text={apiKey.api_key}
                    className="flex size-8 items-center justify-center rounded-lg bg-primary/10 text-primary transition-all hover:bg-primary hover:text-primary-foreground active:scale-95"
                    copyIconClassName="size-4"
                    checkIconClassName="size-4"
                />

                {!confirmDelete && (
                    <motion.button
                        layoutId={deleteLayoutId}
                        onClick={() => setConfirmDelete(true)}
                        className="flex size-8 items-center justify-center rounded-lg bg-destructive/10 text-destructive transition-colors hover:bg-destructive hover:text-destructive-foreground"
                    >
                        <Trash2 className="size-4" />
                    </motion.button>
                )}
            </div>

            <AnimatePresence>
                {confirmDelete && (
                    <motion.div
                        layoutId={deleteLayoutId}
                        className="absolute inset-0 flex items-center justify-center gap-2 bg-destructive p-3 rounded-xl"
                        transition={{ type: 'spring', stiffness: 400, damping: 30 }}
                    >
                        <button
                            onClick={() => setConfirmDelete(false)}
                            className="flex size-8 items-center justify-center rounded-lg bg-destructive-foreground/20 text-destructive-foreground transition-all hover:bg-destructive-foreground/30 active:scale-95"
                        >
                            <X className="size-4" />
                        </button>
                        <button
                            onClick={onDelete}
                            disabled={isDeleting}
                            className="flex-1 h-8 flex items-center justify-center gap-1.5 rounded-lg bg-destructive-foreground text-destructive text-sm font-medium transition-all hover:bg-destructive-foreground/90 active:scale-[0.98] disabled:opacity-50"
                        >
                            <Trash2 className="size-3.5" />
                            {isDeleting ? '...' : t('apiKey.form.confirm')}
                        </button>
                    </motion.div>
                )}
            </AnimatePresence>
        </motion.div>
    );
}
