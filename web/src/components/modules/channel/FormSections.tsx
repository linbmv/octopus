import { X, Plus, RefreshCw } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { isProtectedAuthenticationHeader } from './request-rewrite';
import { useRef, useState } from 'react';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Switch } from '@/components/ui/switch';
import type { Channel } from '@/api/endpoints/channel';
import type { ChannelKeyFormItem } from './Form';

export function ChannelModelSection({
    idPrefix,
    modelValue,
    autoModels,
    customModels,
    onUpdateModels,
    onRefreshModels,
    refreshDisabled,
    isRefreshing,
}: {
    idPrefix: string;
    modelValue: string;
    autoModels: string[];
    customModels: string[];
    onUpdateModels: (nextAuto: string[], nextCustom: string[]) => void;
    onRefreshModels: () => void;
    refreshDisabled: boolean;
    isRefreshing: boolean;
}) {
    const t = useTranslations('channel.form');
    const [inputValue, setInputValue] = useState('');
    const inputRef = useRef<HTMLInputElement>(null);

    const handleAddModel = (model: string) => {
        const trimmedModel = model.trim();
        if (trimmedModel && !customModels.includes(trimmedModel) && !autoModels.includes(trimmedModel)) {
            onUpdateModels(autoModels, [...customModels, trimmedModel]);
        }
        setInputValue('');
    };

    const handleInputKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
        if (e.key === 'Enter') {
            e.preventDefault();
            if (inputValue.trim()) handleAddModel(inputValue);
        }
    };

    return (
        <div className="space-y-2">
            <div className="flex items-center justify-between">
                <label className="text-sm font-medium text-card-foreground">{t('model')}</label>
                <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    onClick={onRefreshModels}
                    disabled={refreshDisabled}
                    className="h-6 px-2 text-xs text-muted-foreground/50 hover:text-muted-foreground hover:bg-transparent"
                >
                    <RefreshCw className={`h-3 w-3 mr-1 ${isRefreshing ? 'animate-spin' : ''}`} />
                    {t('modelRefresh')}
                </Button>
            </div>
            <input type="hidden" value={modelValue} required />

            <div className="relative">
                <Input
                    ref={inputRef}
                    id={`${idPrefix}-model-custom`}
                    type="text"
                    value={inputValue}
                    onChange={(e) => setInputValue(e.target.value)}
                    onKeyDown={handleInputKeyDown}
                    placeholder={t('modelCustomPlaceholder')}
                    className="pr-10 rounded-xl"
                />
                {inputValue.trim() && !customModels.includes(inputValue.trim()) && !autoModels.includes(inputValue.trim()) && (
                    <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        onClick={() => handleAddModel(inputValue)}
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
                        {t('modelSelected')} {(autoModels.length + customModels.length) > 0 && `(${autoModels.length + customModels.length})`}
                    </label>
                    {(autoModels.length + customModels.length) > 0 && (
                        <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            onClick={() => onUpdateModels([], [])}
                            className="h-6 px-2 text-xs text-muted-foreground/50 hover:text-muted-foreground hover:bg-transparent"
                        >
                            {t('modelClearAll')}
                        </Button>
                    )}
                </div>
                <div className="rounded-xl border border-border bg-muted/30 p-2.5 max-h-40 min-h-12 overflow-y-auto">
                    {(autoModels.length + customModels.length) > 0 ? (
                        <div className="flex flex-wrap gap-1.5">
                            {autoModels.map((model) => (
                                <Badge key={model} variant="secondary" className="bg-muted hover:bg-muted/80">
                                    {model}
                                    <button
                                        type="button"
                                        onClick={() => onUpdateModels(autoModels.filter((m) => m !== model), customModels)}
                                        className="ml-1 rounded-sm opacity-70 hover:opacity-100 focus:outline-none focus:ring-1 focus:ring-ring"
                                    >
                                        <X className="h-3 w-3" />
                                    </button>
                                </Badge>
                            ))}
                            {customModels.map((model) => (
                                <Badge key={model} className="bg-primary hover:bg-primary/90">
                                    {model}
                                    <button
                                        type="button"
                                        onClick={() => onUpdateModels(autoModels, customModels.filter((m) => m !== model))}
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
    );
}

export function BaseUrlsSection({
    idPrefix,
    baseUrls,
    onAdd,
    onUpdate,
    onRemove,
	locked = false,
}: {
    idPrefix: string;
    baseUrls: Channel['base_urls'];
    onAdd: () => void;
    onUpdate: (idx: number, patch: Partial<Channel['base_urls'][number]>) => void;
    onRemove: (idx: number) => void;
	locked?: boolean;
}) {
    const t = useTranslations('channel.form');

    return (
        <div className="space-y-2">
            <div className="flex items-center justify-between">
                <label className="text-sm font-medium text-card-foreground">
                    {t('baseUrls')} {baseUrls.length > 0 ? `(${baseUrls.length})` : ''}
                </label>
				{!locked && <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    onClick={onAdd}
                    className="h-6 px-2 text-xs text-muted-foreground/70 hover:text-muted-foreground hover:bg-transparent"
                >
                    <Plus className="h-3 w-3 mr-1" />
                    {t('add')}
                </Button>}
            </div>
            <div className="space-y-2">
                {(baseUrls ?? []).map((u, idx) => (
                    <div key={`baseurl-${idx}`} className="flex items-center gap-2">
                        <Input
                            id={`${idPrefix}-base-${idx}`}
                            type="url"
                            value={u.url}
                            onChange={(e) => onUpdate(idx, { url: e.target.value })}
                            placeholder={t('baseUrlUrl')}
                            required={idx === 0}
							readOnly={locked}
                            className="rounded-xl flex-1"
                        />
						{!locked && <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            onClick={() => onRemove(idx)}
                            disabled={(baseUrls ?? []).length <= 1}
                            className="h-8 w-8 p-0 rounded-xl text-muted-foreground hover:text-destructive disabled:opacity-40 hover:bg-transparent"
                            title="Remove"
                        >
                            <X className="h-4 w-4" />
                        </Button>}
                    </div>
                ))}
            </div>
        </div>
    );
}

export function ChannelKeysSection({
	keys,
	onAdd,
	onUpdate,
	onRemove,
}: {
    keys: ChannelKeyFormItem[];
    onAdd: () => void;
    onUpdate: (idx: number, patch: Partial<ChannelKeyFormItem>) => void;
    onRemove: (idx: number) => void;
}) {
	const t = useTranslations('channel.form');

    return (
        <div className="space-y-2">
            <div className="flex items-center justify-between">
                <label className="text-sm font-medium text-card-foreground">
						{t('apiKey')} {keys.length > 0 ? `(${keys.length})` : ''}
                </label>
                <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    onClick={onAdd}
                    className="h-6 px-2 text-xs text-muted-foreground/70 hover:text-muted-foreground hover:bg-transparent"
                >
                    <Plus className="h-3 w-3 mr-1" />
                    {t('add')}
                </Button>
            </div>
			<div className="space-y-2">
				{(keys ?? []).map((k, idx) => (
						<div key={k.id ?? `new-${idx}`} className="flex items-center gap-2">
						<Input
							type="text"
                            value={k.channel_key}
                            onChange={(e) => onUpdate(idx, { channel_key: e.target.value })}
                            placeholder={t('apiKey')}
                            required={idx === 0}
							className="rounded-xl flex-1"
	                        />
                        <Input
                            type="text"
                            value={k.remark ?? ''}
                            onChange={(e) => onUpdate(idx, { remark: e.target.value })}
                            placeholder={t('remark')}
                            className="rounded-xl w-32"
                        />
                        <Switch
                            checked={k.enabled}
                            onCheckedChange={(checked) => onUpdate(idx, { enabled: checked })}
                        />
                        <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            onClick={() => onRemove(idx)}
                            disabled={(keys ?? []).length <= 1}
                            className="h-8 w-8 p-0 rounded-xl text-muted-foreground hover:text-destructive hover:bg-transparent disabled:opacity-40"
                            title="Remove"
                        >
                            <X className="h-4 w-4" />
                        </Button>
                    </div>
                ))}
            </div>
        </div>
    );
}

export function CustomHeadersSection({
    headers,
    onAdd,
    onUpdate,
    onRemove,
}: {
    headers: Channel['custom_header'];
    onAdd: () => void;
    onUpdate: (idx: number, patch: Partial<Channel['custom_header'][number]>) => void;
    onRemove: (idx: number) => void;
}) {
    const t = useTranslations('channel.form');

    return (
        <div className="space-y-2">
            <div className="flex items-center justify-between">
                <label className="text-sm font-medium text-card-foreground">
                    {t('customHeader')} {headers.length > 0 ? `(${headers.length})` : ''}
                </label>
                <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    onClick={onAdd}
                    className="h-6 px-2 text-xs text-muted-foreground/70 hover:text-muted-foreground hover:bg-transparent"
                >
                    <Plus className="h-3 w-3 mr-1" />
                    {t('customHeaderAdd')}
                </Button>
            </div>
            <div className="space-y-2">
                {(headers ?? []).map((h, idx) => (
                    <div key={`hdr-${idx}`} className="flex items-center gap-2">
                        <Input
                            type="text"
                            value={h.header_key}
                            onChange={(e) => onUpdate(idx, { header_key: e.target.value })}
                            placeholder={t('customHeaderKey')}
							aria-invalid={isProtectedAuthenticationHeader(h.header_key)}
							title={isProtectedAuthenticationHeader(h.header_key) ? t('protectedHeaderHint') : undefined}
							className="rounded-xl flex-1 aria-invalid:border-destructive"
                        />
                        <Input
                            type="text"
                            value={h.header_value}
                            onChange={(e) => onUpdate(idx, { header_value: e.target.value })}
                            placeholder={t('customHeaderValue')}
                            className="rounded-xl flex-1"
                        />
                        <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            onClick={() => onRemove(idx)}
                            disabled={(headers ?? []).length <= 1}
                            className="h-8 w-8 p-0 rounded-xl text-muted-foreground hover:text-destructive hover:bg-transparent disabled:opacity-40"
                            title="Remove"
                        >
                            <X className="h-4 w-4" />
                        </Button>
                    </div>
                ))}
            </div>
        </div>
    );
}
