import { describe, expect, it } from 'vitest';
import {
    BACKUP_FILE_ACCEPT,
    BACKUP_PASSWORD_MAX_BYTES,
    optionalBackupPassword,
    validateOptionalBackupPassword,
} from '@/components/modules/setting/backup-password';
import {
    addBackupPasswordHeader,
    BACKUP_PASSWORD_HEADER,
    buildDBExportSearchParams,
	buildDBImportSearchParams,
} from '@/api/endpoints/setting';

describe('backup encryption UI boundaries', () => {
    it('keeps plaintext export/import as the empty-password default', () => {
        expect(validateOptionalBackupPassword('', '')).toBeNull();
        expect(validateOptionalBackupPassword('')).toBeNull();
        expect(optionalBackupPassword('')).toBeUndefined();
    });

    it('validates UTF-8 byte length and confirmation without normalizing the secret', () => {
        const password = '备份密码-strong';
        expect(validateOptionalBackupPassword(password, password)).toBeNull();
        expect(optionalBackupPassword(password)).toBe(password);
        expect(validateOptionalBackupPassword('密密', '密密')).toBe('length');
        expect(validateOptionalBackupPassword('12345678', '12345679')).toBe('mismatch');
        expect(validateOptionalBackupPassword('x'.repeat(BACKUP_PASSWORD_MAX_BYTES + 1))).toBe('length');
    });

    it('allows both plaintext JSON and encrypted envelope files', () => {
        expect(BACKUP_FILE_ACCEPT).toContain('application/json');
        expect(BACKUP_FILE_ACCEPT).toContain('application/vnd.octopus.backup-encrypted');
        expect(BACKUP_FILE_ACCEPT).toContain('.json');
        expect(BACKUP_FILE_ACCEPT).toContain('.octopus-backup');
    });

    it('puts the password only in the dedicated request header', () => {
        const headers = addBackupPasswordHeader(new Headers({ 'X-Octopus-CSRF': 'csrf-token' }), 'backup-secret');
        expect(headers.get(BACKUP_PASSWORD_HEADER)).toBe('backup-secret');
        expect(headers.get('X-Octopus-CSRF')).toBe('csrf-token');

        const plaintextHeaders = addBackupPasswordHeader(new Headers(), undefined);
        expect(plaintextHeaders.has(BACKUP_PASSWORD_HEADER)).toBe(false);

        const params = buildDBExportSearchParams({
            include_logs: true,
            include_stats: false,
            password: 'must-not-enter-url',
        });
        expect(params.toString()).toBe('include_logs=true&include_stats=false');
        expect(params.has('password')).toBe(false);

		const importParams = buildDBImportSearchParams({
			dry_run: true,
			conflict_policy: 'merge',
		});
		expect(importParams.toString()).toBe('dry_run=true&conflict_policy=merge');
		expect(importParams.has('password')).toBe(false);
    });
});
