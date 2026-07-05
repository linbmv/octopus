'use client';

import { useCallback, useMemo, useState } from 'react';
import { useModelChannelList, type LLMChannel } from '@/api/endpoints/model';
import { useGroupList, type Group, type GroupMode } from '@/api/endpoints/group';
import type { SelectedMember } from './ItemList';
import { matchesGroupName, memberKey, normalizeKey } from './utils';

export type GroupEditorValues = {
    name: string;
    match_regex: string;
    mode: GroupMode;
    first_token_time_out: number;
    session_keep_time: number;
    members: SelectedMember[];
};

function parseRegex(input: string): RegExp {
    const inlineMatch = input.match(/^\(\?([ism]+)\)(.+)$/);
    if (inlineMatch) {
        const flagMap: Record<string, string> = { i: 'i', s: 's', m: 'm' };
        const flags = inlineMatch[1].split('').map((f) => flagMap[f] || '').join('');
        return new RegExp(inlineMatch[2], flags);
    }

    return new RegExp(input);
}

function parsePositiveIntegerInput(raw: string): number {
    if (raw.trim() === '') return 0;
    const n = Number.parseInt(raw, 10);
    return Number.isFinite(n) && n > 0 ? n : 0;
}

export function useGroupEditorState(initial?: Partial<GroupEditorValues> & { id?: number }) {
    const { data: modelChannels = [] } = useModelChannelList();
    const { data: allGroups = [] } = useGroupList();

    const [groupName, setGroupName] = useState(initial?.name ?? '');
    const [matchRegex, setMatchRegex] = useState(initial?.match_regex ?? '');
    const [mode, setMode] = useState<GroupMode>((initial?.mode ?? 1) as GroupMode);
    const [firstTokenTimeOut, setFirstTokenTimeOut] = useState<number>(initial?.first_token_time_out ?? 0);
    const [sessionKeepTime, setSessionKeepTime] = useState<number>(initial?.session_keep_time ?? 0);
    const [selectedMembers, setSelectedMembers] = useState<SelectedMember[]>(initial?.members ?? []);
    const [removingIds, setRemovingIds] = useState<Set<string>>(new Set());

    const currentGroupId = initial?.id;
    const groupKey = normalizeKey(groupName);
    const regexKey = matchRegex.trim();

    const { matchedModelChannels, regexError } = useMemo(() => {
        if (regexKey) {
            try {
                const re = parseRegex(regexKey);
                return { matchedModelChannels: modelChannels.filter((mc) => re.test(mc.name)), regexError: '' };
            } catch (e) {
                return { matchedModelChannels: [], regexError: (e as Error)?.message ?? 'Invalid regex' };
            }
        }
        if (!groupKey) return { matchedModelChannels: [], regexError: '' };
        return { matchedModelChannels: modelChannels.filter((mc) => matchesGroupName(mc.name, groupKey)), regexError: '' };
    }, [groupKey, regexKey, modelChannels]);

    const handleAddMember = useCallback((channel: LLMChannel) => {
        const key = memberKey(channel);
        setSelectedMembers((prev) => {
            if (prev.some((m) => m.id === key)) return prev;
            return [...prev, { ...channel, type: 'channel' as const, id: key, weight: 1 }];
        });
    }, []);

    const autoAddDisabled = useMemo(() => {
        if ((!regexKey && !groupKey) || regexError || matchedModelChannels.length === 0) return true;
        const existing = new Set(selectedMembers.map((m) => m.id));
        return matchedModelChannels.every((mc) => existing.has(memberKey(mc)));
    }, [groupKey, regexKey, regexError, matchedModelChannels, selectedMembers]);

    const handleAutoAdd = useCallback(() => {
        if (matchedModelChannels.length === 0) return;
        setSelectedMembers((prev) => {
            const existing = new Set(prev.map((m) => m.id));
            const toAdd = matchedModelChannels
                .filter((mc) => !existing.has(memberKey(mc)))
                .map((mc) => ({ ...mc, type: 'channel' as const, id: memberKey(mc), weight: 1 }));
            return toAdd.length ? [...prev, ...toAdd] : prev;
        });
    }, [matchedModelChannels]);

    const handleWeightChange = useCallback((id: string, weight: number) => {
        setSelectedMembers((prev) => prev.map((m) => m.id === id ? { ...m, weight } : m));
    }, []);

    const handleRemoveMember = useCallback((id: string) => {
        setRemovingIds((prev) => new Set(prev).add(id));
        setTimeout(() => {
            setSelectedMembers((prev) => prev.filter((m) => m.id !== id));
            setRemovingIds((prev) => {
                const n = new Set(prev);
                n.delete(id);
                return n;
            });
        }, 200);
    }, []);

    const handleClearMembers = useCallback(() => {
        setSelectedMembers([]);
        setRemovingIds(new Set());
    }, []);

    const handleAddGroup = useCallback((group: Group) => {
        if (!group.id) return;
        const groupId = group.id;
        const key = `group-${groupId}`;
        setSelectedMembers((prev) => {
            if (prev.some((m) => m.id === key)) return prev;
            return [...prev, {
                type: 'group' as const,
                id: key,
                target_group_id: groupId,
                target_group_name: group.name,
                weight: 1,
            }];
        });
    }, []);

    const handleFirstTokenTimeOutChange = useCallback((raw: string) => {
        setFirstTokenTimeOut(parsePositiveIntegerInput(raw));
    }, []);

    const handleSessionKeepTimeChange = useCallback((raw: string) => {
        setSessionKeepTime(parsePositiveIntegerInput(raw));
    }, []);

    const isValid = groupKey.length > 0 && selectedMembers.length > 0 && !regexError;

    const values = useMemo<GroupEditorValues>(() => ({
        name: groupName,
        match_regex: regexKey,
        mode,
        first_token_time_out: firstTokenTimeOut,
        session_keep_time: sessionKeepTime,
        members: selectedMembers,
    }), [groupName, regexKey, mode, firstTokenTimeOut, sessionKeepTime, selectedMembers]);

    return {
        modelChannels,
        allGroups,
        currentGroupId,
        groupName,
        setGroupName,
        matchRegex,
        setMatchRegex,
        mode,
        setMode,
        firstTokenTimeOut,
        sessionKeepTime,
        selectedMembers,
        setSelectedMembers,
        removingIds,
        regexError,
        autoAddDisabled,
        isValid,
        values,
        handleAddMember,
        handleAutoAdd,
        handleWeightChange,
        handleRemoveMember,
        handleClearMembers,
        handleAddGroup,
        handleFirstTokenTimeOutChange,
        handleSessionKeepTimeChange,
    };
}
