import type { HeaderRule, JSONRewriteRule } from '@/api/endpoints/channel';

export type HeaderRuleAction = 'set' | 'append' | 'remove';
export type JSONRewriteAction = 'override' | 'remove';

export function isProtectedAuthenticationHeader(name: string): boolean {
    const normalized = name.trim().toLowerCase();
    if (!normalized) return false;
    const exact = new Set([
        'authorization',
        'proxy-authorization',
        'authentication',
        'x-api-key',
        'x-goog-api-key',
        'api-key',
        'apikey',
		'token',
        'x-auth-token',
        'x-access-token',
        'access-token',
        'x-amz-security-token',
        'cookie',
        'set-cookie',
    ]);
    return exact.has(normalized)
        || normalized.includes('authorization')
        || normalized.includes('authentication')
        || normalized.endsWith('-api-key')
		|| normalized.endsWith('-token')
		|| normalized.endsWith('-credential');
}

export function normalizeHeaderRules(rules: HeaderRule[]): HeaderRule[] {
    return rules
        .map((rule) => {
            const action = rule.action.trim().toLowerCase() as HeaderRuleAction;
            return {
                action,
                header_key: rule.header_key.trim(),
                header_value: action === 'remove' ? '' : (rule.header_value ?? ''),
            };
        })
        .filter((rule) => rule.header_key !== '');
}

export function normalizeJSONRewriteRules(rules: JSONRewriteRule[]): JSONRewriteRule[] {
    return rules
        .map((rule) => {
            const action = rule.action.trim().toLowerCase() as JSONRewriteAction;
            return {
                action,
                path: rule.path.trim(),
                value: action === 'remove' ? null : (rule.value ?? '').trim(),
            };
        })
        .filter((rule) => rule.path !== '');
}
