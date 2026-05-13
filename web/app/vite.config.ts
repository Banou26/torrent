import { defineConfig } from 'vite';
import preact from '@preact/preset-vite';
import { nodePolyfills } from 'vite-plugin-node-polyfills';
import { resolve } from 'node:path';

const polyfillOpts = {
  include: ['buffer', 'events', 'stream', 'process', 'util', 'punycode'],
  globals: { Buffer: true, global: true, process: true },
};

export default defineConfig({
  plugins: [preact({ jsxImportSource: '@emotion/react' }), nodePolyfills(polyfillOpts)],
  worker: {
    format: 'es',
    // Workers need the same node polyfill set as the main thread — otherwise
    // @fkn/lib's `import { Stream } from 'stream'` etc. resolve to vite's
    // SPA fallback (HTML) and the worker silently aborts.
    plugins: () => [nodePolyfills(polyfillOpts)],
  },
  resolve: {
    alias: {
      react: 'preact/compat',
      'react-dom': 'preact/compat',
      'react/jsx-runtime': 'preact/jsx-runtime',
      // Vite's worker pipeline doesn't run vite-plugin-node-polyfills'
      // optimizeDeps step, so bare `import ... from 'stream'` etc.
      // inside @fkn/lib resolve to vite's SPA fallback (HTML) when
      // requested from the worker context. Explicit aliases force the
      // right polyfill regardless of context.
      stream: 'stream-browserify',
      events: 'events',
    },
  },
  server: {
    port: 5173,
    strictPort: true,
    fs: {
      allow: [resolve(__dirname, '..'), resolve(__dirname, '../../../fkn')],
    },
  },
  optimizeDeps: {
    exclude: ['@anacrolix/torrent'],
  },
  build: {
    target: 'esnext',
    outDir: 'dist',
  },
});
