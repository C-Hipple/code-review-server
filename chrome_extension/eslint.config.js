import js from '@eslint/js';
import reactHooks from 'eslint-plugin-react-hooks';
import { defineConfig, globalIgnores } from 'eslint/config';
import tseslint from 'typescript-eslint';

export default defineConfig([
    // crs_native_host is Go plus a shell script; nothing for ESLint there.
    globalIgnores(['dist', 'node_modules', 'crs_native_host']),
    {
        files: ['**/*.{ts,tsx}'],
        extends: [js.configs.recommended, tseslint.configs.recommended],
        rules: {
            '@typescript-eslint/no-unused-vars': [
                'error',
                { argsIgnorePattern: '^_', varsIgnorePattern: '^_' },
            ],
        },
    },
    {
        files: ['src/panel/**/*.{ts,tsx}'],
        extends: [reactHooks.configs.flat.recommended],
    },
]);
