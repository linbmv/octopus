function decodeBase64URL(value: string): ArrayBuffer {
    const normalized = value.replace(/-/g, '+').replace(/_/g, '/');
    const padded = normalized.padEnd(Math.ceil(normalized.length / 4) * 4, '=');
    const binary = atob(padded);
    return Uint8Array.from(binary, (character) => character.charCodeAt(0)).buffer;
}

function encodeBase64URL(value: ArrayBuffer): string {
    const bytes = new Uint8Array(value);
    let binary = '';
    for (const byte of bytes) binary += String.fromCharCode(byte);
    return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/g, '');
}

type CredentialEnvelope = { publicKey: Record<string, unknown> };

export function decodeCreationOptions(envelope: unknown): CredentialCreationOptions {
    const source = (envelope as CredentialEnvelope)?.publicKey;
    if (!source || typeof source !== 'object') throw new Error('WEBAUTHN_INVALID_OPTIONS');
    const user = source.user as { id: string };
    const exclusions = source.excludeCredentials as Array<{ id: string }> | undefined;
    return {
        publicKey: {
            ...source,
            challenge: decodeBase64URL(source.challenge as string),
            user: { ...user, id: decodeBase64URL(user.id) },
            excludeCredentials: exclusions?.map((credential) => ({ ...credential, id: decodeBase64URL(credential.id) })),
        } as PublicKeyCredentialCreationOptions,
    };
}

export function decodeRequestOptions(envelope: unknown): CredentialRequestOptions {
    const source = (envelope as CredentialEnvelope)?.publicKey;
    if (!source || typeof source !== 'object') throw new Error('WEBAUTHN_INVALID_OPTIONS');
    const allowed = source.allowCredentials as Array<{ id: string }> | undefined;
    return {
        publicKey: {
            ...source,
            challenge: decodeBase64URL(source.challenge as string),
            allowCredentials: allowed?.map((credential) => ({ ...credential, id: decodeBase64URL(credential.id) })),
        } as PublicKeyCredentialRequestOptions,
    };
}

export function credentialToJSON(credential: PublicKeyCredential): Record<string, unknown> {
    const response = credential.response;
    const common = {
        id: credential.id,
        rawId: encodeBase64URL(credential.rawId),
        type: credential.type,
        authenticatorAttachment: credential.authenticatorAttachment,
        clientExtensionResults: credential.getClientExtensionResults(),
    };
    if (response instanceof AuthenticatorAttestationResponse) {
        return {
            ...common,
            response: {
                clientDataJSON: encodeBase64URL(response.clientDataJSON),
                attestationObject: encodeBase64URL(response.attestationObject),
                transports: response.getTransports?.() ?? [],
            },
        };
    }
    if (response instanceof AuthenticatorAssertionResponse) {
        return {
            ...common,
            response: {
                clientDataJSON: encodeBase64URL(response.clientDataJSON),
                authenticatorData: encodeBase64URL(response.authenticatorData),
                signature: encodeBase64URL(response.signature),
                userHandle: response.userHandle ? encodeBase64URL(response.userHandle) : null,
            },
        };
    }
    throw new Error('WEBAUTHN_UNSUPPORTED_RESPONSE');
}

export async function createWebAuthnCredential(options: unknown): Promise<Record<string, unknown>> {
    if (!globalThis.PublicKeyCredential || !navigator.credentials) throw new Error('WEBAUTHN_UNAVAILABLE');
    const credential = await navigator.credentials.create(decodeCreationOptions(options));
    if (!(credential instanceof PublicKeyCredential)) throw new Error('WEBAUTHN_CANCELLED');
    return credentialToJSON(credential);
}

export async function getWebAuthnCredential(options: unknown): Promise<Record<string, unknown>> {
    if (!globalThis.PublicKeyCredential || !navigator.credentials) throw new Error('WEBAUTHN_UNAVAILABLE');
    const credential = await navigator.credentials.get(decodeRequestOptions(options));
    if (!(credential instanceof PublicKeyCredential)) throw new Error('WEBAUTHN_CANCELLED');
    return credentialToJSON(credential);
}

export const webAuthnBase64 = { decode: decodeBase64URL, encode: encodeBase64URL };
