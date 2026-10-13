// copyText 把一段文字复制到剪贴板，返回是否成功。
//
// ⚠️ 不能只用 navigator.clipboard —— 它要求【安全上下文】（HTTPS 或 localhost）。
// 这个系统的门店部署常常是内网 http://192.168.1.22:1180 这种地址，那里
// navigator.clipboard 直接是 undefined（不是拒绝，是压根没有）。所以必须带上
// 老办法兜底，否则"复制用户名"这个按钮在最需要它的场景里恰恰是坏的。
//
// 用它的地方主要是「把用户名报给管理员」—— 手机上手动选中一段文字很麻烦，
// 所以这个降级路径不是可选项。
export async function copyText(text) {
  const value = String(text ?? '')
  if (!value) return false

  if (typeof navigator !== 'undefined' && navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(value)
      return true
    } catch {
      // 被拒（没授权、焦点不在页面…）就落到下面那条
    }
  }

  // 已废弃，但在非安全上下文里仍然可用 —— 而那正是我们要兼容的场景。
  try {
    const ta = document.createElement('textarea')
    ta.value = value
    ta.setAttribute('readonly', '')
    // 不能 display:none（那样选不中），移出视口即可。
    ta.style.position = 'fixed'
    ta.style.top = '-1000px'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    ta.setSelectionRange(0, value.length)
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    return ok
  } catch {
    return false
  }
}
