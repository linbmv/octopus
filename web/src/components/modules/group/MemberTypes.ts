import type { LLMChannel } from '@/api/endpoints/model';

export interface SelectedChannelMember extends LLMChannel {
    type: 'channel';
    id: string;
    item_id?: number;
    weight?: number;
    disabled?: boolean;
}

export interface SelectedGroupMember {
    type: 'group';
    id: string;
    target_group_id: number;
    target_group_name: string;
    item_id?: number;
    weight?: number;
    disabled?: boolean;
}

export type SelectedMember = SelectedChannelMember | SelectedGroupMember;
