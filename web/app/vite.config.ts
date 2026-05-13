import { defineConfig } from 'vite';
import preact from '@preact/preset-vite';
import { nodePolyfills } from 'vite-plugin-node-polyfills';
import { resolve } from 'node:path';

export default defineConfig({
  plugins: [
    preact({ jsxImportSource: '@emotion/react' }),
    nodePolyfills({
      include: ['buffer', 'events', 'stream', 'process', 'util', 'punycode'],
      globals: { Buffer: true, global: true, process: true },
    }),
  ],
  resolve: {
    alias: {
      react: 'preact/compat',
      'react-dom': 'preact/compat',
      'react/jsx-runtime': 'preact/jsx-runtime',
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
