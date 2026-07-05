import type { Group, GroupUpdateRequest } from '@/api/endpoints/group';
import type { LLMChannel } from '@/api/endpoints/model';
import type { GroupEditorValues } from './Editor';
import type { SelectedMember } from './MemberTypes';
import { buildChannelNameByModelKey, modelChannelKey } from './utils';

export function buildEnabledByModelKey(modelChannels: LLMChannel[]) {
    const map = new Map<string, boolean>();
    modelChannels.forEach((mc) => {
        map.set(modelChannelKey(mc.channel_id, mc.name), mc.enabled);
    });
    return map;
}

export function buildGroupNameById(groups: Group[]) {
    const map = new Map<number, string>();
    groups.forEach((group) => {
        if (group.id) map.set(group.id, group.name);
    });
    return map;
}

export function buildDisplayMembers(group: Group, modelChannels: LLMChannel[], allGroups: Group[]): SelectedMember[] {
    const channelNameByKey = buildChannelNameByModelKey(modelChannels);
    const enabledByKey = buildEnabledByModelKey(modelChannels);
    const groupNameById = buildGroupNameById(allGroups);

    return [...(group.items || [])]
        .sort((a, b) => a.priority - b.priority)
        .map((item): SelectedMember => {
            const itemType = item.type ?? 'channel';
            if (itemType === 'group') {
                const groupId = item.target_group_id!;
                return {
                    type: 'group',
                    id: `group-${groupId}`,
                    target_group_id: groupId,
                    target_group_name: groupNameById.get(groupId) || `Group ${groupId}`,
                    item_id: item.id,
                    weight: item.weight,
                    disabled: item.disabled ?? false,
                };
            }

            const channelID = item.channel_id ?? 0;
            const modelName = item.model_name ?? '';
            const key = modelChannelKey(channelID, modelName);
            return {
                type: 'channel',
                id: key,
                name: modelName,
                enabled: enabledByKey.get(key) ?? true,
                channel_id: channelID,
                channel_name: channelNameByKey.get(key) ?? `Channel ${channelID}`,
                item_id: item.id,
                weight: item.weight,
                disabled: item.disabled ?? false,
            };
        });
}

export function buildPriorityByItemId(group: Group) {
    const map = new Map<number, number>();
    (group.items || []).forEach((item) => {
        if (item.id !== undefined) map.set(item.id, item.priority);
    });
    return map;
}

export function buildGroupEditorUpdatePayload(group: Group, values: GroupEditorValues): GroupUpdateRequest | null {
    if (!group.id) return null;

    const originalItems = [...(group.items || [])].sort((a, b) => a.priority - b.priority);
    const originalById = new Map<number, { priority: number; weight: number }>();
    const originalIds = new Set<number>();
    originalItems.forEach((item) => {
        if (typeof item.id === 'number') {
            originalIds.add(item.id);
            originalById.set(item.id, { priority: item.priority, weight: item.weight });
        }
    });

    const newIds = new Set<number>();
    values.members.forEach((member) => {
        if (typeof member.item_id === 'number') newIds.add(member.item_id);
    });

    const itemsToDelete = Array.from(originalIds).filter((id) => !newIds.has(id));
    const itemsToAdd = values.members
        .map((member, index) => ({ member, priority: index + 1 }))
        .filter(({ member }) => typeof member.item_id !== 'number')
        .map(({ member, priority }) => {
            if (member.type === 'group') {
                return {
                    type: 'group' as const,
                    target_group_id: member.target_group_id,
                    priority,
                    weight: member.weight ?? 1,
                };
            }
            return {
                type: 'channel' as const,
                channel_id: member.channel_id,
                model_name: member.name,
                priority,
                weight: member.weight ?? 1,
            };
        });

    const itemsToUpdate = values.members
        .map((member, index) => ({ member, priority: index + 1 }))
        .filter(({ member }) => typeof member.item_id === 'number')
        .map(({ member, priority }) => {
            const id = member.item_id!;
            const original = originalById.get(id);
            const weight = member.weight ?? 1;
            if (!original) return null;
            if (original.priority === priority && original.weight === weight) return null;
            return { id, priority, weight };
        })
        .filter((item): item is { id: number; priority: number; weight: number } => item !== null);

    const payload: GroupUpdateRequest = { id: group.id };
    const nextName = values.name.trim();
    const nextRegex = (values.match_regex ?? '').trim();
    const nextFirstTokenTimeOut = values.first_token_time_out ?? 0;
    const nextSessionKeepTime = values.session_keep_time ?? 0;

    if (nextName && nextName !== group.name) payload.name = nextName;
    if (values.mode !== group.mode) payload.mode = values.mode;
    if (nextRegex !== (group.match_regex ?? '')) payload.match_regex = nextRegex;
    if (nextFirstTokenTimeOut !== (group.first_token_time_out ?? 0)) payload.first_token_time_out = nextFirstTokenTimeOut;
    if (nextSessionKeepTime !== (group.session_keep_time ?? 0)) payload.session_keep_time = nextSessionKeepTime;
    if (itemsToAdd.length) payload.items_to_add = itemsToAdd;
    if (itemsToUpdate.length) payload.items_to_update = itemsToUpdate;
    if (itemsToDelete.length) payload.items_to_delete = itemsToDelete;

    return Object.keys(payload).length === 1 ? null : payload;
}
