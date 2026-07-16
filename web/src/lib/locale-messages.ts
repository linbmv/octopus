import type { Locale } from '@/stores/setting';

import enMessages from '../../public/locale/en.json';
import zh_hansMessages from '../../public/locale/zh_hans.json';
import zh_hantMessages from '../../public/locale/zh_hant.json';

export const localeMessages: Record<Locale, typeof zh_hansMessages> = {
    zh_hans: zh_hansMessages,
    zh_hant: zh_hantMessages,
    en: enMessages,
};
