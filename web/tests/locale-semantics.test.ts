import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';

import en from '../public/locale/en.json';
import zhHans from '../public/locale/zh_hans.json';
import zhHant from '../public/locale/zh_hant.json';

function stringValues(value: unknown): string[] {
    if (typeof value === 'string') return [value];
    if (!value || typeof value !== 'object') return [];
    return Object.values(value).flatMap(stringValues);
}

function sourceFiles(directory: string): string[] {
    return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
        const path = join(directory, entry.name);
        return entry.isDirectory() ? sourceFiles(path) : path.endsWith('.tsx') ? [path] : [];
    });
}

describe('locale semantics', () => {
    it('keeps the English catalog free of untranslated Han text', () => {
        expect(stringValues(en).filter((value) => /\p{Script=Han}/u.test(value))).toEqual([]);
    });

    it('keeps critical error and relay-attempt copy explicitly reviewed', () => {
        expect({
            en: { appError: en.appError, attemptStatus: en.log.card.attemptStatus },
            zhHans: { appError: zhHans.appError, attemptStatus: zhHans.log.card.attemptStatus },
            zhHant: { appError: zhHant.appError, attemptStatus: zhHant.log.card.attemptStatus },
        }).toEqual({
            en: {
                appError: {
                    pageTitle: 'Page failed to load',
                    rootTitle: 'Application failed to load',
                    description: 'Try again, or refresh the page to continue.',
                    digest: 'Error ID',
                    retry: 'Try again',
                },
                attemptStatus: {
                    success: 'Success',
                    failed: 'Failed',
                    client_canceled: 'Client disconnected',
                    circuit_break: 'Circuit open',
                    skipped: 'Skipped',
                    redirect: 'Redirected',
                },
            },
            zhHans: {
                appError: {
                    pageTitle: '页面加载失败',
                    rootTitle: '应用加载失败',
                    description: '请重试，或刷新页面后继续操作。',
                    digest: '错误编号',
                    retry: '重试',
                },
                attemptStatus: {
                    success: '成功',
                    failed: '失败',
                    client_canceled: '客户端断开',
                    circuit_break: '熔断',
                    skipped: '跳过',
                    redirect: '重定向',
                },
            },
            zhHant: {
                appError: {
                    pageTitle: '頁面載入失敗',
                    rootTitle: '應用程式載入失敗',
                    description: '請重試，或重新整理頁面後繼續操作。',
                    digest: '錯誤編號',
                    retry: '重試',
                },
                attemptStatus: {
                    success: '成功',
                    failed: '失敗',
                    client_canceled: '客戶端中斷連線',
                    circuit_break: '熔斷',
                    skipped: '略過',
                    redirect: '重新導向',
                },
            },
        });
    });

    it('keeps reasoning token terminology available in every locale', () => {
        expect({
            en: [en.log.card.reasoning, en.channel.detail.metrics.reasoningToken, en.setting.apiKey.stats.reasoningToken],
            zhHans: [zhHans.log.card.reasoning, zhHans.channel.detail.metrics.reasoningToken, zhHans.setting.apiKey.stats.reasoningToken],
            zhHant: [zhHant.log.card.reasoning, zhHant.channel.detail.metrics.reasoningToken, zhHant.setting.apiKey.stats.reasoningToken],
        }).toEqual({
            en: ['Reasoning', 'Reasoning Tokens', 'Reasoning Token'],
            zhHans: ['推理', '推理 Token', '推理 Token'],
            zhHant: ['推理', '推理 Token', '推理 Token'],
        });
    });

    it('does not embed user-visible Han text directly in JSX children', () => {
        const offenders = sourceFiles(join(process.cwd(), 'src'))
            .flatMap((path) => readFileSync(path, 'utf8').split('\n').map((line, index) => ({ path, line, number: index + 1 })))
            .filter(({ line }) => />[^<{]*\p{Script=Han}[^<{]*</u.test(line))
            .map(({ path, number }) => `${path}:${number}`);

        expect(offenders).toEqual([]);
    });
});
