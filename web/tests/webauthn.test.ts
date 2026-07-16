import { describe, expect, it } from 'vitest';
import { decodeCreationOptions, decodeRequestOptions, webAuthnBase64 } from '@/lib/webauthn';

function bytes(value: BufferSource): number[] {
    return Array.from(ArrayBuffer.isView(value)
        ? new Uint8Array(value.buffer, value.byteOffset, value.byteLength)
        : new Uint8Array(value));
}

describe('WebAuthn browser encoding', () => {
    it('round-trips binary values through unpadded base64url', () => {
        const input = Uint8Array.from([0, 1, 2, 253, 254, 255]).buffer;
        const encoded = webAuthnBase64.encode(input);
        expect(encoded).toBe('AAEC_f7_');
        expect(Array.from(new Uint8Array(webAuthnBase64.decode(encoded)))).toEqual([0, 1, 2, 253, 254, 255]);
    });

    it('decodes registration challenge, user handle, and exclusions', () => {
        const options = decodeCreationOptions({
            publicKey: {
                challenge: 'AQID',
                rp: { id: 'example.com', name: 'Octopus' },
                user: { id: 'BAUG', name: 'admin', displayName: 'admin' },
                pubKeyCredParams: [{ type: 'public-key', alg: -7 }],
                excludeCredentials: [{ type: 'public-key', id: 'BwgJ' }],
            },
        });
        expect(bytes(options.publicKey!.challenge)).toEqual([1, 2, 3]);
        expect(bytes(options.publicKey!.user.id)).toEqual([4, 5, 6]);
        expect(bytes(options.publicKey!.excludeCredentials![0].id)).toEqual([7, 8, 9]);
    });

    it('decodes authentication challenge and allowed credentials', () => {
        const options = decodeRequestOptions({
            publicKey: {
                challenge: 'AQID',
                rpId: 'example.com',
                allowCredentials: [{ type: 'public-key', id: 'BAUG' }],
            },
        });
        expect(bytes(options.publicKey!.challenge)).toEqual([1, 2, 3]);
        expect(bytes(options.publicKey!.allowCredentials![0].id)).toEqual([4, 5, 6]);
    });
});
