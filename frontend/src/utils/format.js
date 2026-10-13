export function formatFileSize(bytes) {
  if (!bytes) return '0 B'
  if (bytes < 1024) return bytes + ' B'
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB'
  return (bytes / (1024 * 1024)).toFixed(1) + ' MB'
}

// formatCents 把「分」换算成人民币展示串，如 1234 → "¥12.34"。
//
// 金额一律以「分」为整数在前后端间传递（与「字节存 int、前端显示 MB」同一习惯）。
// 钱不走浮点：0.1 + 0.2 那类误差累积到账单上就是事故。
export function formatCents(cents) {
  const n = Number(cents)
  if (!Number.isFinite(n)) return '¥0.00'
  const sign = n < 0 ? '-' : ''
  const abs = Math.abs(Math.round(n))
  return `${sign}¥${Math.floor(abs / 100)}.${String(abs % 100).padStart(2, '0')}`
}

export function formatTime(iso) {
  if (!iso) return ''
  try {
    return new Date(iso).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
  } catch { return iso }
}

export function formatPrinterName(uri) {
  if (!uri) return ''
  const parts = uri.split('/')
  return parts[parts.length - 1] || uri
}

// printerLabel 拼打印机下拉的主标签：队列名 + CUPS 描述（issue #101）。
// CUPS 建队列时若没指定 -D，printer-info 会默认等于队列名，这时不重复显示。
export function printerLabel(printer) {
  if (!printer) return ''
  const name = printer.name || formatPrinterName(printer.uri)
  const info = (printer.info || '').trim()
  if (info && info.toLowerCase() !== name.toLowerCase()) return `${name} — ${info}`
  return name
}

// printerDescription 拼下拉项的副标题：型号 · 位置 · URI。
// 描述也重名时靠这一行区分（同型号多台、或多台机器共用一个描述）。
export function printerDescription(printer) {
  if (!printer) return ''
  return [
    (printer.makeAndModel || '').trim(),
    (printer.location || '').trim(),
    printer.uri
  ].filter(Boolean).join(' · ')
}

export function formatDurationSeconds(totalSeconds) {
  if (!totalSeconds || totalSeconds < 0) return '未知'
  const d = Math.floor(totalSeconds / 86400)
  const h = Math.floor((totalSeconds % 86400) / 3600)
  const m = Math.floor((totalSeconds % 3600) / 60)
  if (d > 0) return `${d}天${h}小时`
  if (h > 0) return `${h}小时${m}分钟`
  if (m > 0) return `${m}分钟`
  return `${totalSeconds}秒`
}

export function statusColor(status) {
  const map = { queued: 'info', printed: 'success', failed: 'error', cancelled: 'neutral' }
  return map[status] || 'neutral'
}

export function statusText(status) {
  const map = { queued: '排队中', printed: '已打印', failed: '失败', cancelled: '已取消' }
  return map[status] || status
}

// ⚠️ 打印记录的状态与【订单】的状态是两套东西，别混用：
//   记录：queued / printed / failed / cancelled
//   订单：pending_payment / held / paid / printing / printed / failed / refunded / expired / cancelled
// 拿 statusText 去渲染订单状态时，printing / refunded / expired 这些会落不到
// 映射里、被原样透出英文。实测踩过：中间页的订单标签显示成 "printing"。
//
// 订单状态目前有多个使用者（PaymentModal、ResultView、AdminOrdersView、
// 待取件列表），所以放在这里共用，不要再各写一份 —— 用户可见的文案分叉过就会漂。
export function orderStatusColor(status) {
  return {
    pending_payment: 'warning',
    // 待取件用 warning（需要用户【去做点什么】），不是 info（等着就行）。
    held: 'warning',
    paid: 'info',
    printing: 'info',
    printed: 'success',
    failed: 'error',
    refunded: 'neutral',
    expired: 'neutral',
    cancelled: 'neutral'
  }[status] || 'neutral'
}

export function orderStatusText(status) {
  return {
    pending_payment: '待支付',
    held: '待取件',
    paid: '已支付',
    printing: '打印中',
    printed: '已打印',
    failed: '失败',
    refunded: '已退款',
    expired: '已过期',
    cancelled: '已取消'
  }[status] || status
}

// 订单的支付方式（`print_orders.pay_method`）。
//
// 存的是渠道标识，不是给人看的字符串。空串 = 还没付款（订单仍在
// pending_payment），也返回空串，让调用方整个不渲染这个标签。
//
// ⚠️ 认不出来的值返回空串而不是原样透出：把 `alipay` 这种渠道 id 印在
// 用户界面上，看起来就像坏了。新增渠道时要在这里补一行 —— 漏了的表现是
// 「这个渠道支付的订单不显示支付方式」，而不是显示一串英文。
export function payMethodText(m) {
  return {
    balance: '余额支付',
    alipay: '支付宝支付',
    wechat: '微信支付',
    mock: '模拟支付'
  }[m] || ''
}

export function printerStateColor(state) {
  const map = { idle: 'success', processing: 'warning', stopped: 'error' }
  return map[state] || 'neutral'
}

export function printerStateText(state) {
  const map = { idle: '空闲', processing: '打印中', stopped: '已停止' }
  return map[state] || state || '未知'
}

// userLabel 把用户名渲染成【能放进表格】的样子 —— 只做长度保护，不做美化。
//
// ⚠️ 不要把它美化成「支付宝顾客」之类的标签。管理端的「用户名」筛选是【精确匹配】
// （后端 `u.username = $1`），表格里显示什么，管理员就得能照着自己去搜什么 ——
// 而"按用户名查某个人的订单 / 流水"正是那几个列表存在的意义。美化了就等于
// 把这条路堵死：表格里写着「支付宝顾客」，搜索框里该输什么？
//
// 支付宝账号的用户名是 `alipay:<6 位 base32>`（见后端 alipayUsername），本身很短。
// 但历史数据可能是老的 `alipay:<128 位 open_id>`，那种必须截断，否则撑破表格。
export function userLabel(username) {
  if (typeof username !== 'string') return username
  const max = 24
  return username.length > max ? username.slice(0, max - 1) + '…' : username
}

// ⚠️ 不要去"美化"用户名 —— 例如把 alipay:8F3K2Q 显示成「支付宝用户」。
//
// 曾经这么干过，被指出来是错的：顾客要退款、联系管理员时，管理员得知道【退谁】，
// 而顾客能报出来的唯一凭据就是这个用户名。把它对顾客藏起来，等于把对账的唯一
// 通路掐断 —— 管理员对着一个「支付宝用户」什么也查不到。
//
// 用户名之所以做成短的（见后端 alipayUsername），本来就【是为了能念给别人听】。
// 所以顾客的个人中心、顶栏、管理端的表格，一律显示这个原值。

export function markerLevelColor(level) {
  if (level === undefined || level === null) return 'text-muted'
  if (level <= 10) return 'text-error font-bold'
  if (level <= 25) return 'text-warning font-medium'
  return 'text-success'
}

export function markerBarColor(level) {
  if (level === undefined || level === null) return 'bg-muted'
  if (level <= 10) return 'bg-error'
  if (level <= 25) return 'bg-warning'
  return 'bg-success'
}
