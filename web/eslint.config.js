import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import tseslint from 'typescript-eslint'
import { defineConfig, globalIgnores } from 'eslint/config'

export default defineConfig([
  globalIgnores(['dist']),
  {
    files: ['**/*.{ts,tsx}'],
    extends: [
      js.configs.recommended,
      tseslint.configs.recommended,
      reactHooks.configs.flat.recommended,
      reactRefresh.configs.vite,
    ],
    languageOptions: {
      globals: globals.browser,
    },
    rules: {
      // allowConstantExport lets a component module also export plain
      // constants (e.g. main.tsx's route consts) without breaking Fast
      // Refresh — only function/class component exports must be isolated.
      'react-refresh/only-export-components': ['error', { allowConstantExport: true }],
    },
  },
  {
    // shadcn/ui primitives are vendored library code: each file intentionally
    // exports its cva variants (buttonVariants, badgeVariants, …) alongside
    // the component. This is the standard shadcn + eslint convention — turn
    // the Fast Refresh rule off for the whole ui/ folder rather than churn
    // upstream-generated files.
    files: ['src/components/ui/**'],
    rules: {
      'react-refresh/only-export-components': 'off',
    },
  },
  {
    // kit.tsx is the app's shared component kit; it deliberately re-exports a
    // couple of non-component helpers (useSave, isDuration) so the ~30
    // consumers can import everything from one '@/components/kit' barrel.
    // Splitting those out would churn every consumer for no real Fast-Refresh
    // gain, so scope the rule off for this one file.
    files: ['src/components/kit.tsx'],
    rules: {
      'react-refresh/only-export-components': 'off',
    },
  },
  {
    // main.tsx is the Vite entry module — it mounts the app and has no
    // exports of its own. Fast Refresh doesn't apply to the entry point, so
    // the rule's "move components to a separate file" guidance is moot here.
    files: ['src/main.tsx'],
    rules: {
      'react-refresh/only-export-components': 'off',
    },
  },
  {
    // TanStack Virtual's useVirtualizer() returns functions that React
    // Compiler can't memoize, so the plugin skips compiling the component and
    // emits this warning. It's a known library/plugin friction (not a real
    // bug) and out of our control — scope it off for the one file that uses
    // the virtualizer.
    files: ['src/pages/Objects.tsx'],
    rules: {
      'react-hooks/incompatible-library': 'off',
    },
  },
])
