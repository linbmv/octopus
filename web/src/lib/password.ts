export const PASSWORD_MIN_BYTES = 8;
export const PASSWORD_MAX_BYTES = 72;

export function passwordByteLength(password: string): number {
    return new TextEncoder().encode(password).length;
}

export function passwordHasValidLength(password: string): boolean {
    const length = passwordByteLength(password);
    return length >= PASSWORD_MIN_BYTES && length <= PASSWORD_MAX_BYTES;
}
