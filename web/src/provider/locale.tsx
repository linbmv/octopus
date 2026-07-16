'use client';

import { type ReactNode } from 'react';
import { NextIntlClientProvider } from 'next-intl';
import { localeMessages } from '@/lib/locale-messages';
import { useSettingStore } from '@/stores/setting';

export function LocaleProvider({ children }: { children: ReactNode }) {
    const { locale } = useSettingStore();

    return (
        <NextIntlClientProvider
            locale={locale}
            messages={localeMessages[locale]}
            timeZone="Asia/Shanghai"
        >
            {children}
        </NextIntlClientProvider>
    );
}
