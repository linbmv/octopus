import { AutoGroupType, ChannelPolicyProfile, ChannelType, type Channel, useFetchModel } from '@/api/endpoints/channel';
import {
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
    SelectValue,
} from '@/components/ui/select';
import { Switch } from '@/components/ui/switch';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { toast } from '@/components/common/Toast';
import { useTranslations } from 'next-intl';
import { useEffect } from 'react';
import { BaseUrlsSection, ChannelKeysSection, ChannelModelSection } from './FormSections';
import { ChannelAdvancedSection } from './FormAdvancedSection';

const CODEX_OAUTH_BASE_URL = 'https://chatgpt.com/backend-api/codex';

export interface ChannelKeyFormItem {
    id?: number;
    enabled: boolean;
    channel_key: string;
    status_code?: number;
    last_use_time_stamp?: number;
    total_cost?: number;
    remark?: string;
}

export interface ChannelFormData {
    name: string;
    type: ChannelType;
    base_urls: Channel['base_urls'];
    custom_header: Channel['custom_header'];
	header_rules: Channel['header_rules'];
	json_rewrite_rules: Channel['json_rewrite_rules'];
    channel_proxy: string;
    param_override: string;
    keys: ChannelKeyFormItem[];
    model: string;
    custom_model: string;
    enabled: boolean;
    proxy: boolean;
    auto_sync: boolean;
    auto_group: AutoGroupType;
    match_regex: string;
    raw_passthrough: boolean;
    rpm_limit: number;
    max_concurrency: number;
    user_agent: string;
    policy_profile: ChannelPolicyProfile;
    self_healing_enabled: boolean;
}

export interface ChannelFormProps {
    formData: ChannelFormData;
    onFormDataChange: (data: ChannelFormData) => void;
    onSubmit: (event: React.FormEvent<HTMLFormElement>) => void;
    isPending: boolean;
    submitText: string;
    pendingText: string;
    onCancel?: () => void;
    cancelText?: string;
    idPrefix?: string;
}

export function ChannelForm({
    formData,
    onFormDataChange,
    onSubmit,
    isPending,
    submitText,
    pendingText,
    onCancel,
    cancelText,
    idPrefix = 'channel',
}: ChannelFormProps) {
    const t = useTranslations('channel.form');

    // Ensure the form always shows at least 1 row for base_urls / keys / custom_header.
    // This avoids "empty list" UI and also keeps URL + APIKEY layout consistent.
    useEffect(() => {
		if (formData.type === ChannelType.OpenAICodex &&
			(formData.base_urls?.length !== 1 || formData.base_urls[0]?.url.trim() !== CODEX_OAUTH_BASE_URL)) {
			onFormDataChange({ ...formData, base_urls: [{ url: CODEX_OAUTH_BASE_URL, delay: 0 }] });
			return;
		}
        if (!formData.base_urls || formData.base_urls.length === 0) {
            onFormDataChange({ ...formData, base_urls: [{ url: '', delay: 0 }] });
            return;
        }
        if (!formData.keys || formData.keys.length === 0) {
            onFormDataChange({ ...formData, keys: [{ enabled: true, channel_key: '' }] });
            return;
        }
        if (!formData.custom_header || formData.custom_header.length === 0) {
            onFormDataChange({ ...formData, custom_header: [{ header_key: '', header_value: '' }] });
        }
    }, [formData, onFormDataChange]);

    const autoModels = formData.model
        ? formData.model.split(',').map((m) => m.trim()).filter(Boolean)
        : [];
    const customModels = formData.custom_model
        ? formData.custom_model.split(',').map((m) => m.trim()).filter(Boolean)
        : [];
    const fetchModel = useFetchModel();

    const fetchBaseUrls = (formData.base_urls ?? [])
        .filter((u) => u.url.trim())
        .map((u) => ({ ...u, url: u.url.trim(), delay: Math.max(0, Number(u.delay) || 0) }));
    const fetchKeys = (formData.keys ?? [])
        .filter((k) => k.channel_key.trim())
        .map((k) => ({ enabled: k.enabled, channel_key: k.channel_key.trim() }));
    const effectiveKey =
        fetchKeys.find((k) => k.enabled && k.channel_key.trim())?.channel_key.trim() || '';
    const canRefreshModels = fetchBaseUrls.length > 0 && Boolean(effectiveKey);

    const updateModels = (nextAuto: string[], nextCustom: string[]) => {
        const model = nextAuto.join(',');
        const custom_model = nextCustom.join(',');
        if (formData.model === model && formData.custom_model === custom_model) return;
        onFormDataChange({ ...formData, model, custom_model });
    };

    const handleRefreshModels = async () => {
        if (!canRefreshModels) return;
        fetchModel.mutate(
            {
                type: formData.type,
                base_urls: fetchBaseUrls,
                keys: fetchKeys,
                proxy: formData.proxy,
                channel_proxy: formData.channel_proxy?.trim() || null,
                match_regex: formData.match_regex.trim() || null,
                custom_header: (formData.custom_header ?? [])
                    .filter((h) => h.header_key.trim())
                    .map((h) => ({ ...h, header_key: h.header_key.trim() })),
            },
            {
                onSuccess: (data) => {
                    if (data && data.length > 0) {
                        const nextAuto = Array.from(new Set([...autoModels, ...data].map((m) => m.trim()).filter(Boolean)));
                        updateModels(nextAuto, customModels);
                        toast.success(t('modelRefreshSuccess'));
                    } else {
                        toast.warning(t('modelRefreshEmpty'));
                    }
                },
                onError: (error) => {
                    let errorMessage: string;
                    if (error instanceof Error) {
                        errorMessage = error.message;
                    } else if (error && typeof error === 'object' && 'message' in error && typeof (error as { message?: unknown }).message === 'string') {
                        // 后端 ApiError（{ code, message }）不是 Error 实例，避免回退成 "[object Object]"。
                        errorMessage = (error as { message: string }).message;
                    } else {
                        errorMessage = String(error);
                    }
                    toast.error(t('modelRefreshFailed'), { description: errorMessage });
                },
            }
        );
    };

    const handleAddKey = () => {
        onFormDataChange({
            ...formData,
            keys: [...formData.keys, { enabled: true, channel_key: '' }],
        });
    };

    const handleUpdateKey = (idx: number, patch: Partial<ChannelKeyFormItem>) => {
        const next = formData.keys.map((k, i) => (i === idx ? { ...k, ...patch } : k));
        onFormDataChange({ ...formData, keys: next });
    };

    const handleRemoveKey = (idx: number) => {
        const curr = formData.keys ?? [];
        if (curr.length <= 1) return;
        const next = curr.filter((_, i) => i !== idx);
        onFormDataChange({ ...formData, keys: next });
    };

    const handleAddBaseUrl = () => {
        onFormDataChange({
            ...formData,
            base_urls: [...(formData.base_urls ?? []), { url: '', delay: 0 }],
        });
    };

    const handleUpdateBaseUrl = (idx: number, patch: Partial<Channel['base_urls'][number]>) => {
        const next = (formData.base_urls ?? []).map((u, i) => (i === idx ? { ...u, ...patch } : u));
        onFormDataChange({ ...formData, base_urls: next });
    };

    const handleRemoveBaseUrl = (idx: number) => {
        const curr = formData.base_urls ?? [];
        if (curr.length <= 1) return;
        onFormDataChange({ ...formData, base_urls: curr.filter((_, i) => i !== idx) });
    };

    const handleAddHeader = () => {
        onFormDataChange({
            ...formData,
            custom_header: [...(formData.custom_header ?? []), { header_key: '', header_value: '' }],
        });
    };

    const handleUpdateHeader = (idx: number, patch: Partial<Channel['custom_header'][number]>) => {
        const next = (formData.custom_header ?? []).map((h, i) => (i === idx ? { ...h, ...patch } : h));
        onFormDataChange({ ...formData, custom_header: next });
    };

    const handleRemoveHeader = (idx: number) => {
        const curr = formData.custom_header ?? [];
        if (curr.length <= 1) return;
        onFormDataChange({ ...formData, custom_header: curr.filter((_, i) => i !== idx) });
    };

    return (
        <form onSubmit={onSubmit} className="space-y-4 px-1">
            <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                <div className="space-y-2">
                    <label htmlFor={`${idPrefix}-name`} className="text-sm font-medium text-card-foreground">
                        {t('name')}
                    </label>
                    <Input
                        className='rounded-xl'
                        id={`${idPrefix}-name`}
                        type="text"
                        value={formData.name}
                        onChange={(event) => onFormDataChange({ ...formData, name: event.target.value })}
                        required
                    />
                </div>

                <div className="space-y-2">
                    <label htmlFor={`${idPrefix}-type`} className="text-sm font-medium text-card-foreground">
                        {t('type')}
                    </label>
                    <Select
                        value={String(formData.type)}
                        onValueChange={(value) => {
							const type = value as ChannelType;
							onFormDataChange({
								...formData,
								type,
								...(type === ChannelType.OpenAICodex
									? { base_urls: [{ url: CODEX_OAUTH_BASE_URL, delay: 0 }] }
									: {}),
							});
						}}
                    >
                        <SelectTrigger id={`${idPrefix}-type`} className="rounded-xl w-full border border-border px-4 py-2 text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
                            <SelectValue />
                        </SelectTrigger>
                        <SelectContent className='rounded-xl'>
                            <SelectItem className='rounded-xl' value={String(ChannelType.OpenAIChat)}>{t('typeOpenAIChat')}</SelectItem>
                            <SelectItem className='rounded-xl' value={String(ChannelType.OpenAIResponse)}>{t('typeOpenAIResponse')}</SelectItem>
							<SelectItem className='rounded-xl' value={String(ChannelType.OpenAICodex)}>{t('typeOpenAICodex')}</SelectItem>
                            <SelectItem className='rounded-xl' value={String(ChannelType.Anthropic)}>{t('typeAnthropic')}</SelectItem>
                            <SelectItem className='rounded-xl' value={String(ChannelType.Gemini)}>{t('typeGemini')}</SelectItem>
                            <SelectItem className='rounded-xl' value={String(ChannelType.Volcengine)}>{t('typeVolcengine')}</SelectItem>
                            <SelectItem className='rounded-xl' value={String(ChannelType.OpenAIEmbedding)}>{t('typeOpenAIEmbedding')}</SelectItem>
                        </SelectContent>
                    </Select>
                </div>
            </div>

            <BaseUrlsSection
                idPrefix={idPrefix}
                baseUrls={formData.base_urls}
                onAdd={handleAddBaseUrl}
                onUpdate={handleUpdateBaseUrl}
                onRemove={handleRemoveBaseUrl}
				locked={formData.type === ChannelType.OpenAICodex}
            />

            <ChannelKeysSection
                keys={formData.keys}
                onAdd={handleAddKey}
                onUpdate={handleUpdateKey}
                onRemove={handleRemoveKey}
				codexOAuth={formData.type === ChannelType.OpenAICodex}
            />

            <ChannelModelSection
                idPrefix={idPrefix}
                modelValue={formData.model}
                autoModels={autoModels}
                customModels={customModels}
                onUpdateModels={updateModels}
                onRefreshModels={handleRefreshModels}
                refreshDisabled={!canRefreshModels || fetchModel.isPending}
                isRefreshing={fetchModel.isPending}
            />

            <ChannelAdvancedSection
                idPrefix={idPrefix}
                formData={formData}
                onFormDataChange={onFormDataChange}
                onAddHeader={handleAddHeader}
                onUpdateHeader={handleUpdateHeader}
                onRemoveHeader={handleRemoveHeader}
            />

            <div className="flex flex-wrap items-center justify-between gap-4 p-4 rounded-xl bg-muted/20 border border-border/50">
                <label className="flex items-center gap-2 cursor-pointer">
                    <Switch
                        checked={formData.enabled}
                        onCheckedChange={(checked) => onFormDataChange({ ...formData, enabled: checked })}
                    />
                    <span className="text-sm font-medium text-card-foreground">{t('enabled')}</span>
                </label>
                <div className="flex items-center gap-6">
                    <label className="flex items-center gap-2 cursor-pointer">
                        <Switch
                            checked={formData.proxy}
                            onCheckedChange={(checked) => onFormDataChange({ ...formData, proxy: checked })}
                        />
                        <span className="text-sm text-card-foreground">{t('proxy')}</span>
                    </label>
                    <label className="flex items-center gap-2 cursor-pointer">
                        <Switch
                            checked={formData.auto_sync}
                            onCheckedChange={(checked) => onFormDataChange({ ...formData, auto_sync: checked })}
                        />
                        <span className="text-sm text-card-foreground">{t('autoSync')}</span>
                    </label>
                    {formData.type === ChannelType.OpenAIChat && (
                        <label className="flex items-center gap-2 cursor-pointer" title={t('rawPassthroughHint')}>
                            <Switch
                                checked={formData.raw_passthrough}
                                onCheckedChange={(checked) => onFormDataChange({ ...formData, raw_passthrough: checked })}
                            />
                            <span className="text-sm text-card-foreground">{t('rawPassthrough')}</span>
                        </label>
                    )}
                    <label className="flex items-center gap-2 cursor-pointer" title={t('selfHealingHint')}>
                        <Switch
                            checked={formData.self_healing_enabled}
                            onCheckedChange={(checked) => onFormDataChange({ ...formData, self_healing_enabled: checked })}
                        />
                        <span className="text-sm text-card-foreground">{t('selfHealing')}</span>
                    </label>
                </div>
            </div>

            <div className={`flex flex-col gap-3 pt-2 ${onCancel ? 'sm:flex-row' : ''}`}>
                {onCancel && cancelText && (
                    <Button
                        type="button"
                        variant="secondary"
                        onClick={onCancel}
                        className="w-full sm:flex-1 rounded-2xl h-12"
                    >
                        {cancelText}
                    </Button>
                )}
                <Button
                    type="submit"
                    disabled={isPending}
                    className="w-full sm:flex-1 rounded-2xl h-12"
                >
                    {isPending ? pendingText : submitText}
                </Button>
            </div>
        </form>
    );
}
