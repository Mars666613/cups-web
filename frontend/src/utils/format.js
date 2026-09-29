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

export function printerStateColor(state) {
  const map = { idle: 'success', processing: 'warning', stopped: 'error' }
  return map[state] || 'neutral'
}

export function printerStateText(state) {
  const map = { idle: '空闲', processing: '打印中', stopped: '已停止' }
  return map[state] || state || '未知'
}

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
