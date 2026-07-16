import { useEffect } from 'react';
import { useMutation, useQuery } from '@tanstack/react-query';
import { create } from 'zustand';
import { createJSONStorage, persist, type StateStorage } from 'zustand/middleware';
import { apiClient, setAuthStoreGetter } from '../client';
import { ApiError } from '../types';
import { logger } from '@/lib/logger';
import { getWebAuthnCredential } from '@/lib/webauthn';
import type {
    UserChangePassword as ChangePasswordRequest,
    UserChangeUsername as ChangeUsernameRequest,
    UserLogin as UserLoginRequest,
    UserLoginResponse,
    UserStatusResponse,
} from '../contracts';

export type {
    UserChangePassword as ChangePasswordRequest,
    UserChangeUsername as ChangeUsernameRequest,
    UserLogin as UserLoginRequest,
    UserLoginResponse,
} from '../contracts';

/**
 * 认证状态 Store
 */
export type AuthStatus = 'checking' | 'anonymous' | 'authenticated' | 'password-change-required' | 'api-key-authenticated';

interface AuthState {
    status: AuthStatus;
    isAuthenticated: boolean;
    isLoading: boolean;
    isAPIKeyAuth: boolean;
    mustChangePassword: boolean;
    token: string | null;
    expireAt: string | null;

    // Actions
    setAuth: (response: UserLoginResponse) => void;
    setAPIKeyAuth: (apiKey: string) => void;
    requirePasswordChange: () => void;
    checkAuth: () => Promise<void>;
    clearAuth: () => void;
    logout: () => Promise<void>;
}

const serverStorage: StateStorage = {
    getItem: () => null,
    setItem: () => undefined,
    removeItem: () => undefined,
};

/**
 * 认证状态管理 Store（使用 zustand + persist）
 */
export const useAuthStore = create<AuthState>()(
    persist(
        (set, get) => ({
            status: 'checking',
            isAuthenticated: false,
            isLoading: true,
            isAPIKeyAuth: false,
            mustChangePassword: false,
            token: null,
            expireAt: null,

            setAuth: (response: UserLoginResponse) => {
                set({
                    status: response.must_change_password ? 'password-change-required' : 'authenticated',
                    isAuthenticated: true,
                    isAPIKeyAuth: false,
                    mustChangePassword: response.must_change_password,
                    // Browser administrator login uses an HttpOnly cookie. A
                    // JWT returned by an incorrectly configured server is
                    // deliberately never retained by the UI.
                    token: null,
                    expireAt: response.expire_at ?? null,
                    isLoading: false
                });
            },

            setAPIKeyAuth: (apiKey: string) => {
                set({
                    status: 'api-key-authenticated',
                    isAuthenticated: true,
                    isAPIKeyAuth: true,
                    mustChangePassword: false,
                    token: apiKey,
                    expireAt: null,
                    isLoading: false
                });
            },

            requirePasswordChange: () => {
                if (get().isAPIKeyAuth) return;
                set({
                    status: 'password-change-required',
                    isAuthenticated: true,
                    mustChangePassword: true,
                    isLoading: false,
                });
            },

            checkAuth: async () => {
                const { token, isAPIKeyAuth } = get();

                if (isAPIKeyAuth && !token) {
                    set({ status: 'anonymous', isAuthenticated: false, mustChangePassword: false, isLoading: false });
                    return;
                }

                try {
                    // API Key 模式只需校验 key 是否有效即可
                    const endpoint = isAPIKeyAuth ? '/api/v1/apikey/login' : '/api/v1/user/status';
                    if (isAPIKeyAuth) {
                        await apiClient.get<unknown>(endpoint);
                        set({
                            status: 'api-key-authenticated',
                            isAuthenticated: true,
                            mustChangePassword: false,
                            isLoading: false,
                        });
                    } else {
                        const session = await apiClient.get<UserStatusResponse>(endpoint);
                        set({
                            status: session.must_change_password ? 'password-change-required' : 'authenticated',
                            isAuthenticated: true,
                            mustChangePassword: session.must_change_password,
                            isLoading: false,
                        });
                    }
                } catch (error) {
                    logger.error('认证验证失败:', error);
                    if (error instanceof ApiError && error.status === 401) {
                        get().clearAuth();
                    } else {
                        set({ isLoading: false });
                    }
                }
            },

            clearAuth: () => {
                set({
                    status: 'anonymous',
                    isAuthenticated: false,
                    isAPIKeyAuth: false,
                    mustChangePassword: false,
                    token: null,
                    expireAt: null,
                    isLoading: false
                });
            },

            logout: async () => {
                const isAPIKeyAuth = get().isAPIKeyAuth;
                if (isAPIKeyAuth) {
                    get().clearAuth();
                    return;
                }
                try {
                    await apiClient.post<string>('/api/v1/user/logout', {});
                } catch (error) {
                    // Local state must still be cleared when the session has
                    // already expired or the network is unavailable.
                    if (!(error instanceof ApiError && error.status === 401)) {
                        logger.warn('服务端注销失败，已清除本地状态:', error);
                    }
                } finally {
                    get().clearAuth();
                }
            }
        }),
        {
            name: 'auth-storage',
            storage: createJSONStorage(() => typeof window === 'undefined' ? serverStorage : window.sessionStorage),
            partialize: (state) => ({
                // Only the explicitly entered client API key remains in
                // sessionStorage. Administrator sessions live solely in the
                // HttpOnly cookie and are rediscovered through /user/status.
                token: state.isAPIKeyAuth ? state.token : null,
                expireAt: null,
                isAPIKeyAuth: state.isAPIKeyAuth,
                mustChangePassword: false,
            }),
            version: 2,
            migrate: (persisted) => {
                const state = persisted as Partial<AuthState> | undefined;
                if (state?.isAPIKeyAuth && typeof state.token === 'string' && state.token) {
                    return {
                        token: state.token,
                        expireAt: null,
                        isAPIKeyAuth: true,
                        mustChangePassword: false,
                    } as AuthState;
                }
                return {
                    token: null,
                    expireAt: null,
                    isAPIKeyAuth: false,
                    mustChangePassword: false,
                } as AuthState;
            },
        }
    )
);

// 注册 auth store getter 到 apiClient
if (typeof window !== 'undefined') {
    // Remove bearer tokens persisted by versions that used localStorage.
    window.localStorage.removeItem('auth-storage');
    setAuthStoreGetter(() => {
        const state = useAuthStore.getState();
        return {
            token: state.token,
            clearAuth: state.clearAuth,
            requirePasswordChange: state.requirePasswordChange,
        };
    });
}

/**
 * 用户登录 Hook
 * 
 * @example
 * const login = useLogin();
 * login.mutate({ username: 'admin', password: 'strong-password', expires_in_minutes: 1440 });
 * 
 * if (login.isPending) return <Loading />;
 * if (login.isError) return <Error message={login.error.message} />;
 */
export function useLogin() {
    const { setAuth } = useAuthStore();

    return useMutation({
        mutationFn: async (data: UserLoginRequest) => {
            const response = await apiClient.post<UserLoginResponse>('/api/v1/user/login', {
                ...data,
                auth_mode: 'cookie',
            });
            if (!response.webauthn_required) return response;
            if (!response.webauthn_transaction || !response.webauthn_options) {
                throw new ApiError(500, 'WEBAUTHN_INVALID_CHALLENGE', 'WEBAUTHN_INVALID_CHALLENGE');
            }
            const credential = await getWebAuthnCredential(response.webauthn_options);
            return apiClient.postWithHeaders<UserLoginResponse>('/api/v1/user/login/webauthn/finish', credential, {
                'X-Octopus-WebAuthn-Transaction': response.webauthn_transaction,
            });
        },
        onSuccess: (data) => {
            setAuth(data);
        },
        onError: (error) => {
            logger.error('登录失败:', error);
        },
    });
}

export function useUserStatus(enabled = true) {
    return useQuery({
        queryKey: ['user', 'status'],
        queryFn: () => apiClient.get<UserStatusResponse>('/api/v1/user/status'),
        enabled,
    });
}

/**
 * 修改密码 Hook
 * 
 * @example
 * const changePassword = useChangePassword();
 * changePassword.mutate({ oldPassword: '123', newPassword: '456' });
 */
export function useChangePassword() {
    return useMutation({
        mutationFn: async (data: { oldPassword: string; newPassword: string }) => {
            const payload: ChangePasswordRequest = {
                old_password: data.oldPassword,
                new_password: data.newPassword,
            };
            return apiClient.post<string>('/api/v1/user/change-password', payload);
        },
        onSuccess: (message) => {
            logger.log('密码修改成功:', message);
        },
        onError: (error) => {
            logger.error('密码修改失败:', error);
        },
    });
}

/**
 * 修改用户名 Hook
 * 
 * @example
 * const changeUsername = useChangeUsername();
 * changeUsername.mutate({ newUsername: 'newname', currentPassword: 'current-password' });
 */
export function useChangeUsername() {
    return useMutation({
        mutationFn: async (data: { newUsername: string; currentPassword: string }) => {
            const payload: ChangeUsernameRequest = {
                new_username: data.newUsername,
                current_password: data.currentPassword,
            };
            return apiClient.post<string>('/api/v1/user/change-username', payload);
        },
        onSuccess: (message) => {
            logger.log('用户名修改成功:', message);
        },
        onError: (error) => {
            logger.error('用户名修改失败:', error);
        },
    });
}

/**
 * 认证状态和方法 Hook
 * 
 * @example
 * const auth = useAuth();
 * 
 * if (auth.isAuthenticated) {
 *   // 已登录
 * }
 * 
 * auth.logout(); // 登出
 */
export function useAuth() {
    const store = useAuthStore();
    const { checkAuth, isLoading } = store;

    // 只在首次挂载时检查认证状态
    useEffect(() => {
        if (isLoading) {
            checkAuth();
        }
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, []); // 有意只在挂载时执行一次

    return {
        isAuthenticated: store.isAuthenticated,
        isAPIKeyAuth: store.isAPIKeyAuth,
        mustChangePassword: store.mustChangePassword,
        status: store.status,
        isLoading: store.isLoading,
        logout: store.logout,
        clearAuth: store.clearAuth,
    };
}
