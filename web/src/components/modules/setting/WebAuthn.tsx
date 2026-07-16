'use client';

import { useState } from 'react';
import { Fingerprint, Plus, Trash2 } from 'lucide-react';
import { useLocale, useTranslations } from 'next-intl';
import { useUserStatus } from '@/api/endpoints/user';
import { useDeleteWebAuthn, useRegisterWebAuthn, useWebAuthnCredentials } from '@/api/endpoints/webauthn';
import { toast } from '@/components/common/Toast';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';

export function WebAuthnSettings() {
    const t = useTranslations('setting.account.webauthn');
    const locale = useLocale();
    const status = useUserStatus();
    const enabled = Boolean(status.data?.webauthn_enabled);
    const credentials = useWebAuthnCredentials(enabled);
    const registerCredential = useRegisterWebAuthn();
    const deleteCredential = useDeleteWebAuthn();
    const [name, setName] = useState('');
    const [currentPassword, setCurrentPassword] = useState('');

    const handleRegister = async () => {
        if (!name.trim() || !currentPassword) return;
        try {
            await registerCredential.mutateAsync({ name: name.trim(), currentPassword });
            setName('');
            setCurrentPassword('');
            toast.success(t('registered'));
        } catch {
            toast.error(t('registerFailed'));
        }
    };

    const handleDelete = async (id: number) => {
        if (!currentPassword) {
            toast.error(t('passwordRequired'));
            return;
        }
        try {
            await deleteCredential.mutateAsync({ id, currentPassword });
            toast.success(t('deleted'));
        } catch {
            toast.error(t('deleteFailed'));
        }
    };

    return (
        <section className="space-y-3">
            <div className="flex items-center gap-2 text-sm font-medium text-muted-foreground">
                <Fingerprint className="size-4" />
                {t('title')}
            </div>
            {!enabled ? (
                <p className="text-xs leading-5 text-muted-foreground">{t('disabled')}</p>
            ) : (
                <>
                    <p className="text-xs leading-5 text-muted-foreground">{t('description')}</p>
                    <div className="space-y-2">
                        <Input value={name} onChange={(event) => setName(event.target.value)} placeholder={t('namePlaceholder')} className="rounded-xl" maxLength={100} />
                        <Input type="password" value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} placeholder={t('passwordPlaceholder')} className="rounded-xl" autoComplete="current-password" />
                        <Button type="button" onClick={handleRegister} disabled={registerCredential.isPending || !name.trim() || !currentPassword} className="w-full rounded-xl">
                            <Plus className="size-4" />
                            {registerCredential.isPending ? t('registering') : t('register')}
                        </Button>
                    </div>
                    <div className="space-y-2">
                        {(credentials.data ?? []).map((credential) => (
                            <div key={credential.id} className="flex items-center gap-3 rounded-xl border border-border px-3 py-2">
                                <Fingerprint className="size-4 shrink-0 text-primary" />
                                <div className="min-w-0 flex-1">
                                    <p className="truncate text-sm font-medium">{credential.name}</p>
                                    <p className="text-xs text-muted-foreground">{new Date(credential.created_at).toLocaleString(locale === 'zh_hans' ? 'zh-CN' : locale === 'zh_hant' ? 'zh-TW' : locale)}</p>
                                </div>
                                <Button type="button" size="icon" variant="ghost" onClick={() => handleDelete(credential.id)} disabled={deleteCredential.isPending} aria-label={t('delete')}>
                                    <Trash2 className="size-4 text-destructive" />
                                </Button>
                            </div>
                        ))}
                        {!credentials.isLoading && (credentials.data?.length ?? 0) === 0 ? <p className="text-xs text-muted-foreground">{t('empty')}</p> : null}
                    </div>
                </>
            )}
        </section>
    );
}
