'use client';

import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useTranslations } from 'next-intl';
import { LockKeyhole } from 'lucide-react';
import { useAuth, useChangePassword } from '@/api/endpoints/user';
import { toast } from '@/components/common/Toast';
import Logo from '@/components/modules/logo';
import { Button } from '@/components/ui/button';
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field';
import { Input } from '@/components/ui/input';
import { passwordHasValidLength } from '@/lib/password';

export function PasswordChangeRequired() {
    const t = useTranslations('passwordChangeRequired');
    const queryClient = useQueryClient();
    const { clearAuth } = useAuth();
    const changePassword = useChangePassword();
    const [currentPassword, setCurrentPassword] = useState('');
    const [newPassword, setNewPassword] = useState('');
    const [confirmPassword, setConfirmPassword] = useState('');
    const [error, setError] = useState<string | null>(null);

    const handleSubmit = async (event: React.FormEvent) => {
        event.preventDefault();
        setError(null);
        if (newPassword !== confirmPassword) {
            setError(t('error.mismatch'));
            return;
        }
        if (!passwordHasValidLength(newPassword)) {
            setError(t('error.length'));
            return;
        }
        if (newPassword === currentPassword) {
            setError(t('error.unchanged'));
            return;
        }

        try {
            await changePassword.mutateAsync({ oldPassword: currentPassword, newPassword });
            queryClient.clear();
            clearAuth();
            toast.success(t('success'));
        } catch (cause) {
            setError(cause instanceof Error ? cause.message : t('error.generic'));
        }
    };

    return (
        <div className="min-h-screen flex items-center justify-center px-6 text-foreground">
            <div className="w-full max-w-sm space-y-7 rounded-3xl border border-border bg-card p-7 shadow-sm">
                <header className="flex flex-col items-center gap-3 text-center">
                    <Logo size={48} />
                    <div className="flex items-center gap-2">
                        <LockKeyhole className="size-5" />
                        <h1 className="text-xl font-bold">{t('title')}</h1>
                    </div>
                    <p className="text-sm text-muted-foreground">{t('description')}</p>
                </header>

                <form onSubmit={handleSubmit} className="space-y-5">
                    <Field>
                        <FieldLabel htmlFor="required-current-password">{t('currentPassword')}</FieldLabel>
                        <Input
                            id="required-current-password"
                            type="password"
                            autoComplete="current-password"
                            value={currentPassword}
                            onChange={(event) => setCurrentPassword(event.target.value)}
                            disabled={changePassword.isPending}
                            required
                        />
                    </Field>
                    <Field>
                        <FieldLabel htmlFor="required-new-password">{t('newPassword')}</FieldLabel>
                        <Input
                            id="required-new-password"
                            type="password"
                            autoComplete="new-password"
                            value={newPassword}
                            onChange={(event) => setNewPassword(event.target.value)}
                            disabled={changePassword.isPending}
                            required
                        />
                    </Field>
                    <Field>
                        <FieldLabel htmlFor="required-confirm-password">{t('confirmPassword')}</FieldLabel>
                        <Input
                            id="required-confirm-password"
                            type="password"
                            autoComplete="new-password"
                            value={confirmPassword}
                            onChange={(event) => setConfirmPassword(event.target.value)}
                            disabled={changePassword.isPending}
                            required
                        />
                    </Field>
                    {error && <FieldDescription className="text-destructive">{error}</FieldDescription>}
                    <Button type="submit" disabled={changePassword.isPending} className="w-full">
                        {changePassword.isPending ? t('submitting') : t('submit')}
                    </Button>
                </form>
            </div>
        </div>
    );
}
