// pdf.js worker 的包装入口：先安装旧内核浏览器缺失的 polyfill，再加载官方 worker。
//
// worker 与主线程是独立的 JS 执行环境，主线程里打的 polyfill 不会自动进入 worker。
// pdfjs 5.x 在 worker 内计算文档 fingerprints 时会调用 `Uint8Array.prototype.toHex()`（Issue #86），
// 并大量直接调用 `Promise.withResolvers()` 建立与主线程的通信（Issue #89）；旧版 Chromium
// 内核浏览器缺失这些较新 API 会导致每份 PDF 预览失败。
// 通过 Vite 的 `?worker&url` 打包本文件，polyfill 会被内联进 worker bundle 的最前面，
// 在官方 worker 代码执行前完成方法补齐。
import './uint8-base64.js'
import './promise-with-resolvers.js'
import 'pdfjs-dist/build/pdf.worker.min.mjs'
