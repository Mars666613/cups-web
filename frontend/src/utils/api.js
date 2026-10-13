// 从 Cookie 提取 CSRF 令牌
export function getCSRF() {
  const m = document.cookie.match('(^|;)\\s*csrf_token\\s*=\\s*([^;]+)')
  return m ? m.pop() : ''
}

// readJSON 解析 JSON 响应体，对非 JSON 给出可读的错误。
//
// 🚫 不要直接 resp.json()：当响应其实是 HTML（反代错误页、SPA 兜底、
// 或者后端二进制太旧、根本没有这个路由）时，抛出的会是
// "Unexpected token '<'" —— 这个错误与真实原因毫无关系，会把人往
// 完全错误的方向带。
//
// 实测踩过：新版前端调旧后端的 /api/admin/prices，SPA 兜底返回
// 200 + HTML，保存被静默吞掉、界面显示成功、刷新后数据消失。
// 后端已修（/api 未知路由现在返回 JSON 404），这里是第二层防御。
export async function readJSON(resp) {
  const text = await resp.text()
  if (text === '') return null
  try {
    return JSON.parse(text)
  } catch {
    throw new Error(
      `服务端返回了非 JSON 响应（HTTP ${resp.status}）：${text.slice(0, 100)}`
    )
  }
}

// 统一解析错误响应
export async function readError(resp) {
  try {
    const data = await resp.json()
    return data.error || resp.statusText
  } catch (e) {
    try {
      const text = await resp.text()
      return text || resp.statusText
    } catch (err) {
      return resp.statusText
    }
  }
}

// ── 会话失效的全局兜底 ────────────────────────────────────────────
//
// ⚠️ 原先只在每个调用点各传各的 `() => emit('logout')`，实测漏了一大片：
// 支付链路上 9 处调用全都没传（PaymentModal 全程一处都没有），
// 症状是会话过期后点「确认支付」反复失败、弹一句英文 Unauthorized，
// 而应用不会登出 —— 用户卡在原地，也看不出是登录过期。
//
// 逐个补是治标的：新写的调用点照样会忘。所以加一个全局默认，
// 第三个参数仍可覆盖（目前只有 PaymentModal 的轮询刻意覆盖过，见那里的说明）。
let unauthorizedHandler = null

// setUnauthorizedHandler 注册会话失效时的默认处理（由 App.vue 在启动时调用）。
export function setUnauthorizedHandler(fn) {
  unauthorizedHandler = fn
}

// 封装 fetch，自动附加 credentials 和 CSRF token，统一处理 401
// onUnauthorized 是一个回调函数，由调用方传入（用于触发登出）
export async function apiFetch(url, options = {}, onUnauthorized = null) {
  const opts = { ...options, credentials: 'include' }

  // 初始化 headers
  const headers = new Headers(opts.headers || {})

  const method = (opts.method || 'GET').toUpperCase()
  const isFormData = opts.body instanceof FormData

  if (method !== 'GET') {
    // 对于非 GET 请求，附加 CSRF token
    headers.set('X-CSRF-Token', getCSRF())

    // 对于非 FormData body，附加 Content-Type
    if (!isFormData) {
      headers.set('Content-Type', 'application/json')
    }
  }

  opts.headers = headers

  const resp = await fetch(url, opts)

  // 401：优先用调用点传的处理，没传就走全局兜底。
  if (resp.status === 401) {
    if (onUnauthorized) onUnauthorized()
    else if (unauthorizedHandler) unauthorizedHandler()
  }

  return resp
}
