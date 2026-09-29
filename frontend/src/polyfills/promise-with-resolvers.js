// Promise.withResolvers polyfill（TC39「Promise.withResolvers」提案）。
//
// 背景：pdfjs-dist 5.x 在主线程与 worker 里大量直接调用 `Promise.withResolvers()`
// （无任何 feature-detect 兜底），这是较新的原生方法，仅 Chromium 119+ / Safari 17.2+
// / Firefox 121+ 才内置。部分国产浏览器（基于旧版 Chromium 内核）缺失该方法，导致
// PDF 在加载阶段（构建文档 / 建立 worker 通信）就抛 `Promise.withResolvers is not a function`，
// 整份 PDF 无法预览、图片走另一条渲染路径不受影响（Issue #89，与 Issue #86 的
// `Uint8Array.prototype.toHex` 属于同一类"旧内核缺新 API"问题）。
//
// 该模块在导入时按需补齐缺失方法，已支持原生方法的浏览器会跳过、零副作用。
// 注意：必须同时在主线程（main.js）与 pdf.js worker（pdf-worker.js）中导入，
// 因为二者是相互独立的 JS 执行环境。
if (typeof Promise.withResolvers !== 'function') {
  Object.defineProperty(Promise, 'withResolvers', {
    value: function withResolvers() {
      let resolve
      let reject
      const promise = new this((res, rej) => {
        resolve = res
        reject = rej
      })
      return { promise, resolve, reject }
    },
    writable: true,
    configurable: true,
  })
}
