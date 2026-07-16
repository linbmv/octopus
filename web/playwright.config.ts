import { defineConfig, devices } from '@playwright/test';

const publicPort = Number(process.env.OCTOPUS_E2E_PUBLIC_PORT || 18180);
const baseURL = `http://localhost:${publicPort}`;

export default defineConfig({
    testDir: './e2e',
    globalTeardown: './e2e/global-teardown.ts',
    fullyParallel: false,
    workers: 1,
    timeout: 90_000,
    expect: {
        timeout: 15_000,
    },
    reporter: [['list']],
    use: {
        baseURL,
        locale: 'en-US',
        screenshot: 'only-on-failure',
        trace: 'retain-on-failure',
        video: 'retain-on-failure',
    },
    projects: [
        {
            name: 'chromium',
            use: { ...devices['Desktop Chrome'] },
        },
    ],
    webServer: {
        command: 'node e2e/start-environment.mjs',
        url: baseURL,
        reuseExistingServer: false,
        timeout: 300_000,
        stdout: 'pipe',
        stderr: 'pipe',
    },
});
