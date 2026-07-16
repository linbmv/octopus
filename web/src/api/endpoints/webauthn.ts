import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiClient } from '../client';
import { createWebAuthnCredential } from '@/lib/webauthn';

export interface WebAuthnCredentialInfo {
    id: number;
    name: string;
    created_at: string;
    updated_at: string;
}

interface WebAuthnBeginResponse {
    transaction: string;
    public_key: unknown;
}

const credentialQueryKey = ['user', 'webauthn', 'credentials'] as const;

export function useWebAuthnCredentials(enabled: boolean) {
    return useQuery({
        queryKey: credentialQueryKey,
        queryFn: () => apiClient.get<WebAuthnCredentialInfo[]>('/api/v1/user/webauthn/credentials'),
        enabled,
    });
}

export function useRegisterWebAuthn() {
    const queryClient = useQueryClient();
    return useMutation({
        mutationFn: async ({ name, currentPassword }: { name: string; currentPassword: string }) => {
            const begin = await apiClient.post<WebAuthnBeginResponse>('/api/v1/user/webauthn/register/begin', {
                name,
                current_password: currentPassword,
            });
            const credential = await createWebAuthnCredential(begin.public_key);
            return apiClient.postWithHeaders<string>('/api/v1/user/webauthn/register/finish', credential, {
                'X-Octopus-WebAuthn-Transaction': begin.transaction,
            });
        },
        onSuccess: () => queryClient.invalidateQueries({ queryKey: credentialQueryKey }),
    });
}

export function useDeleteWebAuthn() {
    const queryClient = useQueryClient();
    return useMutation({
        mutationFn: ({ id, currentPassword }: { id: number; currentPassword: string }) =>
            apiClient.post<string>('/api/v1/user/webauthn/delete', { id, current_password: currentPassword }),
        onSuccess: () => queryClient.invalidateQueries({ queryKey: credentialQueryKey }),
    });
}
