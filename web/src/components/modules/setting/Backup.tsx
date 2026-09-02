import { useMemo, useRef, useState } from 'react';
import { useTranslations } from 'use-intl';
import { Database, Download, Upload } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { toast } from 'sonner';
import { useExportDB, useImportDB } from '@/api/setting';

export function SettingBackup() {
    const t = useTranslations('setting');

    const exportDB = useExportDB();
    const importDB = useImportDB();

    const [file, setFile] = useState<File | null>(null);
    const [exportPassword, setExportPassword] = useState('');
    const [importPassword, setImportPassword] = useState('');
    const fileInputRef = useRef<HTMLInputElement | null>(null);

    const rowsAffected = importDB.data?.rows_affected ?? null;
    const warnings = importDB.data?.warnings ?? [];
    const rowsAffectedList = useMemo(() => {
        if (!rowsAffected) return [];
        return Object.entries(rowsAffected)
            .sort(([a], [b]) => a.localeCompare(b))
            .map(([k, v]) => ({ table: k, count: v }));
    }, [rowsAffected]);

    const onPickFile = (f: File | null) => {
        setFile(f);
    };

    const onImport = async () => {
        if (!file) {
            toast.error(t('backup.import.noFile'));
            return;
        }
        try {
            if (new TextEncoder().encode(importPassword).byteLength < 8) {
                toast.error(t('backup.password.invalid'));
                return;
            }
            await importDB.mutateAsync({ file, password: importPassword });
            toast.success(t('backup.import.success'));
            if (fileInputRef.current) fileInputRef.current.value = '';
            setFile(null);
            setImportPassword('');
        } catch (e) {
            toast.error(e instanceof Error ? e.message : t('backup.import.failed'));
        }
    };

    const onExport = async () => {
        if (new TextEncoder().encode(exportPassword).byteLength < 8) {
            toast.error(t('backup.password.invalid'));
            return;
        }
        try {
            await exportDB.mutateAsync(exportPassword);
            toast.success(t('backup.export.success'));
            setExportPassword('');
        } catch (e) {
            toast.error(e instanceof Error ? e.message : t('backup.export.failed'));
        }
    };

    return (
        <div className="rounded-3xl border border-border bg-card p-6 space-y-5">
            <h2 className="text-lg font-bold text-card-foreground flex items-center gap-2">
                <Database className="h-5 w-5" />
                {t('backup.title')}
            </h2>

            {/* 导出 */}
            <div>
                <Input
                    type="password"
                    autoComplete="new-password"
                    value={exportPassword}
                    onChange={(e) => setExportPassword(e.target.value)}
                    placeholder={t('backup.password.placeholder')}
                    className="mb-3 rounded-xl"
                />
                <Button
                    type="button"
                    variant="outline"
                    className="w-full rounded-xl"
                    onClick={onExport}
                    disabled={exportDB.isPending}
                >
                    <Download className="size-4" />
                    {exportDB.isPending ? t('backup.export.exporting') : t('backup.export.button')}
                </Button>
            </div>

            <div className="h-px bg-border" />

            {/* 导入 */}
            <div className="space-y-3">
                <div className="text-sm font-semibold text-card-foreground">{t('backup.import.title')}</div>

                <Input
                    ref={fileInputRef}
                    type="file"
                    accept="application/vnd.octopus.backup-encrypted,.octopus-backup"
                    onChange={(e) => onPickFile(e.target.files?.[0] ?? null)}
                    className="rounded-xl"
                />

                <Input
                    type="password"
                    autoComplete="current-password"
                    value={importPassword}
                    onChange={(e) => setImportPassword(e.target.value)}
                    placeholder={t('backup.password.placeholder')}
                    className="rounded-xl"
                />

                <Button
                    type="button"
                    variant="destructive"
                    className="w-full rounded-xl"
                    onClick={onImport}
                    disabled={importDB.isPending}
                >
                    <Upload className="size-4" />
                    {importDB.isPending ? t('backup.import.importing') : t('backup.import.button')}
                </Button>

                {rowsAffectedList.length > 0 && (
                    <div className="mt-2 space-y-1">
                        <div className="text-xs font-semibold text-card-foreground">{t('backup.import.result')}</div>
                        <div className="grid grid-cols-2 gap-1 text-xs text-muted-foreground">
                            {rowsAffectedList.map((it) => (
                                <div key={it.table} className="flex justify-between gap-2">
                                    <span className="truncate">{it.table}</span>
                                    <span className="tabular-nums">{it.count}</span>
                                </div>
                            ))}
                        </div>
                    </div>
                )}
                {warnings.length > 0 && (
                    <div className="mt-2 space-y-1 rounded-xl border border-amber-300/40 bg-amber-50/60 p-3 text-xs text-amber-900 dark:bg-amber-950/20 dark:text-amber-100">
                        <div className="font-semibold">{t('backup.import.warnings')}</div>
                        {warnings.map((warning, index) => <div key={`${index}-${warning}`}>{warning}</div>)}
                    </div>
                )}
            </div>
        </div>
    );
}
