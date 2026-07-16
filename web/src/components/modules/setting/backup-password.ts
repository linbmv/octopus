export const BACKUP_PASSWORD_MIN_BYTES = 8;
export const BACKUP_PASSWORD_MAX_BYTES = 1024;

export const BACKUP_FILE_ACCEPT = [
    'application/json',
    'application/vnd.octopus.backup-encrypted',
    '.json',
    '.octopus-backup',
].join(',');

export type BackupPasswordValidationError = 'length' | 'mismatch';

function utf8ByteLength(value: string): number {
    return new TextEncoder().encode(value).byteLength;
}

export function validateOptionalBackupPassword(
    password: string,
    confirmation?: string,
): BackupPasswordValidationError | null {
    if (password === '' && (confirmation === undefined || confirmation === '')) return null;

    const passwordBytes = utf8ByteLength(password);
    if (passwordBytes < BACKUP_PASSWORD_MIN_BYTES || passwordBytes > BACKUP_PASSWORD_MAX_BYTES) {
        return 'length';
    }
    if (confirmation !== undefined && password !== confirmation) return 'mismatch';
    return null;
}

export function optionalBackupPassword(password: string): string | undefined {
    return password === '' ? undefined : password;
}
