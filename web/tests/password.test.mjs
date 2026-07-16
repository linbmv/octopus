import { describe, expect, it } from 'vitest';

import {
    PASSWORD_MAX_BYTES,
    PASSWORD_MIN_BYTES,
    passwordByteLength,
    passwordHasValidLength,
} from '../src/lib/password.ts';

describe('password byte policy', () => {
    it('measures UTF-8 bytes rather than JavaScript characters', () => {
        expect(passwordByteLength('密码')).toBe(6);
        expect(passwordByteLength('🔐')).toBe(4);
    });

    it('matches the backend 8-72 byte boundary', () => {
        expect(passwordHasValidLength('a'.repeat(PASSWORD_MIN_BYTES - 1))).toBe(false);
        expect(passwordHasValidLength('a'.repeat(PASSWORD_MIN_BYTES))).toBe(true);
        expect(passwordHasValidLength('a'.repeat(PASSWORD_MAX_BYTES))).toBe(true);
        expect(passwordHasValidLength('a'.repeat(PASSWORD_MAX_BYTES + 1))).toBe(false);
    });
});
