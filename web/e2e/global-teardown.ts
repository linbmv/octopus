import { rm } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';

export default async function globalTeardown() {
    const publicPort = Number(process.env.OCTOPUS_E2E_PUBLIC_PORT || 18180);
    if (!Number.isInteger(publicPort) || publicPort < 1 || publicPort > 65535) return;
    await rm(join(tmpdir(), `octopus-playwright-runtime-${publicPort}`), {
        recursive: true,
        force: true,
    });
}
