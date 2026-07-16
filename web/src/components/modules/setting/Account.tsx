'use client';

import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useTranslations } from 'next-intl';
import { User, KeyRound, Lock, Eye, EyeOff, LogOut } from 'lucide-react';
import { Input } from '@/components/ui/input';
import { Button } from '@/components/ui/button';
import { useChangeUsername, useChangePassword, useAuth } from '@/api/endpoints/user';
import { toast } from '@/components/common/Toast';
import { passwordHasValidLength } from '@/lib/password';
import { WebAuthnSettings } from './WebAuthn';

export function SettingAccount() {
    const t = useTranslations('setting');
    const { clearAuth, logout } = useAuth();
    const queryClient = useQueryClient();
    const changeUsername = useChangeUsername();
    const changePassword = useChangePassword();

    const [newUsername, setNewUsername] = useState('');
    const [usernameCurrentPassword, setUsernameCurrentPassword] = useState('');
    const [oldPassword, setOldPassword] = useState('');
    const [newPassword, setNewPassword] = useState('');
    const [confirmPassword, setConfirmPassword] = useState('');
    const [loggingOut, setLoggingOut] = useState(false);

    const [showOldPassword, setShowOldPassword] = useState(false);
    const [showUsernameCurrentPassword, setShowUsernameCurrentPassword] = useState(false);
    const [showNewPassword, setShowNewPassword] = useState(false);
    const [showConfirmPassword, setShowConfirmPassword] = useState(false);

    const handleChangeUsername = () => {
        if (!newUsername.trim()) {
            toast.error(t('account.username.empty'));
            return;
        }
        if (!usernameCurrentPassword) {
            toast.error(t('account.password.oldEmpty'));
            return;
        }

        changeUsername.mutate(
            { newUsername: newUsername.trim(), currentPassword: usernameCurrentPassword },
            {
                onSuccess: () => {
                    toast.success(t('account.username.success'));
                    queryClient.clear();
                    clearAuth();
                },
                onError: () => {
                    toast.error(t('account.username.failed'));
                },
            }
        );
    };

    const handleChangePassword = () => {
        if (!oldPassword) {
            toast.error(t('account.password.oldEmpty'));
            return;
        }
        if (!newPassword) {
            toast.error(t('account.password.newEmpty'));
            return;
        }
        if (newPassword !== confirmPassword) {
            toast.error(t('account.password.mismatch'));
            return;
        }
        if (!passwordHasValidLength(newPassword)) {
            toast.error(t('account.password.tooShort'));
            return;
        }
        if (newPassword === oldPassword) {
            toast.error(t('account.password.unchanged'));
            return;
        }

        changePassword.mutate(
            { oldPassword, newPassword },
            {
                onSuccess: () => {
                    toast.success(t('account.password.success'));
                    queryClient.clear();
                    clearAuth();
                },
                onError: () => {
                    toast.error(t('account.password.failed'));
                },
            }
        );
    };

    const handleLogout = async () => {
        setLoggingOut(true);
        try {
            await logout();
            queryClient.clear();
        } finally {
            setLoggingOut(false);
        }
    };

    return (
        <div className="rounded-3xl border border-border bg-card p-6 space-y-6">
            <h2 className="text-lg font-bold text-card-foreground flex items-center gap-2">
                <User className="h-5 w-5" />
                {t('account.title')}
            </h2>

            {/* 修改用户名 */}
            <div className="space-y-3">
                <div className="flex items-center gap-2 text-sm font-medium text-muted-foreground">
                    <KeyRound className="size-4" />
                    {t('account.username.label')}
                </div>
                <div className="space-y-2">
                    <Input
                        value={newUsername}
                        onChange={(e) => setNewUsername(e.target.value)}
                        placeholder={t('account.username.placeholder')}
                        className="rounded-xl"
                    />
                    <div className="relative">
                        <Input
                            type={showUsernameCurrentPassword ? 'text' : 'password'}
                            value={usernameCurrentPassword}
                            onChange={(e) => setUsernameCurrentPassword(e.target.value)}
                            placeholder={t('account.password.oldPlaceholder')}
                            className="rounded-xl pr-10"
                        />
                        <button
                            type="button"
                            onClick={() => setShowUsernameCurrentPassword(!showUsernameCurrentPassword)}
                            className="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground transition-colors"
                        >
                            {showUsernameCurrentPassword ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
                        </button>
                    </div>
                    <Button
                        onClick={handleChangeUsername}
                        disabled={changeUsername.isPending || !newUsername.trim() || !usernameCurrentPassword}
                        className="w-full rounded-xl"
                    >
                        {changeUsername.isPending ? t('account.saving') : t('account.save')}
                    </Button>
                </div>
            </div>

            <div className="border-t border-border" />

            {/* 修改密码 */}
            <div className="space-y-3">
                <div className="flex items-center gap-2 text-sm font-medium text-muted-foreground">
                    <Lock className="size-4" />
                    {t('account.password.label')}
                </div>
                <div className="space-y-2">
                    <div className="relative">
                        <Input
                            type={showOldPassword ? 'text' : 'password'}
                            value={oldPassword}
                            onChange={(e) => setOldPassword(e.target.value)}
                            placeholder={t('account.password.oldPlaceholder')}
                            className="rounded-xl pr-10"
                        />
                        <button
                            type="button"
                            onClick={() => setShowOldPassword(!showOldPassword)}
                            className="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground transition-colors"
                        >
                            {showOldPassword ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
                        </button>
                    </div>
                    <div className="relative">
                        <Input
                            type={showNewPassword ? 'text' : 'password'}
                            value={newPassword}
                            onChange={(e) => setNewPassword(e.target.value)}
                            placeholder={t('account.password.newPlaceholder')}
                            className="rounded-xl pr-10"
                        />
                        <button
                            type="button"
                            onClick={() => setShowNewPassword(!showNewPassword)}
                            className="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground transition-colors"
                        >
                            {showNewPassword ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
                        </button>
                    </div>
                    <div className="relative">
                        <Input
                            type={showConfirmPassword ? 'text' : 'password'}
                            value={confirmPassword}
                            onChange={(e) => setConfirmPassword(e.target.value)}
                            placeholder={t('account.password.confirmPlaceholder')}
                            className="rounded-xl pr-10"
                        />
                        <button
                            type="button"
                            onClick={() => setShowConfirmPassword(!showConfirmPassword)}
                            className="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground transition-colors"
                        >
                            {showConfirmPassword ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
                        </button>
                    </div>
                    <Button
                        onClick={handleChangePassword}
                        disabled={changePassword.isPending || !oldPassword || !newPassword || !confirmPassword}
                        className="w-full rounded-xl"
                    >
                        {changePassword.isPending ? t('account.saving') : t('account.password.change')}
                    </Button>
                </div>
            </div>

            <div className="border-t border-border" />

            <WebAuthnSettings />

            <div className="border-t border-border" />

            <Button
                type="button"
                variant="outline"
                onClick={handleLogout}
                disabled={loggingOut}
                className="w-full rounded-xl"
            >
                <LogOut className="size-4" />
                {loggingOut ? t('account.loggingOut') : t('account.logout')}
            </Button>
        </div>
    );
}
