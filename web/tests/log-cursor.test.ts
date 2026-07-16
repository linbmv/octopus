import { describe, expect, it } from 'vitest';
import {
    canLoadMoreRelayLogs,
    compareDecimalIDs,
    relayLogInfiniteDataSize,
    trimRelayLogInfiniteData,
    type RelayLog,
} from '@/api/endpoints/log';

describe('relay log cursor IDs', () => {
    it('compares Snowflake IDs without JavaScript number precision loss', () => {
        expect(compareDecimalIDs('9007199254740993', '9007199254740992')).toBeGreaterThan(0);
        expect(compareDecimalIDs('9223372036854775807', '999999999999999999')).toBeGreaterThan(0);
        expect(compareDecimalIDs('00042', '42')).toBe(0);
    });
});

const relayLog = (id: string): RelayLog => ({
    id,
    time: Number(id),
    request_model_name: 'm',
    request_api_key_name: 'k',
    channel: 1,
    channel_name: 'c',
    actual_model_name: 'm',
    input_tokens: 0,
    output_tokens: 0,
    ftut: 0,
    use_time: 0,
    cost: 0,
    request_content: '',
    response_content: '',
    error: '',
    attempts: [],
    total_attempts: 0,
});

describe('relay log InfiniteData bound', () => {
    it('deduplicates across every page and caps the actual store', () => {
        const data = {
            pages: [
                { items: [relayLog('5'), relayLog('4'), relayLog('3')], next_cursor: '3', has_more: true },
                { items: [relayLog('3'), relayLog('2')], next_cursor: '2', has_more: true },
                { items: [relayLog('1')], next_cursor: '1', has_more: true },
            ],
            pageParams: ['0', '3', '2'],
        };
        const trimmed = trimRelayLogInfiniteData(data, 4)!;

        expect(relayLogInfiniteDataSize(trimmed)).toBe(4);
        expect(trimmed.pages.flatMap((page) => page.items.map((log) => log.id))).toEqual(['5', '4', '3', '2']);
        expect(trimmed.pages.at(-1)).toMatchObject({ has_more: false, next_cursor: undefined });
        expect(trimmed.pageParams).toHaveLength(trimmed.pages.length);
        expect(canLoadMoreRelayLogs(trimmed, true, 4)).toBe(false);
    });

    it('keeps pagination open below the cap', () => {
        const data = {
            pages: [{ items: [relayLog('2'), relayLog('1')], next_cursor: '1', has_more: true }],
            pageParams: ['0'],
        };
        const trimmed = trimRelayLogInfiniteData(data, 4)!;
        expect(trimmed.pages[0].has_more).toBe(true);
        expect(canLoadMoreRelayLogs(trimmed, true, 4)).toBe(true);
    });
});
