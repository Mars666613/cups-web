import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import ui from '@nuxt/ui/vite'

export default defineConfig({
  plugins: [
    vue(),
    ui({
      components: {
        prefix: 'U'
      }
    })
  ],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (id.includes('node_modules/vue') || id.includes('node_modules/vue-router')) {
            return 'vue-vendor'
          }
          if (id.includes('node_modules/@nuxt/ui') || id.includes('node_modules/reka-ui') || id.includes('node_modules/@vueuse')) {
            return 'ui-vendor'
          }
          // lucide 图标全集离线内联（见 main.js），单独成块便于长期缓存——
          // 图标数据几乎不变，与频繁更新的业务代码分离可减少重复下载
          if (id.includes('node_modules/@iconify-json/lucide')) {
            return 'icons-vendor'
          }
          if (id.includes('node_modules/pdfjs-dist')) {
            return 'pdf-vendor'
          }
        }
      }
    }
  },
  server: {
    port: 5173,
    // 监听所有网卡，而不是只绑 localhost。
    //
    // 默认（不设 host）Vite 只绑 127.0.0.1/::1，所以只能用 localhost 访问，
    // 用本机内网 IP（如 192.168.1.22:5173）一律连不上 —— 手机、平板、
    // 同事的电脑都打不开。这个项目要拿手机扫码测支付流程，绑 localhost
    // 等于把最常见的测试方式堵死了。
    //
    // ⚠️ 副作用：dev server 会暴露在局域网内，同网段的人都能访问。
    // 开发环境可以接受；生产不走这个 server（前端是 go:embed 进二进制的）。
    host: true,
    proxy: {
      '/api': {
        target: 'http://localhost:8090',
        changeOrigin: true,
        // 像一个正经反向代理那样转发【原始 Host】。
        //
        // 为什么必须加：changeOrigin 会把 Host 改写成目标地址（localhost:8090），
        // 后端的跨源防护于是看到「Origin 是 192.168.1.22:5173、Host 是 localhost:8090」，
        // 判定为跨源并 403 cross_origin_blocked —— 登录直接失败。
        //
        // 后端为这种情况留了兜底：Sec-Fetch-Site 缺失时改比 X-Forwarded-Host。
        // 补上这个头就走进那条兜底分支，与 nginx 的
        // `proxy_set_header X-Forwarded-Host $host;` 完全对应。
        //
        // 🚫 不要改用 TRUSTED_ORIGINS 写死 IP：开发机是 DHCP，
        //    地址一变（或换台机器、换手机访问）就重新报错。转发真实 Host 与 IP 无关。
        configure: (proxy) => {
          proxy.on('proxyReq', (proxyReq, req) => {
            // req.headers.host 是改写【之前】的原始 Host（如 192.168.1.22:5173）
            if (req.headers.host) {
              proxyReq.setHeader('X-Forwarded-Host', req.headers.host)
            }
          })
        }
      }
    }
  }
})
