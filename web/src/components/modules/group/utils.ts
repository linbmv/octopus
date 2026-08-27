export function normalizeKey(value: string) {
    return value.trim().toLowerCase();
}

export function memberKey(member: { type?: 'channel_model' | 'group'; channel_model_id?: number; target_group_id?: number }) {
    return member.type === 'group'
        ? `group:${member.target_group_id ?? 0}`
        : `model:${member.channel_model_id ?? 0}`;
}

export function matchesGroupName(modelName: string, groupKey: string) {
    if (!groupKey) return false;
    return modelName.toLowerCase().includes(groupKey);
}

// Keep the picker aligned with the server's graph validation. A proposed edge
// is accepted only when the complete graph remains acyclic and no path has
// more than maxDepth nested edges.
export function canAddNestedGroup(
    groups: Array<{ id?: number; items?: Array<{ type?: 'channel_model' | 'group'; target_group_id?: number }> }>,
    ownerId: number | undefined,
    targetId: number,
    maxDepth = 3,
) {
    if (targetId <= 0 || maxDepth < 0 || ownerId === targetId) return false;

    const virtualOwnerId = ownerId ?? Number.MIN_SAFE_INTEGER;
    const edges = new Map<number, number[]>();
    for (const group of groups) {
        if (typeof group.id !== 'number') continue;
        edges.set(group.id, (group.items ?? [])
            .filter((item) => item.type === 'group' && typeof item.target_group_id === 'number')
            .map((item) => item.target_group_id!));
    }
    if (!edges.has(targetId)) return false;
    if (!edges.has(virtualOwnerId)) edges.set(virtualOwnerId, []);
    edges.get(virtualOwnerId)!.push(targetId);

    const visit = (id: number, depth: number, path: Set<number>): boolean => {
        if (depth > maxDepth || path.has(id)) return false;
        const nextPath = new Set(path);
        nextPath.add(id);
        for (const child of edges.get(id) ?? []) {
            if (!visit(child, depth + 1, nextPath)) return false;
        }
        return true;
    };

    for (const id of edges.keys()) {
        if (!visit(id, 0, new Set())) return false;
    }
    return true;
}
