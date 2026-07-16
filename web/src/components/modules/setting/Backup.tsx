'use client';

import { useMemo, useRef, useState } from 'react';
import { useTranslations } from 'next-intl';
import { Database, Download, ListChecks, Upload } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Switch } from '@/components/ui/switch';
import { toast } from '@/components/common/Toast';
import {
    DBImportConflictResponseError,
    useExportDB,
    useImportDB,
    type DBImportConflictPolicy,
    type DBImportResult,
} from '@/api/endpoints/setting';
import {
    BACKUP_FILE_ACCEPT,
    BACKUP_PASSWORD_MAX_BYTES,
    optionalBackupPassword,
    validateOptionalBackupPassword,
} from './backup-password';

export function SettingBackup() {
    const t = useTranslations('setting');

    const exportDB = useExportDB();
    const importDB = useImportDB();

    const [includeLogs, setIncludeLogs] = useState(false);
    const [includeStats, setIncludeStats] = useState(false);
    const [exportPassword, setExportPassword] = useState('');
    const [exportPasswordConfirmation, setExportPasswordConfirmation] = useState('');

    const [file, setFile] = useState<File | null>(null);
    const [importPassword, setImportPassword] = useState('');
	const [conflictPolicy, setConflictPolicy] = useState<DBImportConflictPolicy>('reject');
    const [lastImportResult, setLastImportResult] = useState<DBImportResult | null>(null);
    const fileInputRef = useRef<HTMLInputElement | null>(null);

    const rowsAffected = lastImportResult?.rows_affected ?? null;
    const rowsAffectedList = useMemo(() => {
        if (!rowsAffected) return [];
        return Object.entries(rowsAffected)
            .sort(([a], [b]) => a.localeCompare(b))
            .map(([k, v]) => ({ table: k, count: v }));
    }, [rowsAffected]);
	const tableSummaryList = useMemo(() => {
		const tables = lastImportResult?.tables;
		if (!tables) return [];
		return Object.entries(tables)
			.sort(([a], [b]) => a.localeCompare(b))
			.map(([table, summary]) => ({ table, ...summary }));
	}, [lastImportResult]);

    const onPickFile = (f: File | null) => {
        setFile(f);
        setLastImportResult(null);
    };

	const onImport = async (dryRun: boolean) => {
        if (!file) {
            toast.error(t('backup.import.noFile'));
            setImportPassword('');
            return;
        }

        const passwordError = validateOptionalBackupPassword(importPassword);
        if (passwordError) {
            toast.error(t('backup.import.password.error.length'));
            setImportPassword('');
            return;
        }
        try {
            const result = await importDB.mutateAsync({
                file,
                password: optionalBackupPassword(importPassword),
				dry_run: dryRun,
				conflict_policy: conflictPolicy,
            });
            setLastImportResult(result);
			toast.success(t(dryRun ? 'backup.import.dryRunSuccess' : 'backup.import.success'));
			if (!dryRun) {
				if (fileInputRef.current) fileInputRef.current.value = '';
				setFile(null);
			}
        } catch (e) {
			if (e instanceof DBImportConflictResponseError) {
				setLastImportResult(e.result);
			}
            toast.error(e instanceof Error ? e.message : t('backup.import.failed'));
        } finally {
            setImportPassword('');
            importDB.reset();
        }
    };

    const onExport = async () => {
        const passwordError = validateOptionalBackupPassword(exportPassword, exportPasswordConfirmation);
        if (passwordError) {
            toast.error(t(passwordError === 'mismatch'
                ? 'backup.export.password.error.mismatch'
                : 'backup.export.password.error.length'));
            setExportPassword('');
            setExportPasswordConfirmation('');
            return;
        }
        try {
            await exportDB.mutateAsync({
                include_logs: includeLogs,
                include_stats: includeStats,
                password: optionalBackupPassword(exportPassword),
            });
            toast.success(t('backup.export.success'));
        } catch (e) {
            toast.error(e instanceof Error ? e.message : t('backup.export.failed'));
        } finally {
            setExportPassword('');
            setExportPasswordConfirmation('');
            exportDB.reset();
        }
    };

    return (
        <div className="rounded-3xl border border-border bg-card p-6 space-y-5">
            <h2 className="text-lg font-bold text-card-foreground flex items-center gap-2">
                <Database className="h-5 w-5" />
                {t('backup.title')}
            </h2>

            {/* 导出 */}
            <div className="space-y-3">
                <div className="text-sm font-semibold text-card-foreground">{t('backup.export.title')}</div>

                <div className="flex items-center justify-between gap-4">
                    <div className="text-sm text-muted-foreground">{t('backup.export.includeLogs')}</div>
                    <Switch checked={includeLogs} onCheckedChange={setIncludeLogs} />
                </div>

                <div className="flex items-center justify-between gap-4">
                    <div className="text-sm text-muted-foreground">{t('backup.export.includeStats')}</div>
                    <Switch checked={includeStats} onCheckedChange={setIncludeStats} />
                </div>

                <div className="space-y-2">
                    <label htmlFor="backup-export-password" className="text-sm text-muted-foreground">
                        {t('backup.export.password.label')}
                    </label>
                    <Input
                        id="backup-export-password"
                        type="password"
                        autoComplete="off"
                        spellCheck={false}
                        maxLength={BACKUP_PASSWORD_MAX_BYTES}
                        value={exportPassword}
                        onChange={(event) => setExportPassword(event.target.value)}
                        placeholder={t('backup.export.password.placeholder')}
                        aria-describedby="backup-export-password-hint"
                        className="rounded-xl"
                    />
                    <label htmlFor="backup-export-password-confirmation" className="text-sm text-muted-foreground">
                        {t('backup.export.password.confirmLabel')}
                    </label>
                    <Input
                        id="backup-export-password-confirmation"
                        type="password"
                        autoComplete="off"
                        spellCheck={false}
                        maxLength={BACKUP_PASSWORD_MAX_BYTES}
                        value={exportPasswordConfirmation}
                        onChange={(event) => setExportPasswordConfirmation(event.target.value)}
                        placeholder={t('backup.export.password.confirmPlaceholder')}
                        aria-describedby="backup-export-password-hint"
                        className="rounded-xl"
                    />
                    <p id="backup-export-password-hint" className="text-xs text-muted-foreground">
                        {t('backup.export.password.hint')}
                    </p>
                </div>

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
                <p className="text-xs text-amber-600 dark:text-amber-400">
                    {t('backup.import.warning')}
                </p>

                <Input
                    ref={fileInputRef}
                    type="file"
                    accept={BACKUP_FILE_ACCEPT}
                    onChange={(e) => onPickFile(e.target.files?.[0] ?? null)}
                    className="rounded-xl"
                />

                <div className="space-y-2">
                    <label htmlFor="backup-import-password" className="text-sm text-muted-foreground">
                        {t('backup.import.password.label')}
                    </label>
                    <Input
                        id="backup-import-password"
                        type="password"
                        autoComplete="off"
                        spellCheck={false}
                        maxLength={BACKUP_PASSWORD_MAX_BYTES}
                        value={importPassword}
                        onChange={(event) => setImportPassword(event.target.value)}
                        placeholder={t('backup.import.password.placeholder')}
                        aria-describedby="backup-import-password-hint"
                        className="rounded-xl"
                    />
                    <p id="backup-import-password-hint" className="text-xs text-muted-foreground">
                        {t('backup.import.password.hint')}
                    </p>
                </div>

                <div className="space-y-2">
                    <label htmlFor="backup-import-policy" className="text-sm text-muted-foreground">
                        {t('backup.import.policy.label')}
                    </label>
                    <Select value={conflictPolicy} onValueChange={(value) => setConflictPolicy(value as DBImportConflictPolicy)}>
                        <SelectTrigger id="backup-import-policy" className="w-full rounded-xl">
                            <SelectValue />
                        </SelectTrigger>
                        <SelectContent className="rounded-xl">
                            <SelectItem value="reject">{t('backup.import.policy.reject')}</SelectItem>
                            <SelectItem value="skip">{t('backup.import.policy.skip')}</SelectItem>
                            <SelectItem value="replace">{t('backup.import.policy.replace')}</SelectItem>
                            <SelectItem value="merge">{t('backup.import.policy.merge')}</SelectItem>
                        </SelectContent>
                    </Select>
                </div>

                <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                    <Button
                        type="button"
                        variant="outline"
                        className="w-full rounded-xl"
                        onClick={() => onImport(true)}
                        disabled={importDB.isPending}
                    >
                        <ListChecks className="size-4" />
                        {importDB.isPending ? t('backup.import.importing') : t('backup.import.dryRunButton')}
                    </Button>

                <Button
                    type="button"
                    variant="destructive"
                    className="w-full rounded-xl"
					onClick={() => onImport(false)}
                    disabled={importDB.isPending}
                >
                    <Upload className="size-4" />
                    {importDB.isPending ? t('backup.import.importing') : t('backup.import.button')}
                </Button>
				</div>

				{tableSummaryList.length > 0 && (
					<div className="mt-2 space-y-2 overflow-x-auto">
						<div className="text-xs font-semibold text-card-foreground">
							{t(lastImportResult?.dry_run ? 'backup.import.dryRunResult' : 'backup.import.result')}
						</div>
						<table className="w-full min-w-[36rem] text-xs text-muted-foreground">
							<thead>
								<tr className="border-b border-border text-left">
									<th className="py-1 pr-2 font-medium">{t('backup.import.summary.table')}</th>
									{(['create', 'update', 'skip', 'delete', 'conflict', 'unresolved'] as const).map((key) => (
										<th key={key} className="px-2 py-1 text-right font-medium">{t(`backup.import.summary.${key}`)}</th>
									))}
								</tr>
							</thead>
							<tbody>
								{tableSummaryList.map((item) => (
									<tr key={item.table} className="border-b border-border/60 last:border-0">
										<td className="py-1 pr-2 font-mono text-card-foreground">{item.table}</td>
										{(['create', 'update', 'skip', 'delete', 'conflict', 'unresolved'] as const).map((key) => (
											<td key={key} className="px-2 py-1 text-right tabular-nums">{item[key]}</td>
										))}
									</tr>
								))}
							</tbody>
						</table>
					</div>
				)}

				{(lastImportResult?.issues?.length ?? 0) > 0 && (
					<div className="space-y-1 text-xs text-destructive">
						{lastImportResult?.issues?.slice(0, 10).map((issue, index) => (
							<div key={`${issue.table}-${issue.uuid ?? issue.field ?? index}`}>
								<span className="font-mono">{issue.table}{issue.field ? `.${issue.field}` : ''}</span>: {issue.problem}
							</div>
						))}
					</div>
				)}

				{tableSummaryList.length === 0 && rowsAffectedList.length > 0 && (
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
            </div>
        </div>
    );
}
