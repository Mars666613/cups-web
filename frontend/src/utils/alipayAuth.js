// 支付宝授权登录的【全部离页与轮询逻辑】—— 前端只有这一处决定怎么离开当前页面。
//
// 换唤起方式（短链参数、别的 scheme、以后支付宝给了新接口）只改这个文件。
// 真机联调的结论都记在这里，别丢：
//
//   · 系统浏览器里直接开授权链接 → 死路（「请在支付宝客户端打开链接」，无可点按钮）
//   · 系统浏览器里开 alipays:// → 能授权，但结果落在支付宝 webview 里，
//     而支付宝的 webview【干不了活】：它的文件选择器翻不到微信/QQ 的文件
//     （浏览器用的是系统文件管理器，有「快捷访问」），pdf.js 的 worker 也起不来、
//     PDF 预览一直转圈。
//   · 所以结论是【授权在支付宝里做，会话交回浏览器】—— 就是微信 PC 扫码登录那套。
//
// 轮询为什么能成立：自定义 scheme 唤起 App 时浏览器【不会卸载当前页】，
// 所以我们这一页还在，可以继续发请求。（这条照抄 PaymentPanel 的教训。）

import { apiFetch, readError, readJSON } from './api'
import { isAlipayClient } from './device'

// 支付宝用来"在支付宝里打开一个网页"的固定入口。
// appId=20000067 是支付宝自己的应用号（不是我们的 app_id）。
const ALIPAY_START_APP = 'alipays://platformapi/startapp?appId=20000067&url='

const POLL_INTERVAL_MS = 1500
// 总时长给得比较宽：用户可能在支付宝里慢悠悠地看。
// 真正的过期判据在服务端（状态行 15 分钟），这里只是别让页面永远转下去。
const POLL_MAX_MS = 5 * 60 * 1000

export function isInAlipay() {
  return isAlipayClient()
}

// ══════════════════════════════════════════════════════════════════════
// 🚫 不要再试着"让支付宝自动退回浏览器"了 —— 三条路都真机试过，全都不行：
//
//   AlipayJSBridge.call('popWindow')                              → 只关这一页，露出支付宝首页
//   AlipayJSBridge.call('exitApp')                                 → 同上（退的是"自身应用"，即我们这个页面栈）
//   AlipayJSBridge.call('exitApp', { closeActionType: 'exitTop' })  → 同上
//
// 根因是 Android 的返回栈：支付宝本来就在运行，我们的页面压在它主界面上，
// H5 没有权限一次性把整个 App 干掉。支付宝开放平台论坛也有官方回复：
// 「手机浏览器跳转支付宝后，支付宝不会自动跳转回原浏览器，这是授权机制的设计限制」。
//
// 而"读两秒后自动关页"更糟 —— 实测反馈是「页面关了，提示也没看到，
// 一脸茫然地停在支付宝首页」。所以现在的做法是：**这一页只显示提示，什么都不做，
// 让用户自己读完、自己按返回键。** 见 App.vue 里的 alipayNeedBrowserReturn。
// ══════════════════════════════════════════════════════════════════════

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms))
}

// beginAlipayLogin 发起授权登录。成功时 resolve（此时会话已经落在【本浏览器】上）。
//
// 两条路，取决于我们现在在哪：
//
//   在支付宝里 → 直接跳授权链接。授权完回跳，本页自己就是会话的持有者。
//   在浏览器里 → 把【授权页】交给支付宝打开，然后本页轮询，等支付宝那边
//                兑换完，再把会话领回浏览器。
export async function beginAlipayLogin() {
  const resp = await apiFetch('/api/auth/alipay/start')
  if (!resp.ok) {
    throw new Error(await readError(resp))
  }
  const data = await readJSON(resp)
  if (!data || !data.authorizeUrl) {
    throw new Error('服务端没有返回授权地址，请查看服务端日志')
  }

  if (isAlipayClient()) {
    // 端内：已经在支付宝里了，直接就位。页面正在导航走，这个 promise 不会再 resolve。
    window.location.href = data.authorizeUrl
    return new Promise(() => {})
  }

  // 端外：把授权页交给支付宝打开。⚠️ 用 location.href 在【当前标签】跳 ——
  // 自定义 scheme 不会卸载当前页，我们这一页还在，下面的轮询才有地方跑。
  //
  // backBehavior=pop：让 Android 的返回键【直接关掉这一页】，而不是在历史里后退。
  // 否则历史是「授权页 → 我们的页」，按返回会退回授权页，而 auth_base 是静默的、
  // 立刻又跳回来 —— 用户看到的是来回打乒乓。
  window.location.href =
    ALIPAY_START_APP + encodeURIComponent(data.authorizeUrl) + '&backBehavior=pop'
  return pollUntilSettled(data.state)
}

// pollUntilSettled 等支付宝那边把授权结果写回来，然后领走会话。
async function pollUntilSettled(state) {
  const deadline = Date.now() + POLL_MAX_MS

  while (Date.now() < deadline) {
    await sleep(POLL_INTERVAL_MS)

    let resp
    try {
      resp = await apiFetch(`/api/auth/alipay/poll?state=${encodeURIComponent(state)}`)
    } catch {
      // 轮询本来就要容忍网络抖一下 —— 用户此刻正在 App 之间来回切，网络状态不稳。
      continue
    }
    if (!resp.ok) continue

    let data
    try {
      data = await readJSON(resp)
    } catch {
      continue
    }

    if (data?.status === 'pending') continue
    if (data?.status === 'expired') {
      throw new Error('登录状态已失效，请重新点击支付宝登录')
    }
    // 不是 status 就是一份会话（服务端把 cookie 签给【这个浏览器】了）。
    if (data && data.userId) return data
  }

  throw new Error('等待授权超时，请重新点击支付宝登录')
}
