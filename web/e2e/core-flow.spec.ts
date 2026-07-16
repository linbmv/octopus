import { expect, test, type APIResponse, type Page } from '@playwright/test';

const bootstrapPassword = 'Bootstrap-Only-2026!';
const changedPassword = 'Changed-Once-2026!';
const upstreamKey = 'e2e-upstream-key';
const upstreamModel = 'upstream-e2e-model';
const channelName = 'e2e-browser-channel';
const groupName = 'e2e-browser-group';
const clientKeyName = 'e2e-browser-client';
const upstreamPort = Number(process.env.OCTOPUS_E2E_UPSTREAM_PORT || 18183);

async function expectSuccessful(response: Pick<APIResponse, 'status' | 'text'>) {
    expect(response.status(), await response.text()).toBe(200);
}

async function signIn(page: Page, password: string) {
    await page.getByLabel('Username', { exact: true }).fill('admin');
    await page.getByLabel('Password', { exact: true }).fill(password);
    const responsePromise = page.waitForResponse((response) =>
        response.url().endsWith('/api/v1/user/login') && response.request().method() === 'POST'
    );
    await page.getByRole('button', { name: 'Login', exact: true }).click();
    const response = await responsePromise;
    await expectSuccessful(response);
    return response;
}

test('first boot, configuration, relay, and log flow', async ({ page, request }) => {
    const cdp = await page.context().newCDPSession(page);
    await cdp.send('WebAuthn.enable');
    await cdp.send('WebAuthn.addVirtualAuthenticator', {
        options: {
            protocol: 'ctap2',
            transport: 'internal',
            hasResidentKey: true,
            hasUserVerification: true,
            isUserVerified: true,
        },
    });
    await page.addInitScript(() => {
        window.localStorage.setItem('octopus-settings', JSON.stringify({
            state: { locale: 'en' },
            version: 0,
        }));
    });

    await test.step('force the first administrator password change and sign in again', async () => {
        await page.goto('/');
        await expect(page.getByRole('button', { name: 'Login', exact: true })).toBeVisible();
        await signIn(page, bootstrapPassword);

        await expect(page.getByRole('heading', { name: 'Change your temporary password' })).toBeVisible();
        await expect(page.getByRole('navigation', { name: 'Main Navigation' })).toHaveCount(0);

        await page.getByLabel('Temporary password', { exact: true }).fill(bootstrapPassword);
        await page.getByLabel('New password', { exact: true }).fill(changedPassword);
        await page.getByLabel('Confirm new password', { exact: true }).fill(changedPassword);
        await page.getByRole('button', { name: 'Change password', exact: true }).click();

        await expect(page.getByRole('button', { name: 'Login', exact: true })).toBeVisible();
        const loginResponse = await signIn(page, changedPassword);
        const loginPayload = await loginResponse.json() as {
            data?: { token?: string; auth_mode?: string };
        };
        expect(loginPayload.data?.auth_mode).toBe('cookie');
        expect(loginPayload.data?.token).toBeUndefined();

        const authCookies = await page.context().cookies();
        const sessionCookie = authCookies.find((cookie) => cookie.name === 'octopus_admin_session');
        const csrfCookie = authCookies.find((cookie) => cookie.name === 'octopus_csrf');
        expect(sessionCookie).toMatchObject({ httpOnly: true, sameSite: 'Strict', path: '/' });
        expect(csrfCookie).toMatchObject({ httpOnly: false, sameSite: 'Strict', path: '/' });
        const persistedAuth = await page.evaluate(() => window.sessionStorage.getItem('auth-storage'));
        expect(persistedAuth || '').not.toContain('eyJ');
        expect(JSON.parse(persistedAuth || '{}')?.state?.token ?? null).toBeNull();

        const missingCSRFStatus = await page.evaluate(async () => {
            const response = await fetch('/api/v1/user/change-username', {
                method: 'POST',
                credentials: 'same-origin',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ new_username: 'must-not-change', current_password: 'unused' }),
            });
            return response.status;
        });
        expect(missingCSRFStatus).toBe(403);
        const navigation = page.getByRole('navigation', { name: 'Main Navigation' });
        await expect(navigation.getByRole('button', { name: 'Home', exact: true })).toBeVisible();

        await page.reload();
        await expect(navigation.getByRole('button', { name: 'Home', exact: true })).toBeVisible();
    });

    const navigation = page.getByRole('navigation', { name: 'Main Navigation' });

    await test.step('register WebAuthn and require it after the password factor', async () => {
        await navigation.getByRole('button', { name: 'Setting', exact: true }).click();
        await expect(page.getByText('Security keys and passkeys', { exact: true })).toBeVisible();
        await page.getByPlaceholder('Credential name, for example Work laptop').fill('E2E virtual authenticator');
        await page.getByPlaceholder('Enter current password to confirm').fill(changedPassword);
        const registrationPromise = page.waitForResponse((response) =>
            response.url().endsWith('/api/v1/user/webauthn/register/finish') && response.request().method() === 'POST'
        );
        await page.getByRole('button', { name: 'Register credential', exact: true }).click();
        await expectSuccessful(await registrationPromise);
        await expect(page.getByText('E2E virtual authenticator', { exact: true })).toBeVisible();

        await page.getByRole('button', { name: 'Logout', exact: true }).click();
        await expect(page.getByRole('button', { name: 'Login', exact: true })).toBeVisible();
        const finishPromise = page.waitForResponse((response) =>
            response.url().endsWith('/api/v1/user/login/webauthn/finish') && response.request().method() === 'POST'
        );
        const passwordResponse = await signIn(page, changedPassword);
        const passwordPayload = await passwordResponse.json() as { data?: { webauthn_required?: boolean; expire_at?: string } };
        expect(passwordPayload.data?.webauthn_required).toBe(true);
        expect(passwordPayload.data?.expire_at).toBeUndefined();
        await expectSuccessful(await finishPromise);
        await expect(page.getByRole('navigation', { name: 'Main Navigation' })).toBeVisible();
    });

    await test.step('create a real OpenAI channel through the browser', async () => {
        await navigation.getByRole('button', { name: 'Channel', exact: true }).click();
        await expect(page.locator('main > header').getByText('Channel', { exact: true })).toBeVisible();
        await page.getByRole('button', { name: 'Create', exact: true }).click();

        const dialog = page.getByRole('dialog');
        await expect(dialog.getByRole('heading', { name: 'Create New Channel' })).toBeVisible();
        await dialog.locator('#new-channel-name').fill(channelName);
        await dialog.locator('#new-channel-base-0').fill(`http://127.0.0.1:${upstreamPort}`);
        await dialog.getByPlaceholder('API Key', { exact: true }).fill(upstreamKey);
        await dialog.locator('#new-channel-model-custom').fill(upstreamModel);
        await dialog.locator('#new-channel-model-custom').press('Enter');

        const createResponsePromise = page.waitForResponse((response) =>
            response.url().endsWith('/api/v1/channel/create') && response.request().method() === 'POST'
        );
        await dialog.getByRole('button', { name: 'Create Channel', exact: true }).click();
        await expectSuccessful(await createResponsePromise);
        await expect(dialog).toHaveCount(0);
        await expect(page.getByText(channelName, { exact: true })).toBeVisible();
    });

    await test.step('create a group and bind the channel model through the browser', async () => {
        await navigation.getByRole('button', { name: 'Group', exact: true }).click();
        await expect(page.locator('main > header').getByText('Group', { exact: true })).toBeVisible();
        await page.getByRole('button', { name: 'Create', exact: true }).click();

        const dialog = page.getByRole('dialog');
        await expect(dialog.getByRole('heading', { name: 'Create Group' })).toBeVisible();
        await dialog.getByLabel('Group Name', { exact: true }).fill(groupName);
        await dialog.getByRole('button', { name: new RegExp(channelName) }).click();
        await dialog.getByRole('button', { name: new RegExp(upstreamModel) }).click();

        const createResponsePromise = page.waitForResponse((response) =>
            response.url().endsWith('/api/v1/group/create') && response.request().method() === 'POST'
        );
        await dialog.getByRole('button', { name: 'Create', exact: true }).click();
        await expectSuccessful(await createResponsePromise);
        await expect(dialog).toHaveCount(0);
        await expect(page.getByText(groupName, { exact: true })).toBeVisible();
    });

    let clientAPIKey = '';
    await test.step('create a group-scoped client key through the browser', async () => {
        await navigation.getByRole('button', { name: 'Setting', exact: true }).click();
        await expect(page.locator('main > header').getByText('Setting', { exact: true })).toBeVisible();
        await expect(page.getByRole('heading', { name: 'API Keys', exact: true })).toBeVisible();
        await page.getByTitle('Add Key').click();

        const apiKeyForm = page.locator('form').filter({
            has: page.getByText('Supported Models', { exact: true }),
        });
        await expect(apiKeyForm).toBeVisible();
        await apiKeyForm.getByLabel('Name', { exact: true }).fill(clientKeyName);
        await apiKeyForm.getByRole('button', { name: groupName, exact: true }).click();

        const createResponsePromise = page.waitForResponse((response) =>
            response.url().endsWith('/api/v1/apikey/create') && response.request().method() === 'POST'
        );
        await apiKeyForm.getByRole('button', { name: 'Create', exact: true }).click();
        const createResponse = await createResponsePromise;
        await expectSuccessful(createResponse);
        expect(await createResponse.request().headerValue('authorization')).toBeNull();
        expect(await createResponse.request().headerValue('x-octopus-csrf')).toBeTruthy();
        const payload = await createResponse.json() as { data?: { api_key?: string } };
        clientAPIKey = payload.data?.api_key || '';
        expect(clientAPIKey).toMatch(/^sk-octopus-/);
        await expect(page.getByText(clientKeyName, { exact: true })).toBeVisible();
    });

    await test.step('list the mapped model and relay a real chat completion', async () => {
        const headers = { Authorization: `Bearer ${clientAPIKey}` };
        const modelsResponse = await request.get('/v1/models', { headers });
        await expectSuccessful(modelsResponse);
        const models = await modelsResponse.json() as { data?: Array<{ id?: string }> };
        expect(models.data?.map((model) => model.id)).toContain(groupName);

        const relayResponse = await request.post('/v1/chat/completions', {
            headers,
            data: {
                model: groupName,
                messages: [{ role: 'user', content: 'deterministic browser e2e' }],
            },
        });
        await expectSuccessful(relayResponse);
        const completion = await relayResponse.json() as {
            choices?: Array<{ message?: { content?: string } }>;
            usage?: { prompt_tokens?: number; completion_tokens?: number };
        };
        expect(completion.choices?.[0]?.message?.content).toBe(`relay received ${upstreamModel}`);
        expect(completion.usage).toMatchObject({ prompt_tokens: 5, completion_tokens: 3 });
    });

    await test.step('show the successful mapped relay in the browser log view', async () => {
        await navigation.getByRole('button', { name: 'Log', exact: true }).click();
        await expect(page.locator('main > header').getByText('Log', { exact: true })).toBeVisible();

        const logCard = page.getByRole('button').filter({
            has: page.getByText(groupName, { exact: true }),
        }).filter({
            has: page.getByText(channelName, { exact: true }),
        }).first();
        await expect(logCard).toBeVisible();
        await expect(logCard).toContainText(upstreamModel);
        await expect(logCard).toContainText(clientKeyName);
        await expect(logCard).toContainText('Input 5');
        await expect(logCard).toContainText('Output 3');
        await logCard.click();

        const dialog = page.getByRole('dialog');
        await expect(dialog).toContainText('(No request content)');
        await expect(dialog).toContainText('(No response content)');
    });

    await test.step('logout revokes the browser session and clears both cookies', async () => {
        const logoutStatus = await page.evaluate(async () => {
            const csrf = document.cookie
                .split(';')
                .map((part) => part.trim())
                .find((part) => part.startsWith('octopus_csrf='))
                ?.slice('octopus_csrf='.length);
            const response = await fetch('/api/v1/user/logout', {
                method: 'POST',
                credentials: 'same-origin',
                headers: {
                    'Content-Type': 'application/json',
                    'X-Octopus-CSRF': decodeURIComponent(csrf || ''),
                },
                body: '{}',
            });
            return response.status;
        });
        expect(logoutStatus).toBe(200);
        const remainingCookies = await page.context().cookies();
        expect(remainingCookies.some((cookie) => cookie.name === 'octopus_admin_session')).toBe(false);
        expect(remainingCookies.some((cookie) => cookie.name === 'octopus_csrf')).toBe(false);
        const statusAfterLogout = await page.evaluate(async () => {
            const response = await fetch('/api/v1/user/status', { credentials: 'same-origin' });
            return response.status;
        });
        expect(statusAfterLogout).toBe(401);
        await page.reload();
        await expect(page.getByRole('button', { name: 'Login', exact: true })).toBeVisible();
    });
});
