export interface UsageCacheTokens {
    cachedReadTokens: number;
    cachedWriteTokens: number;
    sourcePath: string;
}

type JsonRecord = Record<string, unknown>;

interface TokenCandidate {
    path: string[];
    sourcePath: string;
}

const READ_TOKEN_CANDIDATES: TokenCandidate[] = [
    {
        path: ['usage', 'prompt_tokens_details', 'cached_tokens'],
        sourcePath: 'usage.prompt_tokens_details.cached_tokens',
    },
    {
        path: ['usage', 'promptTokensDetails', 'cachedTokens'],
        sourcePath: 'usage.promptTokensDetails.cachedTokens',
    },
    {
        path: ['usage', 'input_tokens_details', 'cached_tokens'],
        sourcePath: 'usage.input_tokens_details.cached_tokens',
    },
    {
        path: ['usage', 'inputTokensDetails', 'cachedTokens'],
        sourcePath: 'usage.inputTokensDetails.cachedTokens',
    },
    { path: ['usage', 'cache_read_input_tokens'], sourcePath: 'usage.cache_read_input_tokens' },
    { path: ['usage', 'cacheReadInputTokens'], sourcePath: 'usage.cacheReadInputTokens' },
    { path: ['usageMetadata', 'cachedContentTokenCount'], sourcePath: 'usageMetadata.cachedContentTokenCount' },
];

const WRITE_TOKEN_CANDIDATES: TokenCandidate[] = [
    {
        path: ['usage', 'prompt_tokens_details', 'write_cached_tokens'],
        sourcePath: 'usage.prompt_tokens_details.write_cached_tokens',
    },
    {
        path: ['usage', 'promptTokensDetails', 'writeCachedTokens'],
        sourcePath: 'usage.promptTokensDetails.writeCachedTokens',
    },
    {
        path: ['usage', 'input_tokens_details', 'write_cached_tokens'],
        sourcePath: 'usage.input_tokens_details.write_cached_tokens',
    },
    {
        path: ['usage', 'inputTokensDetails', 'writeCachedTokens'],
        sourcePath: 'usage.inputTokensDetails.writeCachedTokens',
    },
    { path: ['usage', 'cache_creation_input_tokens'], sourcePath: 'usage.cache_creation_input_tokens' },
    { path: ['usage', 'cacheCreationInputTokens'], sourcePath: 'usage.cacheCreationInputTokens' },
];

export function parseUsageCacheTokens(responseContent?: string): UsageCacheTokens | null {
    if (!responseContent?.trim()) return null;

    let payload: unknown;
    try {
        payload = JSON.parse(responseContent);
    } catch {
        return null;
    }

    const read = findTokenValue(payload, READ_TOKEN_CANDIDATES);
    const write = findTokenValue(payload, WRITE_TOKEN_CANDIDATES);
    const cachedReadTokens = read?.value ?? 0;
    const cachedWriteTokens = write?.value ?? 0;

    if (cachedReadTokens <= 0 && cachedWriteTokens <= 0) return null;

    return {
        cachedReadTokens,
        cachedWriteTokens,
        sourcePath: [read?.sourcePath, write?.sourcePath].filter(Boolean).join(' / '),
    };
}

function findTokenValue(payload: unknown, candidates: TokenCandidate[]): { value: number; sourcePath: string } | null {
    for (const candidate of candidates) {
        const value = numberAt(payload, candidate.path);
        if (value !== null) return { value, sourcePath: candidate.sourcePath };
    }
    return null;
}

function numberAt(payload: unknown, path: string[]): number | null {
    let current = payload;
    for (const key of path) {
        if (!isRecord(current)) return null;
        current = current[key];
    }
    if (typeof current !== 'number' || !Number.isFinite(current) || current < 0) return null;
    return Math.trunc(current);
}

function isRecord(value: unknown): value is JsonRecord {
    return typeof value === 'object' && value !== null && !Array.isArray(value);
}
