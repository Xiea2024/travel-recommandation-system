import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// dev server 把 API 请求代理到本机 Go 服务，前端代码完全不用处理 CORS。
// 部署到 Render 时同源托管构建产物，同样无跨域问题。
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/v1': 'http://localhost:8080',
      '/admin': 'http://localhost:8080',
    },
  },
});
