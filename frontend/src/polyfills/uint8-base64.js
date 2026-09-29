// Uint8Array base64/hex 方法 polyfill（TC39「Uint8Array to/from base64」提案）。
//
// 背景：pdfjs-dist 5.x 在计算文档 fingerprints（worker 内）与处理签名 / 数据 URL（主线程）
// 时会调用 `Uint8Array.prototype.toHex()` / `toBase64()` 与 `Uint8Array.fromBase64()`。
// 这些是较新的原生方法，仅 Chromium 140+ / Safari 18.2+ / Firefox 133+ 才内置。
// 部分国产浏览器（基于旧版 Chromium）缺失这些方法，导致 PDF 预览在加载阶段就抛
// `a.toHex is not a function`，整份 PDF 无法预览（Issue #86）。
//
// 该模块在导入时按需补齐缺失方法，已支持原生方法的浏览器会跳过、零副作用。
// 注意：必须同时在主线程（main.js）与 pdf.js worker（pdf-worker.js）中导入，
// 因为二者是相互独立的 JS 执行环境。

const HEX_CHARS = '0123456789abcdef'

function bytesToBinaryString(bytes) {
  // 分块拼接，避免超长数组一次性 apply 触发调用栈溢出。
  let binary = ''
  const CHUNK = 0x8000
  for (let i = 0; i < bytes.length; i += CHUNK) {
    binary += String.fromCharCode.apply(null, bytes.subarray(i, i + CHUNK))
  }
  return binary
}

function normalizeBase64Alphabet(str, alphabet) {
  if (alphabet === 'base64url') {
    return str.replace(/-/g, '+').replace(/_/g, '/')
  }
  return str
}

if (typeof Uint8Array.prototype.toHex !== 'function') {
  Object.defineProperty(Uint8Array.prototype, 'toHex', {
    value: function toHex() {
      let out = ''
      for (let i = 0; i < this.length; i++) {
        const b = this[i]
        out += HEX_CHARS[b >> 4] + HEX_CHARS[b & 0x0f]
      }
      return out
    },
    writable: true,
    configurable: true,
  })
}

if (typeof Uint8Array.fromHex !== 'function') {
  Object.defineProperty(Uint8Array, 'fromHex', {
    value: function fromHex(str) {
      if (typeof str !== 'string') {
        throw new TypeError('fromHex expects a string')
      }
      if (str.length % 2 !== 0) {
        throw new SyntaxError('fromHex: string length must be even')
      }
      const out = new Uint8Array(str.length / 2)
      for (let i = 0; i < out.length; i++) {
        const byte = parseInt(str.substr(i * 2, 2), 16)
        if (Number.isNaN(byte)) {
          throw new SyntaxError('fromHex: invalid hex character')
        }
        out[i] = byte
      }
      return out
    },
    writable: true,
    configurable: true,
  })
}

if (typeof Uint8Array.prototype.toBase64 !== 'function') {
  Object.defineProperty(Uint8Array.prototype, 'toBase64', {
    value: function toBase64(options) {
      const alphabet = (options && options.alphabet) || 'base64'
      const omitPadding = !!(options && options.omitPadding)
      let b64 = btoa(bytesToBinaryString(this))
      if (alphabet === 'base64url') {
        b64 = b64.replace(/\+/g, '-').replace(/\//g, '_')
      }
      if (omitPadding) {
        b64 = b64.replace(/=+$/, '')
      }
      return b64
    },
    writable: true,
    configurable: true,
  })
}

if (typeof Uint8Array.fromBase64 !== 'function') {
  Object.defineProperty(Uint8Array, 'fromBase64', {
    value: function fromBase64(str, options) {
      if (typeof str !== 'string') {
        throw new TypeError('fromBase64 expects a string')
      }
      const alphabet = (options && options.alphabet) || 'base64'
      // 去掉空白字符，兼容带换行的 base64。
      let normalized = normalizeBase64Alphabet(str.replace(/[\t\n\f\r ]/g, ''), alphabet)
      const binary = atob(normalized)
      const out = new Uint8Array(binary.length)
      for (let i = 0; i < binary.length; i++) {
        out[i] = binary.charCodeAt(i)
      }
      return out
    },
    writable: true,
    configurable: true,
  })
}
