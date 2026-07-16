import { readFile } from 'node:fs/promises';
import { describe, expect, it } from 'vitest';

describe('Playwright environment', () => {
    it('selects the exact quality or packaged release frontend', async () => {
        const [source, releaseWorkflow] = await Promise.all([
            readFile(new URL('../e2e/start-environment.mjs', import.meta.url), 'utf8'),
            readFile(new URL('../../.github/workflows/release.yaml', import.meta.url), 'utf8'),
        ]);

        expect(source).toContain("const usePackagedStatic = process.env.OCTOPUS_E2E_USE_PACKAGED_STATIC === '1';");
        expect(source).toContain("? join(projectRoot, 'static', 'out')");
        expect(source).toContain(": join(webRoot, 'out');");
        expect(source).toContain('Packaged static E2E requires OCTOPUS_E2E_SKIP_BUILD=1');
        expect(source).toContain("await stat(join(staticOutputRoot, 'index.html'));");
        expect(source).toContain('createStaticServer(staticOutputRoot)');
        expect(releaseWorkflow).toContain("OCTOPUS_E2E_USE_PACKAGED_STATIC: '1'");
    });
});
