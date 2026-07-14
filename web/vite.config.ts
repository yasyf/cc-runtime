import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Export CC_RUNTIME_DEV_PORT (the daemon's HTTP port) before `npm run dev`.
const devPort = process.env.CC_RUNTIME_DEV_PORT ?? '8790';
const devTarget = `http://127.0.0.1:${devPort}`;

export default defineConfig({
  plugins: [react()],
  base: '/',
  build: {
    // Straight into the Go embed target, outside this web root.
    outDir: '../internal/web/dist',
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '/api': { target: devTarget, changeOrigin: false },
      '/events': {
        target: devTarget,
        changeOrigin: false,
        // SSE must stream, never buffer.
        configure(proxy) {
          proxy.on('proxyRes', (proxyRes) => {
            proxyRes.headers['x-accel-buffering'] = 'no';
            proxyRes.headers['cache-control'] = 'no-cache';
          });
        },
      },
    },
  },
});
