import { AutoGroupType, ChannelPolicyProfile, type Channel } from '@/api/endpoints/channel';
import {
    Accordion,
    AccordionContent,
    AccordionItem,
    AccordionTrigger,
} from '@/components/ui/accordion';
import {
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
    SelectValue,
} from '@/components/ui/select';
import { Input } from '@/components/ui/input';
import { Switch } from '@/components/ui/switch';
import { useTranslations } from 'next-intl';
import type { ChannelFormData } from './Form';
import { CustomHeadersSection } from './FormSections';
import { RequestRewriteRulesSection } from './RequestRewriteRulesSection';

export function ChannelAdvancedSection({
    idPrefix,
    formData,
    onFormDataChange,
    onAddHeader,
    onUpdateHeader,
    onRemoveHeader,
}: {
    idPrefix: string;
    formData: ChannelFormData;
    onFormDataChange: (data: ChannelFormData) => void;
    onAddHeader: () => void;
    onUpdateHeader: (idx: number, patch: Partial<Channel['custom_header'][number]>) => void;
    onRemoveHeader: (idx: number) => void;
}) {
    const t = useTranslations('channel.form');

    return (
        <Accordion type="single" collapsible className="w-full border rounded-xl bg-card">
            <AccordionItem value="advanced" className="border-none">
                <AccordionTrigger className="text-sm font-medium text-card-foreground py-3 px-4 hover:no-underline hover:bg-muted/30 rounded-xl transition-colors">
                    {t('advanced')}
                </AccordionTrigger>
                <AccordionContent className="pt-4 px-4 pb-4 space-y-4 border-t">
                    <div className="space-y-2">
                        <label htmlFor={`${idPrefix}-policy-profile`} className="text-sm font-medium text-card-foreground">
                            {t('policyProfile')}
                        </label>
                        <Select
                            value={formData.policy_profile}
                            onValueChange={(value) => onFormDataChange({ ...formData, policy_profile: value as ChannelPolicyProfile })}
                        >
                            <SelectTrigger id={`${idPrefix}-policy-profile`} className="w-full rounded-xl border border-border px-4 py-2 text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
                                <SelectValue />
                            </SelectTrigger>
                            <SelectContent className="rounded-xl">
                                {Object.values(ChannelPolicyProfile).map((profile) => (
                                    <SelectItem key={profile} className="rounded-xl" value={profile}>
                                        {t(`policyProfiles.${profile}`)}
                                    </SelectItem>
                                ))}
                            </SelectContent>
                        </Select>
                        <p className="text-xs text-muted-foreground">{t('policyProfileHint')}</p>
                    </div>

                    <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                        <div className="space-y-2">
                            <label htmlFor={`${idPrefix}-auto-group`} className="text-sm font-medium text-card-foreground">
                                {t('autoGroup')}
                            </label>
                            <Select
                                value={String(formData.auto_group)}
                                onValueChange={(value) => onFormDataChange({ ...formData, auto_group: Number(value) as AutoGroupType })}
                            >
                                <SelectTrigger id={`${idPrefix}-auto-group`} className="rounded-xl w-full border border-border px-4 py-2 text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
                                    <SelectValue />
                                </SelectTrigger>
                                <SelectContent className="rounded-xl">
                                    <SelectItem className="rounded-xl" value={String(AutoGroupType.None)}>{t('autoGroupNone')}</SelectItem>
                                    <SelectItem className="rounded-xl" value={String(AutoGroupType.Fuzzy)}>{t('autoGroupFuzzy')}</SelectItem>
                                    <SelectItem className="rounded-xl" value={String(AutoGroupType.Exact)}>{t('autoGroupExact')}</SelectItem>
                                    <SelectItem className="rounded-xl" value={String(AutoGroupType.Regex)}>{t('autoGroupRegex')}</SelectItem>
                                </SelectContent>
                            </Select>
                        </div>

                        <div className="space-y-2">
                            <label htmlFor={`${idPrefix}-channel-proxy`} className="text-sm font-medium text-card-foreground">
                                {t('channelProxy')}
                            </label>
                            <Input
                                id={`${idPrefix}-channel-proxy`}
                                type="text"
                                value={formData.channel_proxy}
                                onChange={(e) => onFormDataChange({ ...formData, channel_proxy: e.target.value })}
                                placeholder={t('channelProxyPlaceholder')}
                                className="rounded-xl"
                            />
                        </div>
                    </div>

                    <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                        <div className="space-y-2">
                            <label htmlFor={`${idPrefix}-rpm-limit`} className="text-sm font-medium text-card-foreground">
                                {t('rpmLimit')}
                            </label>
                            <Input
                                id={`${idPrefix}-rpm-limit`}
                                type="number"
                                min={0}
                                step={1}
                                value={formData.rpm_limit}
                                onChange={(e) => onFormDataChange({ ...formData, rpm_limit: Math.max(0, Number(e.target.value || 0)) })}
                                placeholder={t('unlimitedNumber')}
                                className="rounded-xl"
                            />
                        </div>

                        <div className="space-y-2">
                            <label htmlFor={`${idPrefix}-max-concurrency`} className="text-sm font-medium text-card-foreground">
                                {t('maxConcurrency')}
                            </label>
                            <Input
                                id={`${idPrefix}-max-concurrency`}
                                type="number"
                                min={0}
                                step={1}
                                value={formData.max_concurrency}
                                onChange={(e) => onFormDataChange({ ...formData, max_concurrency: Math.max(0, Number(e.target.value || 0)) })}
                                placeholder={t('unlimitedNumber')}
                                className="rounded-xl"
                            />
                        </div>
                    </div>

                    <CustomHeadersSection
                        headers={formData.custom_header}
                        onAdd={onAddHeader}
                        onUpdate={onUpdateHeader}
                        onRemove={onRemoveHeader}
                    />

                    <div className="space-y-2">
                        <label htmlFor={`${idPrefix}-match-regex`} className="text-sm font-medium text-card-foreground">
                            {t('matchRegex')}
                        </label>
                        <Input
                            id={`${idPrefix}-match-regex`}
                            type="text"
                            value={formData.match_regex}
                            onChange={(e) => onFormDataChange({ ...formData, match_regex: e.target.value })}
                            placeholder={t('matchRegexPlaceholder')}
                            className="rounded-xl"
                        />
                    </div>

                    <div className="space-y-2">
                        <label htmlFor={`${idPrefix}-user-agent`} className="text-sm font-medium text-card-foreground">
                            {t('userAgent')}
                        </label>
                        <Input
                            id={`${idPrefix}-user-agent`}
                            type="text"
                            value={formData.user_agent}
                            onChange={(e) => onFormDataChange({ ...formData, user_agent: e.target.value })}
                            placeholder={t('userAgentPlaceholder')}
                            className="rounded-xl"
                        />
                        <p className="text-xs text-muted-foreground">{t('userAgentHint')}</p>
                    </div>

                    <div className="space-y-2">
                        <label htmlFor={`${idPrefix}-param-override`} className="text-sm font-medium text-card-foreground">
                            {t('paramOverride')}
                        </label>
                        <textarea
                            id={`${idPrefix}-param-override`}
                            value={formData.param_override}
                            onChange={(e) => onFormDataChange({ ...formData, param_override: e.target.value })}
                            placeholder={t('paramOverridePlaceholder')}
                            className="min-h-28 w-full rounded-xl border border-border bg-background px-3 py-2 text-sm text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                        />
                    </div>

                    <div className="space-y-3 rounded-xl border border-border/60 bg-muted/10 p-3">
                        <label className="flex items-center gap-2 cursor-pointer">
                            <Switch
                                checked={formData.first_token_timeout_exception_enabled}
                                onCheckedChange={(checked) => onFormDataChange({
                                    ...formData,
                                    first_token_timeout_exception_enabled: checked,
                                    first_token_timeout_exception_seconds: checked && formData.first_token_timeout_exception_seconds <= 120
                                        ? 200
                                        : formData.first_token_timeout_exception_seconds,
                                })}
                            />
                            <span className="text-sm font-medium text-card-foreground">{t('firstTokenTimeoutException')}</span>
                        </label>
                        <p className="text-xs text-muted-foreground">{t('firstTokenTimeoutExceptionHint')}</p>
                        <div className="space-y-2">
                            <label htmlFor={`${idPrefix}-first-token-timeout-exception-seconds`} className="text-sm font-medium text-card-foreground">
                                {t('firstTokenTimeoutExceptionSeconds')}
                            </label>
                            <Input
                                id={`${idPrefix}-first-token-timeout-exception-seconds`}
                                type="number"
                                min={121}
                                max={600}
                                step={1}
                                disabled={!formData.first_token_timeout_exception_enabled}
                                value={formData.first_token_timeout_exception_seconds}
                                onChange={(e) => onFormDataChange({
                                    ...formData,
                                    first_token_timeout_exception_seconds: Math.min(600, Math.max(121, Number(e.target.value || 121))),
                                })}
                                className="rounded-xl"
                            />
                        </div>
                    </div>

                    <RequestRewriteRulesSection
                        formData={formData}
                        onFormDataChange={onFormDataChange}
                    />
                </AccordionContent>
            </AccordionItem>
        </Accordion>
    );
}
