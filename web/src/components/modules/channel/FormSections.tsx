import { X, Plus } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Switch } from '@/components/ui/switch';
import type { Channel } from '@/api/endpoints/channel';
import type { ChannelKeyFormItem } from './Form';

export function BaseUrlsSection({
    idPrefix,
    baseUrls,
    onAdd,
    onUpdate,
    onRemove,
}: {
    idPrefix: string;
    baseUrls: Channel['base_urls'];
    onAdd: () => void;
    onUpdate: (idx: number, patch: Partial<Channel['base_urls'][number]>) => void;
    onRemove: (idx: number) => void;
}) {
    const t = useTranslations('channel.form');

    return (
        <div className="space-y-2">
            <div className="flex items-center justify-between">
                <label className="text-sm font-medium text-card-foreground">
                    {t('baseUrls')} {baseUrls.length > 0 ? `(${baseUrls.length})` : ''}
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
                {(baseUrls ?? []).map((u, idx) => (
                    <div key={`baseurl-${idx}`} className="flex items-center gap-2">
                        <Input
                            id={`${idPrefix}-base-${idx}`}
                            type="url"
                            value={u.url}
                            onChange={(e) => onUpdate(idx, { url: e.target.value })}
                            placeholder={t('baseUrlUrl')}
                            required={idx === 0}
                            className="rounded-xl flex-1"
                        />
                        <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            onClick={() => onRemove(idx)}
                            disabled={(baseUrls ?? []).length <= 1}
                            className="h-8 w-8 p-0 rounded-xl text-muted-foreground hover:text-destructive disabled:opacity-40 hover:bg-transparent"
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
                            className="rounded-xl flex-1"
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
