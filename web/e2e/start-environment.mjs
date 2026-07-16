import { spawn } from 'node:child_process';
import { createReadStream, rmSync } from 'node:fs';
import { mkdir, rm, stat, writeFile } from 'node:fs/promises';
import { createServer, request as createRequest } from 'node:http';
import { tmpdir } from 'node:os';
import { dirname, extname, join, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptDir = dirname(fileURLToPath(import.meta.url));
const webRoot = resolve(scriptDir, '..');
const projectRoot = resolve(webRoot, '..');
const usePackagedStatic = process.env.OCTOPUS_E2E_USE_PACKAGED_STATIC === '1';
const staticOutputRoot = usePackagedStatic
    ? join(projectRoot, 'static', 'out')
    : join(webRoot, 'out');

if (usePackagedStatic && process.env.OCTOPUS_E2E_SKIP_BUILD !== '1') {
    throw new Error('Packaged static E2E requires OCTOPUS_E2E_SKIP_BUILD=1');
}

const ports = {
    public: Number(process.env.OCTOPUS_E2E_PUBLIC_PORT || 18180),
    backend: Number(process.env.OCTOPUS_E2E_BACKEND_PORT || 18181),
    frontend: Number(process.env.OCTOPUS_E2E_FRONTEND_PORT || 18182),
    upstream: Number(process.env.OCTOPUS_E2E_UPSTREAM_PORT || 18183),
};

for (const [name, port] of Object.entries(ports)) {
    if (!Number.isInteger(port) || port < 1 || port > 65535) {
        throw new Error(`Invalid ${name} E2E port`);
    }
}

const bootstrapPassword = 'Bootstrap-Only-2026!';
const upstreamKey = 'e2e-upstream-key';
const upstreamModel = 'upstream-e2e-model';
const tempRoot = join(tmpdir(), `octopus-playwright-runtime-${ports.public}`);
await rm(tempRoot, { recursive: true, force: true });
await mkdir(tempRoot, { recursive: true, mode: 0o700 });
const binaryPath = join(tempRoot, 'octopus-e2e');
const configPath = join(tempRoot, 'config.json');
const databasePath = join(tempRoot, 'octopus-e2e.db');
const credentialPath = join(tempRoot, 'initial-admin-password');

let backendProcess;
let upstreamServer;
let staticServer;
let proxyServer;
let stopping = false;

function waitForExit(child, timeoutMs) {
    if (!child || child.exitCode !== null || child.signalCode !== null) return Promise.resolve();
    return new Promise((resolvePromise) => {
        const timer = setTimeout(resolvePromise, timeoutMs);
        child.once('exit', () => {
            clearTimeout(timer);
            resolvePromise();
        });
    });
}

async function stopChild(child) {
    if (!child || child.exitCode !== null || child.signalCode !== null) return;
    child.kill('SIGTERM');
    await waitForExit(child, 5_000);
    if (child.exitCode === null && child.signalCode === null) {
        child.kill('SIGKILL');
        await waitForExit(child, 2_000);
    }
}

function closeServer(server) {
    if (!server?.listening) return Promise.resolve();
    return new Promise((resolvePromise) => {
        server.close(resolvePromise);
        server.closeAllConnections?.();
    });
}

async function shutdown(exitCode = 0) {
    if (stopping) return;
    stopping = true;
    await closeServer(proxyServer);
    await closeServer(staticServer);
    await stopChild(backendProcess);
    await closeServer(upstreamServer);
    await rm(tempRoot, { recursive: true, force: true });
    process.exit(exitCode);
}

process.once('SIGINT', () => void shutdown(0));
process.once('SIGTERM', () => void shutdown(0));
process.once('exit', () => {
    // Playwright may enforce its own process shutdown deadline. A synchronous
    // finalizer keeps the isolated binary/database/credentials from surviving
    // even if the asynchronous signal cleanup is interrupted.
    rmSync(tempRoot, { recursive: true, force: true });
});

function spawnProcess(command, args, options = {}) {
    return spawn(command, args, {
        cwd: projectRoot,
        env: process.env,
        stdio: ['ignore', 'inherit', 'inherit'],
        ...options,
    });
}

async function runCommand(command, args, options = {}) {
    const child = spawnProcess(command, args, options);
    const code = await new Promise((resolvePromise, reject) => {
        child.once('error', reject);
        child.once('exit', (exitCode, signal) => {
            if (signal) reject(new Error(`${command} terminated by ${signal}`));
            else resolvePromise(exitCode);
        });
    });
    if (code !== 0) throw new Error(`${command} exited with code ${code}`);
}

async function waitForHTTP(url, child, timeoutMs = 90_000) {
    const deadline = Date.now() + timeoutMs;
    let lastError;
    while (Date.now() < deadline) {
        if (child && (child.exitCode !== null || child.signalCode !== null)) {
            throw new Error(`Service exited before becoming ready: ${url}`);
        }
        try {
            const response = await fetch(url, { signal: AbortSignal.timeout(1_000) });
            if (response.status < 500) return;
        } catch (error) {
            lastError = error;
        }
        await new Promise((resolvePromise) => setTimeout(resolvePromise, 200));
    }
    throw new Error(`Timed out waiting for ${url}: ${lastError instanceof Error ? lastError.message : 'unknown error'}`);
}

function readJSONBody(request, maxBytes = 1 << 20) {
    return new Promise((resolvePromise, reject) => {
        const chunks = [];
        let size = 0;
        request.on('data', (chunk) => {
            size += chunk.length;
            if (size > maxBytes) {
                reject(new Error('request body too large'));
                request.destroy();
                return;
            }
            chunks.push(chunk);
        });
        request.on('end', () => {
            try {
                resolvePromise(JSON.parse(Buffer.concat(chunks).toString('utf8')));
            } catch (error) {
                reject(error);
            }
        });
        request.on('error', reject);
    });
}

function sendJSON(response, status, body) {
    const encoded = JSON.stringify(body);
    response.writeHead(status, {
        'content-type': 'application/json',
        'content-length': Buffer.byteLength(encoded),
    });
    response.end(encoded);
}

function createMockUpstream() {
    return createServer(async (request, response) => {
        try {
            if (request.headers.authorization !== `Bearer ${upstreamKey}`) {
                sendJSON(response, 401, { error: { message: 'invalid fixture credential' } });
                return;
            }

            if (request.method === 'GET' && request.url === '/v1/models') {
                sendJSON(response, 200, {
                    object: 'list',
                    data: [{ id: upstreamModel, object: 'model', owned_by: 'e2e' }],
                });
                return;
            }

            if (request.method === 'POST' && request.url === '/v1/chat/completions') {
                const body = await readJSONBody(request);
                if (body?.model !== upstreamModel || !Array.isArray(body?.messages)) {
                    sendJSON(response, 400, { error: { message: 'unexpected relay request' } });
                    return;
                }
                sendJSON(response, 200, {
                    id: 'chatcmpl-octopus-e2e',
                    object: 'chat.completion',
                    created: 1_763_395_200,
                    model: upstreamModel,
                    choices: [{
                        index: 0,
                        message: {
                            role: 'assistant',
                            content: `relay received ${body.model}`,
                        },
                        finish_reason: 'stop',
                    }],
                    usage: {
                        prompt_tokens: 5,
                        completion_tokens: 3,
                        total_tokens: 8,
                    },
                });
                return;
            }

            sendJSON(response, 404, { error: { message: 'fixture route not found' } });
        } catch {
            if (!response.headersSent) sendJSON(response, 400, { error: { message: 'invalid fixture request' } });
            else response.end();
        }
    });
}

function listen(server, port) {
    return new Promise((resolvePromise, reject) => {
        server.once('error', reject);
        server.listen(port, '127.0.0.1', () => {
            server.off('error', reject);
            resolvePromise();
        });
    });
}

function proxyTo(targetPort, request, response) {
    const proxied = createRequest({
        hostname: '127.0.0.1',
        port: targetPort,
        path: request.url,
        method: request.method,
        // Preserve the browser-visible Host alongside Origin. Octopus then
        // evaluates this as the same-origin request it really is; rewriting
        // Host to the private backend port would make the CORS middleware
        // correctly reject the otherwise legitimate management POST.
        headers: request.headers,
    }, (upstreamResponse) => {
        response.writeHead(upstreamResponse.statusCode || 502, upstreamResponse.headers);
        upstreamResponse.pipe(response);
    });
    proxied.on('error', () => {
        if (!response.headersSent) response.writeHead(502);
        response.end();
    });
    request.pipe(proxied);
}

function createSameOriginProxy() {
    return createServer((request, response) => {
        const path = request.url || '/';
        const isBackend = path.startsWith('/api/')
            || path.startsWith('/v1/')
            || path.startsWith('/v1beta/')
            || path === '/metrics';
        proxyTo(isBackend ? ports.backend : ports.frontend, request, response);
    });
}

const contentTypes = new Map([
    ['.css', 'text/css; charset=utf-8'],
    ['.html', 'text/html; charset=utf-8'],
    ['.ico', 'image/x-icon'],
    ['.js', 'text/javascript; charset=utf-8'],
    ['.json', 'application/json; charset=utf-8'],
    ['.png', 'image/png'],
    ['.svg', 'image/svg+xml'],
    ['.webmanifest', 'application/manifest+json'],
    ['.woff2', 'font/woff2'],
]);

function createStaticServer(root) {
    const normalizedRoot = resolve(root);
    return createServer(async (request, response) => {
        try {
            const pathname = decodeURIComponent(new URL(request.url || '/', 'http://localhost').pathname);
            const relativePath = pathname === '/' ? 'index.html' : pathname.replace(/^\/+/, '');
            let filePath = resolve(normalizedRoot, relativePath);
            if (filePath !== normalizedRoot && !filePath.startsWith(`${normalizedRoot}${sep}`)) {
                response.writeHead(400);
                response.end();
                return;
            }
            let info = await stat(filePath);
            if (info.isDirectory()) {
                filePath = join(filePath, 'index.html');
                info = await stat(filePath);
            }
            if (!info.isFile()) throw new Error('not a file');
            response.writeHead(200, {
                'content-length': info.size,
                'content-type': contentTypes.get(extname(filePath)) || 'application/octet-stream',
            });
            createReadStream(filePath).pipe(response);
        } catch {
            response.writeHead(404);
            response.end();
        }
    });
}

try {
    await writeFile(configPath, JSON.stringify({
        server: { host: '127.0.0.1', port: ports.backend },
        database: { type: 'sqlite', path: databasePath },
        log: { level: 'warn', format: 'console' },
        observability: {
            metrics: { enabled: false },
            tracing: { enabled: false },
        },
        webauthn: {
            enabled: true,
            rp_id: 'localhost',
            rp_display_name: 'Octopus E2E',
            rp_origins: [`http://localhost:${ports.public}`],
        },
    }));

    await runCommand('go', ['build', '-o', binaryPath, '.'], {
        env: {
            ...process.env,
            GOCACHE: process.env.GOCACHE || join(tmpdir(), 'octopus-go-build'),
        },
    });
    if (process.env.OCTOPUS_E2E_SKIP_BUILD !== '1') {
        await runCommand(process.execPath, [
            join(webRoot, 'node_modules', 'next', 'dist', 'bin', 'next'),
            'build',
            '--webpack',
        ], {
            cwd: webRoot,
            env: {
                ...process.env,
                NEXT_PUBLIC_API_BASE_URL: '.',
                NEXT_PUBLIC_APP_VERSION: 'e2e',
                NEXT_TELEMETRY_DISABLED: '1',
            },
        });
    }

    upstreamServer = createMockUpstream();
    await listen(upstreamServer, ports.upstream);

    backendProcess = spawnProcess(binaryPath, ['start', '--config', configPath], {
        env: {
            ...process.env,
            OCTOPUS_INITIAL_ADMIN_PASSWORD: bootstrapPassword,
            OCTOPUS_INITIAL_ADMIN_PASSWORD_FILE: credentialPath,
        },
    });
    await waitForHTTP(`http://127.0.0.1:${ports.backend}/`, backendProcess);

    // Quality E2E serves the exact web/out from its preceding Next build.
    // Release E2E explicitly switches to the packaged static/out tree.
    await stat(join(staticOutputRoot, 'index.html'));
    staticServer = createStaticServer(staticOutputRoot);
    await listen(staticServer, ports.frontend);

    proxyServer = createSameOriginProxy();
    await listen(proxyServer, ports.public);
    console.log(`Octopus E2E environment ready on http://127.0.0.1:${ports.public}`);

    await new Promise(() => {});
} catch (error) {
    console.error(error instanceof Error ? error.message : String(error));
    await shutdown(1);
}
