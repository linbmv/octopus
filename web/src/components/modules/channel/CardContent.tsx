import { useState } from 'react';
import { useTranslations } from 'next-intl';
import { useDeleteChannel, useUpdateChannel, type Channel, type UpdateChannelRequest } from '@/api/endpoints/channel';
import { type StatsMetricsFormatted } from '@/api/endpoints/stats';
import { Tabs, TabsContent, TabsContents } from '@/components/animate-ui/primitives/animate/tabs';
import { MorphingDialogClose, MorphingDialogDescription, MorphingDialogTitle, useMorphingDialog } from '@/components/ui/morphing-dialog';
import { ChannelForm, type ChannelFormData } from './Form';
import { ChannelOverview } from './ChannelOverview';
import { normalizeHeaderRules, normalizeJSONRewriteRules } from './request-rewrite';

function initialChannelForm(channel: Channel): ChannelFormData {
    return {
        name: channel.name,
        type: channel.type,
        enabled: channel.enabled,
        base_urls: channel.base_urls?.length ? channel.base_urls : [{ url: '', delay: 0 }],
        custom_header: channel.custom_header ?? [],
        header_rules: channel.header_rules ?? [],
        json_rewrite_rules: channel.json_rewrite_rules ?? [],
        channel_proxy: channel.channel_proxy ?? '',
        param_override: channel.param_override ?? '',
        keys: channel.keys.length > 0 ? channel.keys.map((key) => ({ ...key })) : [{ enabled: true, channel_key: '', remark: '' }],
        model: channel.model,
        custom_model: channel.custom_model,
        proxy: channel.proxy,
        auto_sync: channel.auto_sync,
        auto_group: channel.auto_group,
        match_regex: channel.match_regex ?? '',
        raw_passthrough: channel.raw_passthrough,
        rpm_limit: channel.rpm_limit ?? 0,
        max_concurrency: channel.max_concurrency ?? 0,
        user_agent: channel.user_agent ?? '',
        policy_profile: channel.policy_profile ?? 'standard',
        self_healing_enabled: channel.self_healing_enabled ?? false,
    };
}

function equalJSON(left: unknown, right: unknown): boolean {
    return JSON.stringify(left ?? []) === JSON.stringify(right ?? []);
}

function channelUpdateRequest(channel: Channel, form: ChannelFormData): UpdateChannelRequest {
    const request: UpdateChannelRequest = { id: channel.id };
    if (form.name !== channel.name) request.name = form.name;
    if (form.type !== channel.type) request.type = form.type;
    if (form.enabled !== channel.enabled) request.enabled = form.enabled;
    if (!equalJSON(form.base_urls, channel.base_urls)) request.base_urls = (form.base_urls ?? []).filter((url) => url.url.trim()).map((url) => ({ url: url.url.trim(), delay: Number(url.delay || 0) }));
    if (form.model !== channel.model) request.model = form.model;
    if (form.custom_model !== channel.custom_model) request.custom_model = form.custom_model;
    if (form.proxy !== channel.proxy) request.proxy = form.proxy;
    if (form.auto_sync !== channel.auto_sync) request.auto_sync = form.auto_sync;
    if (form.auto_group !== channel.auto_group) request.auto_group = form.auto_group;
    const headers = (form.custom_header ?? []).map((header) => ({ header_key: header.header_key.trim(), header_value: header.header_value })).filter((header) => header.header_key && header.header_value !== '');
    if (!equalJSON(headers, channel.custom_header)) request.custom_header = headers;
    const headerRules = normalizeHeaderRules(form.header_rules);
    if (!equalJSON(headerRules, channel.header_rules)) request.header_rules = headerRules;
    const jsonRules = normalizeJSONRewriteRules(form.json_rewrite_rules);
    if (!equalJSON(jsonRules, channel.json_rewrite_rules)) request.json_rewrite_rules = jsonRules;
    setTrimmedChange(request, 'channel_proxy', form.channel_proxy, channel.channel_proxy);
    setTrimmedChange(request, 'param_override', form.param_override, channel.param_override);
    setTrimmedChange(request, 'match_regex', form.match_regex, channel.match_regex);
    setTrimmedChange(request, 'user_agent', form.user_agent, channel.user_agent);
    if (form.policy_profile !== channel.policy_profile) request.policy_profile = form.policy_profile;
    if (form.raw_passthrough !== channel.raw_passthrough) request.raw_passthrough = form.raw_passthrough;
    if (form.self_healing_enabled !== (channel.self_healing_enabled ?? false)) {
        request.self_healing_enabled = form.self_healing_enabled;
    }
    const rpm = Math.max(0, Number(form.rpm_limit || 0));
    if (rpm !== (channel.rpm_limit ?? 0)) request.rpm_limit = rpm;
    const concurrency = Math.max(0, Number(form.max_concurrency || 0));
    if (concurrency !== (channel.max_concurrency ?? 0)) request.max_concurrency = concurrency;
    applyKeyChanges(request, channel, form);
    return request;
}

function setTrimmedChange(request: UpdateChannelRequest, key: 'channel_proxy' | 'param_override' | 'match_regex' | 'user_agent', next: string, current?: string | null) {
    const value = next.trim();
    if (value !== (current ?? '')) request[key] = value;
}

function applyKeyChanges(request: UpdateChannelRequest, channel: Channel, form: ChannelFormData) {
    const original = new Map(channel.keys.map((key) => [key.id, key]));
    const nextKeys = form.keys ?? [];
    const nextIDs = new Set(nextKeys.flatMap((key) => typeof key.id === 'number' ? [key.id] : []));
    const deleted = channel.keys.filter((key) => !nextIDs.has(key.id)).map((key) => key.id);
    const added = nextKeys.filter((key) => !key.id && key.channel_key.trim()).map((key) => ({ enabled: key.enabled, channel_key: key.channel_key, remark: key.remark ?? '' }));
    const updated = nextKeys.flatMap((key) => {
        if (typeof key.id !== 'number' || !original.has(key.id)) return [];
        const before = original.get(key.id)!;
        const change: { id: number; enabled?: boolean; channel_key?: string; remark?: string } = { id: key.id };
        if (key.enabled !== before.enabled) change.enabled = key.enabled;
        if (key.channel_key !== before.channel_key) change.channel_key = key.channel_key;
        if ((key.remark ?? '') !== before.remark) change.remark = key.remark ?? '';
        return Object.keys(change).length > 1 ? [change] : [];
    });
    if (added.length) request.keys_to_add = added;
    if (updated.length) request.keys_to_update = updated;
    if (deleted.length) request.keys_to_delete = deleted;
}

export function CardContent({ channel, stats }: { channel: Channel; stats: StatsMetricsFormatted }) {
    const { setIsOpen } = useMorphingDialog();
    const updateChannel = useUpdateChannel();
    const deleteChannel = useDeleteChannel();
    const [isEditing, setIsEditing] = useState(false);
    const [isConfirmingDelete, setIsConfirmingDelete] = useState(false);
    const [formData, setFormData] = useState<ChannelFormData>(() => initialChannelForm(channel));
    const t = useTranslations('channel.detail');

    const handleUpdate = (event: React.FormEvent<HTMLFormElement>) => {
        event.preventDefault();
        updateChannel.mutate(channelUpdateRequest(channel, formData), { onSuccess: () => { setIsEditing(false); setIsOpen(false); } });
    };
    const handleDelete = () => {
        if (!isConfirmingDelete) return setIsConfirmingDelete(true);
        setIsOpen(false);
        setTimeout(() => deleteChannel.mutate(channel.id), 300);
    };

    return (
        <>
            <MorphingDialogTitle>
                <header className="mb-6 flex items-center justify-between">
                    <h2 className="text-2xl font-bold text-card-foreground">{isEditing ? t('title.edit') : t('title.view')}</h2>
                    <MorphingDialogClose className="relative top-0 right-0" variants={{ initial: { opacity: 0, scale: 0.8 }, animate: { opacity: 1, scale: 1 }, exit: { opacity: 0, scale: 0.8 } }} />
                </header>
            </MorphingDialogTitle>
            <MorphingDialogDescription>
                <Tabs value={isEditing ? 'editing' : 'viewing'}>
                    <TabsContents>
                        <TabsContent value="viewing">
                            <ChannelOverview
                                channel={channel}
                                stats={stats}
                                confirmingDelete={isConfirmingDelete}
                                deletePending={deleteChannel.isPending}
                                onEdit={() => isConfirmingDelete ? setIsConfirmingDelete(false) : setIsEditing(true)}
                                onDelete={handleDelete}
                            />
                        </TabsContent>
                        <TabsContent value="editing">
                            <ChannelForm formData={formData} onFormDataChange={setFormData} onSubmit={handleUpdate} isPending={updateChannel.isPending} submitText={t('actions.save')} pendingText={t('actions.saving')} onCancel={() => setIsEditing(false)} cancelText={t('actions.cancel')} idPrefix="channel" />
                        </TabsContent>
                    </TabsContents>
                </Tabs>
            </MorphingDialogDescription>
        </>
    );
}
