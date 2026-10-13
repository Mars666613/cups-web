// 把「当前待打印的文件」暂存到 IndexedDB，供页面被浏览器回收后恢复。
//
// ── 为什么需要 ──
//
// 实测（手机）：传好文件、预览正常 → 切到微信回消息 → 手机内存紧张，
// 浏览器把标签页【回收】→ 切回来时页面重新加载，文件没了，一切从头。
//
// 回收本身拦不住 —— 那是浏览器的内存策略，网页没有任何 API 能阻止它。
// 但文件是【我们自己持有的对象】，可以提前存一份，重载后还回去。
//
// ── 为什么用 IndexedDB 而不是 localStorage ──
//
// 文件是二进制且可能几十 MB。localStorage 只能存字符串（base64 编码再涨
// 三分之一），而且有 5MB 硬上限 —— 一张手机拍的照片就超了。
//
// ⚠️ 这里存的是【用户上传的原始文件】，属于隐私数据。所以：
//   - 打印成功、清空文件、返回落地页、登出，都立刻清掉
//   - 超过 TTL 的记录读取时直接丢弃（共用手机的场景）
//
// ⚠️ 所有操作都是「尽力而为」：失败只打日志，绝不让它影响打印本身。
// IndexedDB 在隐私模式下可能直接不可用，配额也可能满。

const DB_NAME = 'cups-web'
const STORE = 'pending-upload'
const KEY = 'current'

// 6 小时：够覆盖「切出去回个消息」这类真实场景，又不至于让文件在
// 共用手机上躺一整天。
const TTL_MS = 6 * 60 * 60 * 1000

function openDB() {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, 1)
    req.onupgradeneeded = () => {
      const db = req.result
      if (!db.objectStoreNames.contains(STORE)) {
        db.createObjectStore(STORE)
      }
    }
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
}

// ── File 的包装 ──────────────────────────────────────────────────
//
// ⚠️ 不直接把 File 丢进 IndexedDB 就完事：个别浏览器（尤其老 WebView）把
// File 结构化克隆回来时只剩一个 Blob，name / lastModified 丢了。而
// processFile 正是靠 f.name 与 f.type 认文件类型（.jpg / .docx / .ofd），
// 名字一丢就认不出来，恢复出来的文件会当成「未知类型」。
//
// 自己带着这几个字段，读回来时重新拼一个 File，与浏览器的克隆行为无关。
//
// 递归处理整个 payload，这样调用方照常塞 File 即可，不必关心包装。
// packValue / unpackValue 导出是为了能被单独验证：往返一旦出错，表现是
// 「恢复出来的文件没有名字」，而类型识别完全依赖 f.name —— 这种错在界面上
// 看不出来，只会当成「不支持的文件类型」。
export function packValue(v) {
  if (typeof File !== 'undefined' && v instanceof File) {
    return { __file: true, name: v.name, type: v.type, lastModified: v.lastModified, blob: v }
  }
  if (Array.isArray(v)) return v.map(packValue)
  if (v && typeof v === 'object') {
    const out = {}
    for (const k of Object.keys(v)) out[k] = packValue(v[k])
    return out
  }
  return v
}

export function unpackValue(v) {
  if (v && typeof v === 'object' && v.__file) {
    // 浏览器正常克隆时它本身就是个完好的 File，直接用。
    if (v.blob instanceof File && v.blob.name) return v.blob
    return new File([v.blob], v.name || 'document', {
      type: v.type || '',
      lastModified: v.lastModified || Date.now()
    })
  }
  if (Array.isArray(v)) return v.map(unpackValue)
  if (v && typeof v === 'object') {
    const out = {}
    for (const k of Object.keys(v)) out[k] = unpackValue(v[k])
    return out
  }
  return v
}

// 单次暂存的体积上限。超过就不存了 —— 手机上的 IndexedDB 配额有限，
// 而且这种量级的文件本来也不该塞进浏览器存储。
const MAX_TOTAL_BYTES = 100 * 1024 * 1024

function totalBytes(v) {
  if (v && typeof v === 'object' && v.__file) return v.blob?.size || 0
  if (Array.isArray(v)) return v.reduce((n, x) => n + totalBytes(x), 0)
  if (v && typeof v === 'object') return Object.values(v).reduce((n, x) => n + totalBytes(x), 0)
  return 0
}

// savePendingUpload 暂存当前待打印的原始文件。
// payload 里可以直接放 File，由调用方决定形状；这里负责包装与体积把关。
export async function savePendingUpload(payload) {
  try {
    const packed = packValue(payload)
    const bytes = totalBytes(packed)
    if (bytes > MAX_TOTAL_BYTES) {
      console.warn(
        `[pending] 文件合计 ${(bytes / 1048576).toFixed(1)}MB，超过暂存上限，` +
        '页面若被浏览器回收将无法自动恢复（不影响本次打印）。'
      )
      return
    }
    const db = await openDB()
    try {
      await new Promise((resolve, reject) => {
        const tx = db.transaction(STORE, 'readwrite')
        tx.objectStore(STORE).put({ savedAt: Date.now(), payload: packed }, KEY)
        tx.oncomplete = () => resolve()
        tx.onerror = () => reject(tx.error)
        tx.onabort = () => reject(tx.error)
      })
    } finally {
      db.close()
    }
  } catch (e) {
    console.warn('[pending] 暂存上传文件失败（不影响打印）:', e)
  }
}

// loadPendingUpload 读取暂存的文件；没有、过期或读失败都返回 null。
export async function loadPendingUpload() {
  try {
    const db = await openDB()
    let rec = null
    try {
      rec = await new Promise((resolve, reject) => {
        const tx = db.transaction(STORE, 'readonly')
        const req = tx.objectStore(STORE).get(KEY)
        req.onsuccess = () => resolve(req.result || null)
        req.onerror = () => reject(req.error)
      })
    } finally {
      db.close()
    }
    if (!rec) return null
    if (!rec.savedAt || Date.now() - rec.savedAt > TTL_MS) {
      await clearPendingUpload()
      return null
    }
    return unpackValue(rec.payload)
  } catch (e) {
    console.warn('[pending] 读取暂存上传文件失败:', e)
    return null
  }
}

// clearPendingUpload 清掉暂存。
//
// 打印成功、用户清空文件、返回落地页、登出时都要调 —— 存的是原始文档，
// 不该在手机上多留。
export async function clearPendingUpload() {
  try {
    const db = await openDB()
    try {
      await new Promise((resolve, reject) => {
        const tx = db.transaction(STORE, 'readwrite')
        tx.objectStore(STORE).delete(KEY)
        tx.oncomplete = () => resolve()
        tx.onerror = () => reject(tx.error)
        tx.onabort = () => reject(tx.error)
      })
    } finally {
      db.close()
    }
  } catch (e) {
    // 清不掉也不该报错打扰用户；TTL 会兜底。
    console.warn('[pending] 清除暂存上传文件失败:', e)
  }
}
