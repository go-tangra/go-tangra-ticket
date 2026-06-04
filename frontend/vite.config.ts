import { federation } from '@module-federation/vite';
import vue from '@vitejs/plugin-vue';
import { defineConfig } from 'vite';

export default defineConfig(({ command }) => ({
  base: command === 'serve' ? '/' : '/modules/ticket/',
  plugins: [
    vue(),
    federation({
      name: 'ticket',
      filename: 'remoteEntry.js',
      remotes: {
        shell: {
          type: 'module',
          name: 'shell',
          entry:
            command === 'serve'
              ? 'http://localhost:5666/remoteEntry.js'
              : '/remoteEntry.js',
        },
      },
      exposes: {
        './module': './src/index.ts',
      },
      shared: {
        vue: { singleton: true, requiredVersion: '^3.5.13' },
        'vue-router': { singleton: true, requiredVersion: '^4.5.0' },
        pinia: { singleton: true, requiredVersion: '^2.2.2' },
        'ant-design-vue': { singleton: true, requiredVersion: '^4.2.6' },
      },
      dts: false,
    }),
  ],
  server: {
    port: 3013,
    strictPort: true,
    origin: 'http://localhost:3013',
    cors: true,
  },
  build: {
    target: 'esnext',
    minify: true,
  },
}));
