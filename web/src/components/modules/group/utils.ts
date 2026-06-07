import type { LLMChannel } from '@/api/endpoints/model';
import { GroupMode } from '@/api/endpoints/group';
import type { SelectedMember } from '@/components/modules/group/ItemList';

export const MODE_LABELS: Record<GroupMode, string> = {
    [GroupMode.RoundRobin]: 'roundRobin',
    [GroupMode.Random]: 'random',
    [GroupMode.Failover]: 'failover',
    [GroupMode.Weighted]: 'weighted',
} as const;

export function normalizeKey(value: string) {
    return value.trim().toLowerCase();
}

export function modelChannelKey(channelId: number, modelName: string) {
    return `${channelId}-${modelName}`;
}

export function memberKey(member: Pick<LLMChannel, 'channel_id' | 'name'> | SelectedMember): string {
    if ('type' in member) {
        // SelectedMember 类型
        if (member.type === 'group') {
            return `group-${member.target_group_id}`;
        } else {
            return modelChannelKey(member.channel_id, member.name);
        }
    } else {
        // LLMChannel 类型
        return modelChannelKey(member.channel_id, member.name);
    }
}

export function matchesGroupName(modelName: string, groupKey: string) {
    if (!groupKey) return false;
    return modelName.toLowerCase().includes(groupKey);
}

export function buildChannelNameByModelKey(modelChannels: LLMChannel[]) {
    const map = new Map<string, string>();
    modelChannels.forEach((mc) => {
        map.set(modelChannelKey(mc.channel_id, mc.name), mc.channel_name);
    });
    return map;
}


