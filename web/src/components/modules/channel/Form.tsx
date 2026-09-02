import {
    ChannelType,
    type Channel,
    type ChannelModelInput,
    type HeaderRule,
    type JSONRewriteRule,
    type UpdateChannelRequest,
    useCreateChannel,
    useFetchModel,
    useUpdateChannel,
} from '@/api/channel';
import { useMorphingDialog } from '@/components/ui/morphing-dialog';
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
import { Badge } from '@/components/ui/badge';
import {
    Accordion,
    AccordionContent,
    AccordionItem,
    AccordionTrigger,
} from '@/components/ui/accordion';
import { toast } from 'sonner';
import { useTranslations } from 'use-intl';
import { useState } from 'react';
import { RefreshCw, X, Plus } from 'lucide-react';

interface ChannelFormData {
    name: string;
    type: ChannelType;
    base_url: string;
    key: string;
    custom_header: Channel['custom_header'];
    header_rules: HeaderRule[];
    json_rewrite_rules: JSONRewriteRule[];
    channel_proxy: string;
    param_override: string;
    models: ChannelModelInput[];
    enabled: boolean;
    proxy: boolean;
    auto_sync: boolean;
    match_regex: string;
}

// 新建渠道的初始表单值, 自定义 Header 至少保留一行
const emptyFormData: ChannelFormData = {
    name: '',
    type: ChannelType.OpenAIChat,
    base_url: '',
    key: '',
    custom_header: [{ header_key: '', header_value: '' }],
    header_rules: [],
    json_rewrite_rules: [],
    channel_proxy: '',
    param_override: '',
    models: [],
    auto_sync: false,
    enabled: true,
    proxy: false,
    match_regex: '',
};

// 忽略顺序生成模型集合的比较键
const modelsKey = (models: ChannelModelInput[]) => models.map((model) => `${model.source}:${model.name}`).sort().join(',');

// ChannelForm 新建与编辑共用: 传入 channel 时提交变更字段, 否则创建新渠道; 提交或取消后关闭所在弹窗
export function ChannelForm({ channel }: { channel?: Channel }) {
    const t = useTranslations('channel.form');
    const { setIsOpen } = useMorphingDialog();
    const createChannel = useCreateChannel();
    const updateChannel = useUpdateChannel();
    const [formData, setFormData] = useState<ChannelFormData>(channel ? {
        name: channel.name,
        type: channel.type,
        enabled: channel.enabled,
        base_url: channel.base_url,
        key: channel.key,
        custom_header: channel.custom_header.length > 0 ? channel.custom_header : [{ header_key: '', header_value: '' }],
        header_rules: channel.header_rules ?? [],
        json_rewrite_rules: channel.json_rewrite_rules ?? [],
        channel_proxy: channel.channel_proxy ?? '',
        param_override: channel.param_override ?? '',
        models: channel.models.map(({ name, source }) => ({ name, source })),
        proxy: channel.proxy,
        auto_sync: channel.auto_sync,
        match_regex: channel.match_regex ?? '',
    } : emptyFormData);
    // 新建和编辑共存时隔离表单控件的 id
    const idPrefix = channel ? `channel-${channel.id}` : 'new-channel';
    const isPending = channel ? updateChannel.isPending : createChannel.isPending;
    const autoModels = formData.models.filter((model) => model.source === 'auto').map((model) => model.name);
    const manualModels = formData.models.filter((model) => model.source === 'manual').map((model) => model.name);
    const hasModels = formData.models.length > 0;
    const [inputValue, setInputValue] = useState('');
    const trimmedInput = inputValue.trim();
    // 输入的模型名非空且未添加过时才允许追加
    const canAddModel = trimmedInput !== '' && !manualModels.includes(trimmedInput) && !autoModels.includes(trimmedInput);

    const fetchModel = useFetchModel();

    const updateModels = (nextAuto: string[], nextManual: string[]) => {
        const models: ChannelModelInput[] = [
            ...nextAuto.map((name) => ({ name, source: 'auto' as const })),
            ...nextManual.map((name) => ({ name, source: 'manual' as const })),
        ];
        if (JSON.stringify(formData.models) === JSON.stringify(models)) return;
        setFormData({ ...formData, models });
    };

    const handleRefreshModels = () => {
        if (!formData.base_url || !formData.key) return;
        fetchModel.mutate(
            {
                type: formData.type,
                base_url: formData.base_url.trim(),
                key: formData.key.trim(),
                proxy: formData.proxy,
                channel_proxy: formData.channel_proxy.trim() || null,
                match_regex: formData.match_regex.trim() || null,
                custom_header: formData.custom_header.filter((h) => h.header_key.trim()),
            },
            {
                onSuccess: (data) => {
                    if (data && data.length > 0) {
                        const nextAuto = Array.from(new Set([...autoModels, ...data]
                            .map((m) => m.trim())
                            .filter((m) => m && !manualModels.includes(m))));
                        updateModels(nextAuto, manualModels);
                        toast.success(t('modelRefreshSuccess'));
                    } else {
                        toast.warning(t('modelRefreshEmpty'));
                    }
                },
                onError: (error) => {
                    const errorMessage = error instanceof Error ? error.message : String(error);
                    toast.error(t('modelRefreshFailed'), { description: errorMessage });
                },
            }
        );
    };

    const handleAddModel = () => {
        if (canAddModel) updateModels(autoModels, [...manualModels, trimmedInput]);
        setInputValue('');
    };

    const handleInputKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
        if (e.key === 'Enter') {
            e.preventDefault();
            if (trimmedInput) handleAddModel();
        }
    };

    const handleUpdateHeader = (idx: number, patch: Partial<Channel['custom_header'][number]>) => {
        const next = formData.custom_header.map((h, i) => (i === idx ? { ...h, ...patch } : h));
        setFormData({ ...formData, custom_header: next });
    };

    const handleUpdateHeaderRule = (idx: number, patch: Partial<HeaderRule>) => {
        const next = formData.header_rules.map((rule, i) => (i === idx ? { ...rule, ...patch } : rule));
        setFormData({ ...formData, header_rules: next });
    };

    const handleUpdateJSONRule = (idx: number, patch: Partial<JSONRewriteRule>) => {
        const next = formData.json_rewrite_rules.map((rule, i) => (i === idx ? { ...rule, ...patch } : rule));
        setFormData({ ...formData, json_rewrite_rules: next });
    };

    // 新建提交全部字段; 编辑只提交变化字段, 空串对应后端的清空语义
    const handleSubmit = (event: React.FormEvent<HTMLFormElement>) => {
        event.preventDefault();
        if (!hasModels) return;

        const custom_header = formData.custom_header
            .map((h) => ({ header_key: h.header_key.trim(), header_value: h.header_value }))
            .filter((h) => h.header_key && h.header_value !== '');

        // remove 不需要值, 其余动作要求值非空; 缺名规则直接丢弃而不是提交给后端报错
        const header_rules = formData.header_rules
            .map((rule) => ({ ...rule, header_key: rule.header_key.trim() }))
            .filter((rule) => rule.header_key && (rule.action === 'remove' || (rule.header_value ?? '') !== ''))
            .map((rule) => (rule.action === 'remove'
                ? { action: rule.action, header_key: rule.header_key }
                : rule));

        const json_rewrite_rules = formData.json_rewrite_rules
            .map((rule) => ({ ...rule, path: rule.path.trim() }))
            .filter((rule) => rule.path && (rule.action === 'remove' || (rule.value ?? '').trim() !== ''))
            .map((rule) => (rule.action === 'remove'
                ? { action: rule.action, path: rule.path }
                : { ...rule, value: (rule.value ?? '').trim() }));

        if (!channel) {
            createChannel.mutate({
                name: formData.name,
                type: formData.type,
                enabled: formData.enabled,
                base_url: formData.base_url.trim(),
                key: formData.key.trim(),
                models: formData.models,
                proxy: formData.proxy,
                auto_sync: formData.auto_sync,
                custom_header,
                header_rules,
                json_rewrite_rules,
                channel_proxy: formData.channel_proxy.trim(),
                param_override: formData.param_override.trim(),
                match_regex: formData.match_regex.trim(),
            }, { onSuccess: () => setIsOpen(false) });
            return;
        }

        const req: UpdateChannelRequest = { id: channel.id };
        if (formData.name !== channel.name) req.name = formData.name;
        if (formData.type !== channel.type) req.type = formData.type;
        if (formData.enabled !== channel.enabled) req.enabled = formData.enabled;
        if (formData.base_url.trim() !== channel.base_url) req.base_url = formData.base_url.trim();
        if (formData.key.trim() !== channel.key) req.key = formData.key.trim();
        if (modelsKey(formData.models) !== modelsKey(channel.models)) req.models = formData.models;
        if (formData.proxy !== channel.proxy) req.proxy = formData.proxy;
        if (formData.auto_sync !== channel.auto_sync) req.auto_sync = formData.auto_sync;
        if (JSON.stringify(custom_header) !== JSON.stringify(channel.custom_header)) req.custom_header = custom_header;
        if (JSON.stringify(header_rules) !== JSON.stringify(channel.header_rules ?? [])) req.header_rules = header_rules;
        if (JSON.stringify(json_rewrite_rules) !== JSON.stringify(channel.json_rewrite_rules ?? [])) req.json_rewrite_rules = json_rewrite_rules;
        for (const key of ['channel_proxy', 'param_override', 'match_regex'] as const) {
            const next = formData[key].trim();
            if (next !== (channel[key] ?? '')) req[key] = next;
        }

        updateChannel.mutate(req, { onSuccess: () => setIsOpen(false) });
    };

    return (
        <form onSubmit={handleSubmit} className="space-y-4 px-1">
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
                        onChange={(event) => setFormData({ ...formData, name: event.target.value })}
                        required
                    />
                </div>

                <div className="space-y-2">
                    <label htmlFor={`${idPrefix}-type`} className="text-sm font-medium text-card-foreground">
                        {t('type')}
                    </label>
                    <Select
                        value={String(formData.type)}
                        onValueChange={(value) => setFormData({ ...formData, type: value as ChannelType })}
                    >
                        <SelectTrigger id={`${idPrefix}-type`} className="rounded-xl w-full border border-border px-4 py-2 text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
                            <SelectValue />
                        </SelectTrigger>
                        <SelectContent className='rounded-xl'>
                            <SelectItem className='rounded-xl' value={String(ChannelType.OpenAIChat)}>{t('typeOpenAIChat')}</SelectItem>
                            <SelectItem className='rounded-xl' value={String(ChannelType.OpenAIResponse)}>{t('typeOpenAIResponse')}</SelectItem>
                            <SelectItem className='rounded-xl' value={String(ChannelType.Anthropic)}>{t('typeAnthropic')}</SelectItem>
                            <SelectItem className='rounded-xl' value={String(ChannelType.Gemini)}>{t('typeGemini')}</SelectItem>
                            <SelectItem className='rounded-xl' value={String(ChannelType.Volcengine)}>{t('typeVolcengine')}</SelectItem>
                        </SelectContent>
                    </Select>
                </div>
            </div>

            <div className="space-y-2">
                <label htmlFor={`${idPrefix}-base-url`} className="text-sm font-medium text-card-foreground">
                    {t('baseUrl')}
                </label>
                <Input
                    id={`${idPrefix}-base-url`}
                    type="url"
                    value={formData.base_url}
                    onChange={(event) => setFormData({ ...formData, base_url: event.target.value })}
                    placeholder={t('baseUrlUrl')}
                    required
                    className="rounded-xl"
                />
            </div>

            <div className="space-y-2">
                <label htmlFor={`${idPrefix}-key`} className="text-sm font-medium text-card-foreground">
                    {t('apiKey')}
                </label>
                <Input
                    id={`${idPrefix}-key`}
                    type="text"
                    value={formData.key}
                    onChange={(event) => setFormData({ ...formData, key: event.target.value })}
                    placeholder={t('apiKey')}
                    required
                    className="rounded-xl"
                />
            </div>

            <div className="space-y-2">
                <div className="flex items-center justify-between">
                    <label className="text-sm font-medium text-card-foreground">{t('model')}</label>
                    <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        onClick={handleRefreshModels}
                        disabled={!formData.base_url || !formData.key || fetchModel.isPending}
                        className="h-6 px-2 text-xs text-muted-foreground/50 hover:text-muted-foreground hover:bg-transparent"
                    >
                        <RefreshCw className={`h-3 w-3 mr-1 ${fetchModel.isPending ? 'animate-spin' : ''}`} />
                        {t('modelRefresh')}
                    </Button>
                </div>
                <div className="relative">
                    <Input
                        id={`${idPrefix}-model-custom`}
                        type="text"
                        value={inputValue}
                        onChange={(e) => setInputValue(e.target.value)}
                        onKeyDown={handleInputKeyDown}
                        placeholder={t('modelCustomPlaceholder')}
                        className="pr-10 rounded-xl"
                    />
                    {canAddModel && (
                        <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            onClick={handleAddModel}
                            className="absolute rounded-lg right-1 top-1/2 -translate-y-1/2 h-7 w-7 p-0 text-muted-foreground hover:bg-accent hover:text-accent-foreground transition-colors"
                            title={t('modelAdd')}
                        >
                            <Plus className="size-4" />
                        </Button>
                    )}
                </div>

                <div className="space-y-2">
                    <div className="flex items-center justify-between">
                        <label className="text-xs font-medium text-card-foreground">
                            {t('modelSelected')} {hasModels && `(${formData.models.length})`}
                        </label>
                        {hasModels && (
                            <Button
                                type="button"
                                variant="ghost"
                                size="sm"
                                onClick={() => {
                                    updateModels([], []);
                                }}
                                className="h-6 px-2 text-xs text-muted-foreground/50 hover:text-muted-foreground hover:bg-transparent"
                            >
                                {t('modelClearAll')}
                            </Button>
                        )}
                    </div>
                    <div className="rounded-xl border border-border bg-muted/30 p-2.5 max-h-40 min-h-12 overflow-y-auto">
                        {hasModels ? (
                            <div className="flex flex-wrap gap-1.5">
                                {autoModels.map((model) => (
                                    <Badge key={model} variant="secondary" className="bg-muted hover:bg-muted/80">
                                        {model}
                                        <button
                                            type="button"
                                            onClick={() => updateModels(autoModels.filter((m) => m !== model), manualModels)}
                                            className="ml-1 rounded-sm opacity-70 hover:opacity-100 focus:outline-none focus:ring-1 focus:ring-ring"
                                        >
                                            <X className="h-3 w-3" />
                                        </button>
                                    </Badge>
                                ))}
                                {manualModels.map((model) => (
                                    <Badge key={model} className="bg-primary hover:bg-primary/90">
                                        {model}
                                        <button
                                            type="button"
                                            onClick={() => updateModels(autoModels, manualModels.filter((m) => m !== model))}
                                            className="ml-1 rounded-sm opacity-70 hover:opacity-100 focus:outline-none focus:ring-1 focus:ring-ring"
                                        >
                                            <X className="h-3 w-3" />
                                        </button>
                                    </Badge>
                                ))}
                            </div>
                        ) : (
                            <div className="flex items-center justify-center h-8 text-xs text-muted-foreground">
                                {t('modelNoSelected')}
                            </div>
                        )}
                    </div>
                </div>
            </div>

            <Accordion type="single" collapsible className="w-full border rounded-xl bg-card">
                <AccordionItem value="advanced" className="border-none">
                    <AccordionTrigger className="text-sm font-medium text-card-foreground py-3 px-4 hover:no-underline hover:bg-muted/30 rounded-xl transition-colors">
                        {t('advanced')}
                    </AccordionTrigger>
                    <AccordionContent className="pt-4 px-4 pb-4 space-y-4 border-t">
                        <div className="space-y-2">
                            <label htmlFor={`${idPrefix}-channel-proxy`} className="text-sm font-medium text-card-foreground">
                                {t('channelProxy')}
                            </label>
                            <Input
                                id={`${idPrefix}-channel-proxy`}
                                type="text"
                                value={formData.channel_proxy}
                                onChange={(e) => setFormData({ ...formData, channel_proxy: e.target.value })}
                                placeholder={t('channelProxyPlaceholder')}
                                className="rounded-xl"
                            />
                        </div>

                        <div className="space-y-2">
                            <div className="flex items-center justify-between">
                                <label className="text-sm font-medium text-card-foreground">
                                    {t('customHeader')} {formData.custom_header.length > 0 ? `(${formData.custom_header.length})` : ''}
                                </label>
                                <Button
                                    type="button"
                                    variant="ghost"
                                    size="sm"
                                    onClick={() => setFormData({ ...formData, custom_header: [...formData.custom_header, { header_key: '', header_value: '' }] })}
                                    className="h-6 px-2 text-xs text-muted-foreground/70 hover:text-muted-foreground hover:bg-transparent"
                                >
                                    <Plus className="h-3 w-3 mr-1" />
                                    {t('customHeaderAdd')}
                                </Button>
                            </div>
                            <div className="space-y-2">
                                {formData.custom_header.map((h, idx) => (
                                    <div key={`hdr-${idx}`} className="flex items-center gap-2">
                                        <Input
                                            type="text"
                                            value={h.header_key}
                                            onChange={(e) => handleUpdateHeader(idx, { header_key: e.target.value })}
                                            placeholder={t('customHeaderKey')}
                                            className="rounded-xl flex-1"
                                        />
                                        <Input
                                            type="text"
                                            value={h.header_value}
                                            onChange={(e) => handleUpdateHeader(idx, { header_value: e.target.value })}
                                            placeholder={t('customHeaderValue')}
                                            className="rounded-xl flex-1"
                                        />
                                        <Button
                                            type="button"
                                            variant="ghost"
                                            size="sm"
                                            onClick={() => setFormData({ ...formData, custom_header: formData.custom_header.filter((_, i) => i !== idx) })}
                                            disabled={formData.custom_header.length <= 1}
                                            className="h-8 w-8 p-0 rounded-xl text-muted-foreground hover:text-destructive hover:bg-transparent disabled:opacity-40"
                                            title="Remove"
                                        >
                                            <X className="h-4 w-4" />
                                        </Button>
                                    </div>
                                ))}
                            </div>
                        </div>

                        <div className="space-y-2">
                            <div className="flex items-center justify-between">
                                <label className="text-sm font-medium text-card-foreground">
                                    {t('headerRules')} {formData.header_rules.length > 0 ? `(${formData.header_rules.length})` : ''}
                                </label>
                                <Button
                                    type="button"
                                    variant="ghost"
                                    size="sm"
                                    onClick={() => setFormData({ ...formData, header_rules: [...formData.header_rules, { action: 'set', header_key: '', header_value: '' }] })}
                                    className="h-6 px-2 text-xs text-muted-foreground/70 hover:text-muted-foreground hover:bg-transparent"
                                >
                                    <Plus className="h-3 w-3 mr-1" />
                                    {t('headerRulesAdd')}
                                </Button>
                            </div>
                            <p className="text-xs text-muted-foreground/70">{t('headerRulesHint')}</p>
                            <div className="space-y-2">
                                {formData.header_rules.map((rule, idx) => (
                                    <div key={`hdr-rule-${idx}`} className="flex items-center gap-2">
                                        <Select
                                            value={rule.action}
                                            onValueChange={(value) => handleUpdateHeaderRule(idx, { action: value as HeaderRule['action'] })}
                                        >
                                            <SelectTrigger className="rounded-xl w-28">
                                                <SelectValue />
                                            </SelectTrigger>
                                            <SelectContent>
                                                <SelectItem value="set">set</SelectItem>
                                                <SelectItem value="append">append</SelectItem>
                                                <SelectItem value="remove">remove</SelectItem>
                                            </SelectContent>
                                        </Select>
                                        <Input
                                            type="text"
                                            value={rule.header_key}
                                            onChange={(e) => handleUpdateHeaderRule(idx, { header_key: e.target.value })}
                                            placeholder={t('customHeaderKey')}
                                            className="rounded-xl flex-1"
                                        />
                                        <Input
                                            type="text"
                                            value={rule.header_value ?? ''}
                                            onChange={(e) => handleUpdateHeaderRule(idx, { header_value: e.target.value })}
                                            placeholder={t('customHeaderValue')}
                                            disabled={rule.action === 'remove'}
                                            className="rounded-xl flex-1 disabled:opacity-40"
                                        />
                                        <Button
                                            type="button"
                                            variant="ghost"
                                            size="sm"
                                            onClick={() => setFormData({ ...formData, header_rules: formData.header_rules.filter((_, i) => i !== idx) })}
                                            className="h-8 w-8 p-0 rounded-xl text-muted-foreground hover:text-destructive hover:bg-transparent"
                                            title="Remove"
                                        >
                                            <X className="h-4 w-4" />
                                        </Button>
                                    </div>
                                ))}
                            </div>
                        </div>

                        <div className="space-y-2">
                            <div className="flex items-center justify-between">
                                <label className="text-sm font-medium text-card-foreground">
                                    {t('jsonRewriteRules')} {formData.json_rewrite_rules.length > 0 ? `(${formData.json_rewrite_rules.length})` : ''}
                                </label>
                                <Button
                                    type="button"
                                    variant="ghost"
                                    size="sm"
                                    onClick={() => setFormData({ ...formData, json_rewrite_rules: [...formData.json_rewrite_rules, { action: 'override', path: '', value: '' }] })}
                                    className="h-6 px-2 text-xs text-muted-foreground/70 hover:text-muted-foreground hover:bg-transparent"
                                >
                                    <Plus className="h-3 w-3 mr-1" />
                                    {t('jsonRewriteRulesAdd')}
                                </Button>
                            </div>
                            <p className="text-xs text-muted-foreground/70">{t('jsonRewriteRulesHint')}</p>
                            <div className="space-y-2">
                                {formData.json_rewrite_rules.map((rule, idx) => (
                                    <div key={`json-rule-${idx}`} className="flex items-center gap-2">
                                        <Select
                                            value={rule.action}
                                            onValueChange={(value) => handleUpdateJSONRule(idx, { action: value as JSONRewriteRule['action'] })}
                                        >
                                            <SelectTrigger className="rounded-xl w-28">
                                                <SelectValue />
                                            </SelectTrigger>
                                            <SelectContent>
                                                <SelectItem value="override">override</SelectItem>
                                                <SelectItem value="remove">remove</SelectItem>
                                            </SelectContent>
                                        </Select>
                                        <Input
                                            type="text"
                                            value={rule.path}
                                            onChange={(e) => handleUpdateJSONRule(idx, { path: e.target.value })}
                                            placeholder={t('jsonRewritePathPlaceholder')}
                                            className="rounded-xl flex-1"
                                        />
                                        <Input
                                            type="text"
                                            value={rule.value ?? ''}
                                            onChange={(e) => handleUpdateJSONRule(idx, { value: e.target.value })}
                                            placeholder={t('jsonRewriteValuePlaceholder')}
                                            disabled={rule.action === 'remove'}
                                            className="rounded-xl flex-1 disabled:opacity-40"
                                        />
                                        <Button
                                            type="button"
                                            variant="ghost"
                                            size="sm"
                                            onClick={() => setFormData({ ...formData, json_rewrite_rules: formData.json_rewrite_rules.filter((_, i) => i !== idx) })}
                                            className="h-8 w-8 p-0 rounded-xl text-muted-foreground hover:text-destructive hover:bg-transparent"
                                            title="Remove"
                                        >
                                            <X className="h-4 w-4" />
                                        </Button>
                                    </div>
                                ))}
                            </div>
                        </div>

                        <div className="space-y-2">
                            <label htmlFor={`${idPrefix}-match-regex`} className="text-sm font-medium text-card-foreground">
                                {t('matchRegex')}
                            </label>
                            <Input
                                id={`${idPrefix}-match-regex`}
                                type="text"
                                value={formData.match_regex}
                                onChange={(e) => setFormData({ ...formData, match_regex: e.target.value })}
                                placeholder={t('matchRegexPlaceholder')}
                                className="rounded-xl"
                            />
                        </div>

                        <div className="space-y-2">
                            <label htmlFor={`${idPrefix}-param-override`} className="text-sm font-medium text-card-foreground">
                                {t('paramOverride')}
                            </label>
                            <textarea
                                id={`${idPrefix}-param-override`}
                                value={formData.param_override}
                                onChange={(e) => setFormData({ ...formData, param_override: e.target.value })}
                                placeholder={t('paramOverridePlaceholder')}
                                className="min-h-28 w-full rounded-xl border border-border bg-background px-3 py-2 text-sm text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                            />
                        </div>
                    </AccordionContent>
                </AccordionItem>
            </Accordion>

            <div className="flex flex-wrap items-center justify-between gap-4 p-4 rounded-xl bg-muted/20 border border-border/50">
                <label className="flex items-center gap-2 cursor-pointer">
                    <Switch
                        checked={formData.enabled}
                        onCheckedChange={(checked) => setFormData({ ...formData, enabled: checked })}
                    />
                    <span className="text-sm font-medium text-card-foreground">{t('enabled')}</span>
                </label>
                <div className="flex items-center gap-6">
                    <label className="flex items-center gap-2 cursor-pointer">
                        <Switch
                            checked={formData.proxy}
                            onCheckedChange={(checked) => setFormData({ ...formData, proxy: checked })}
                        />
                        <span className="text-sm text-card-foreground">{t('proxy')}</span>
                    </label>
                    <label className="flex items-center gap-2 cursor-pointer">
                        <Switch
                            checked={formData.auto_sync}
                            onCheckedChange={(checked) => setFormData({ ...formData, auto_sync: checked })}
                        />
                        <span className="text-sm text-card-foreground">{t('autoSync')}</span>
                    </label>
                </div>
            </div>

            <div className={`flex flex-col gap-3 pt-2 ${channel ? 'sm:flex-row' : ''}`}>
                {channel && (
                    <Button
                        type="button"
                        variant="secondary"
                        onClick={() => setIsOpen(false)}
                        className="w-full sm:flex-1 rounded-2xl h-12"
                    >
                        {t('cancel')}
                    </Button>
                )}
                <Button
                    type="submit"
                    disabled={isPending || !hasModels}
                    className="w-full sm:flex-1 rounded-2xl h-12"
                >
                    {channel
                        ? (isPending ? t('saving') : t('save'))
                        : (isPending ? t('submitting') : t('submit'))}
                </Button>
            </div>
        </form>
    );
}
