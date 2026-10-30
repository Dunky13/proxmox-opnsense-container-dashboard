import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  build: {
    rolldownOptions: {
      output: {
        codeSplitting: {
          groups: [
            {
              name: 'react',
              test: /node_modules\/.*(?:react-dom|scheduler|\/react\/)/,
            },
            { name: 'ui', test: /node_modules\/.*(?:@base-ui|@floating-ui)/ },
            { name: 'schema', test: /node_modules\/.*zod/ },
          ],
        },
      },
    },
  },
  server: {
    host: '0.0.0.0',
    proxy: {
      '/api': {
        target: process.env.DASHBOARD_API_ORIGIN || 'http://127.0.0.1:8080',
      },
    },
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./tests/setup.ts'],
    include: ['tests/**/*.test.ts', 'tests/**/*.test.tsx'],
    maxWorkers: 2,
  },
});
