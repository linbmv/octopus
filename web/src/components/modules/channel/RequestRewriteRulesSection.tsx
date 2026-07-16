import type { Channel } from '@/api/endpoints/channel';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import {
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
    SelectValue,
} from '@/components/ui/select';
import { Plus, X } from 'lucide-react';
import { useTranslations } from 'next-intl';
import type { ChannelFormData } from './Form';
import { isProtectedAuthenticationHeader } from './request-rewrite';

const MAX_RULES = 32;

export function RequestRewriteRulesSection({
    formData,
    onFormDataChange,
}: {
    formData: ChannelFormData;
    onFormDataChange: (data: ChannelFormData) => void;
}) {
    const t = useTranslations('channel.form');
    const headerRules = formData.header_rules ?? [];
    const jsonRewriteRules = formData.json_rewrite_rules ?? [];

    const updateHeaderRule = (index: number, patch: Partial<Channel['header_rules'][number]>) => {
        onFormDataChange({
            ...formData,
            header_rules: headerRules.map((rule, ruleIndex) => ruleIndex === index ? { ...rule, ...patch } : rule),
        });
    };
    const updateJSONRule = (index: number, patch: Partial<Channel['json_rewrite_rules'][number]>) => {
        onFormDataChange({
            ...formData,
            json_rewrite_rules: jsonRewriteRules.map((rule, ruleIndex) => ruleIndex === index ? { ...rule, ...patch } : rule),
        });
    };

    return (
        <details className="rounded-xl border border-border bg-muted/10">
            <summary className="cursor-pointer select-none px-4 py-3 text-sm font-medium text-card-foreground">
                {t('requestRewriteAdvanced')}
            </summary>
            <div className="space-y-5 border-t border-border px-4 py-4">
                <p className="text-xs text-muted-foreground">{t('requestRewriteHint')}</p>

                <div className="space-y-2">
                    <div className="flex items-center justify-between gap-2">
                        <label className="text-sm font-medium text-card-foreground">
                            {t('headerRules')} ({headerRules.length}/{MAX_RULES})
                        </label>
                        <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            disabled={headerRules.length >= MAX_RULES}
                            onClick={() => onFormDataChange({
                                ...formData,
                                header_rules: [...headerRules, { action: 'set', header_key: '', header_value: '' }],
                            })}
                            className="h-7 px-2 text-xs"
                        >
                            <Plus className="mr-1 h-3 w-3" />
                            {t('ruleAdd')}
                        </Button>
                    </div>
                    {headerRules.map((rule, index) => (
                        <div key={`header-rule-${index}`} className="grid grid-cols-1 gap-2 rounded-xl border border-border/70 p-3 md:grid-cols-[8rem_1fr_1fr_auto]">
                            <Select value={rule.action || 'set'} onValueChange={(action) => updateHeaderRule(index, { action })}>
                                <SelectTrigger aria-label={t('ruleAction')} className="w-full rounded-xl">
                                    <SelectValue />
                                </SelectTrigger>
                                <SelectContent>
                                    <SelectItem value="set">{t('ruleSet')}</SelectItem>
                                    <SelectItem value="append">{t('ruleAppend')}</SelectItem>
                                    <SelectItem value="remove">{t('ruleRemove')}</SelectItem>
                                </SelectContent>
                            </Select>
                            <Input
                                value={rule.header_key}
                                onChange={(event) => updateHeaderRule(index, { header_key: event.target.value })}
                                placeholder={t('customHeaderKey')}
                                aria-invalid={isProtectedAuthenticationHeader(rule.header_key)}
                                title={isProtectedAuthenticationHeader(rule.header_key) ? t('protectedHeaderHint') : undefined}
                                className="rounded-xl aria-invalid:border-destructive"
                            />
                            <Input
                                value={rule.header_value ?? ''}
                                onChange={(event) => updateHeaderRule(index, { header_value: event.target.value })}
                                placeholder={rule.action === 'remove' ? t('ruleValueUnused') : t('customHeaderValue')}
                                disabled={rule.action === 'remove'}
                                className="rounded-xl"
                            />
                            <Button
                                type="button"
                                variant="ghost"
                                size="sm"
                                onClick={() => onFormDataChange({
                                    ...formData,
                                    header_rules: headerRules.filter((_, ruleIndex) => ruleIndex !== index),
                                })}
                                className="h-9 w-9 p-0 text-muted-foreground hover:text-destructive"
                                aria-label={t('ruleDelete')}
                            >
                                <X className="h-4 w-4" />
                            </Button>
                        </div>
                    ))}
                </div>

                <div className="space-y-2">
                    <div className="flex items-center justify-between gap-2">
                        <label className="text-sm font-medium text-card-foreground">
                            {t('jsonRewriteRules')} ({jsonRewriteRules.length}/{MAX_RULES})
                        </label>
                        <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            disabled={jsonRewriteRules.length >= MAX_RULES}
                            onClick={() => onFormDataChange({
                                ...formData,
                                json_rewrite_rules: [...jsonRewriteRules, { action: 'override', path: '', value: '' }],
                            })}
                            className="h-7 px-2 text-xs"
                        >
                            <Plus className="mr-1 h-3 w-3" />
                            {t('ruleAdd')}
                        </Button>
                    </div>
                    {jsonRewriteRules.map((rule, index) => (
                        <div key={`json-rule-${index}`} className="grid grid-cols-1 gap-2 rounded-xl border border-border/70 p-3 md:grid-cols-[8rem_1fr_1fr_auto]">
                            <Select value={rule.action || 'override'} onValueChange={(action) => updateJSONRule(index, { action })}>
                                <SelectTrigger aria-label={t('ruleAction')} className="w-full rounded-xl">
                                    <SelectValue />
                                </SelectTrigger>
                                <SelectContent>
                                    <SelectItem value="override">{t('ruleOverride')}</SelectItem>
                                    <SelectItem value="remove">{t('ruleRemove')}</SelectItem>
                                </SelectContent>
                            </Select>
                            <Input
                                value={rule.path}
                                onChange={(event) => updateJSONRule(index, { path: event.target.value })}
                                placeholder={t('jsonPathPlaceholder')}
                                className="rounded-xl font-mono"
                            />
                            <Input
                                value={rule.value ?? ''}
                                onChange={(event) => updateJSONRule(index, { value: event.target.value })}
                                placeholder={rule.action === 'remove' ? t('ruleValueUnused') : t('jsonValuePlaceholder')}
                                disabled={rule.action === 'remove'}
                                className="rounded-xl font-mono"
                            />
                            <Button
                                type="button"
                                variant="ghost"
                                size="sm"
                                onClick={() => onFormDataChange({
                                    ...formData,
                                    json_rewrite_rules: jsonRewriteRules.filter((_, ruleIndex) => ruleIndex !== index),
                                })}
                                className="h-9 w-9 p-0 text-muted-foreground hover:text-destructive"
                                aria-label={t('ruleDelete')}
                            >
                                <X className="h-4 w-4" />
                            </Button>
                        </div>
                    ))}
                </div>
            </div>
        </details>
    );
}
