import { defineConfig, loadEnv } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import tailwindcss from '@tailwindcss/vite';
import { mockApi } from './mock/plugin.ts';

export default defineConfig(({ mode }) => {
  const env = { ...process.env, ...loadEnv(mode, process.cwd(), '') };
  const mock = env.VITE_MOCK === '1';
  return {
    plugins: [svelte(), tailwindcss(), ...(mock ? [mockApi()] : [])],
    server: {
      port: 5173,
      proxy: mock ? undefined : { '/api': { target: env.NODESPOT_BACKEND || 'http://localhost:8080', changeOrigin: false } },
    },
    build: { outDir: 'dist', emptyOutDir: true, target: 'es2022' },
  };
});
