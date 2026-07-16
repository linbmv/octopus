import { configDefaults, defineConfig } from 'vitest/config';
import { fileURLToPath, URL } from 'node:url';

export default defineConfig({
    resolve: {
        alias: {
            '@': fileURLToPath(new URL('./src', import.meta.url)),
        },
    },
    test: {
        // Playwright owns the browser suites. Keeping the runners disjoint
        // prevents Vitest from importing Playwright's global test registry.
        exclude: [...configDefaults.exclude, 'e2e/**'],
    },
});
