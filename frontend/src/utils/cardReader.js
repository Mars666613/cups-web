import { onUnmounted, ref } from 'vue'

// 绑卡输入框的「读卡器兼容层」。
//
// 卡号有两种来的方式，这个 composable 让**同一个输入框**都接得住：
//
//   ① 管理员手抄 / 粘贴 —— 现在这是主路径。PC/SC 读卡器（ACR122U 等）
//      挂在打印机旁的终端上，浏览器读不到它，所以 UID 是管理员从终端
//      那儿抄/粘过来的。
//   ② 插在这台电脑上的**键盘模拟读卡器**（HID）—— 刷卡 = 极快地敲一串
//      字符 + 回车。它敲出来的就是普通按键，进哪个输入框取决于焦点。
//
// 平时走 ①：输入框自己处理按键，粘贴、退格、选区全都正常。
// 只有**焦点跑到别处时**才由这里的全局监听兜底，把读卡器敲出来的字符
// 捡回来填进输入框 —— 少了这道兜底，管理员手滑点一下空白处，
// 刷卡就整个丢失，而且界面上看不出任何异常（他们只会以为卡没刷上）。
//
// 🚫 存的是**原样字符串**：大小写与分隔符的归一是服务端做的
// （`store.NormalizeCardNo`，绑定与刷卡两侧对称），这里再归一一次只会
// 制造第二套规则、让两边漂移。

// gapResetMs 两次按键间隔超过它就重置缓冲。没有这条的话：管理员误敲
// 几个键、过一会儿再刷卡，缓冲里会拼出「误敲的字符 + 真卡号」这种谁也
// 看不懂的串。
const gapResetMs = 500
// maxLen 上限。读卡器不可能吐出这么长的串；防的是「按住某个键不放」。
const maxLen = 64

export function useCardReader(options = {}) {
  // text 与输入框双向绑定。它是唯一的事实来源 —— 手输、粘贴、读卡器
  // 兜底这三条路都往它上面写。
  const text = ref('')
  // reading 为真时开启全局兜底监听。
  const reading = ref(false)

  let lastKeyAt = 0
  let bound = false
  // 焦点在可编辑元素里时用它判断 —— 那种情况不该由我们接管。
  const inputEl = options.inputEl || null

  function isEditable(el) {
    if (!el) return false
    if (inputEl && el === inputEl.value) return true
    const tag = (el.tagName || '').toLowerCase()
    return tag === 'input' || tag === 'textarea' || el.isContentEditable === true
  }

  function onKeydown(e) {
    if (!reading.value) return
    // 焦点在输入框里 → 让它自己处理。粘贴、退格、选区都靠浏览器原生行为，
    // 我们插手只会把它们弄坏。
    if (isEditable(e.target)) {
      lastKeyAt = Date.now()
      return
    }

    const now = Date.now()
    if (now - lastKeyAt > gapResetMs) text.value = ''
    lastKeyAt = now

    // 修饰键组合一律放行（不拦截、不参与缓冲）：那不是读卡器，是人在按
    // F5 或 Ctrl+Shift+I。拦截它们会让页面像卡死。
    if (e.ctrlKey || e.metaKey || e.altKey) return

    if (e.key === 'Enter' || e.key === 'Tab') {
      e.preventDefault()
      options.onSubmit?.()
      return
    }
    if (e.key === 'Escape') {
      e.preventDefault()
      text.value = ''
      return
    }
    if (e.key === 'Backspace') {
      e.preventDefault()
      text.value = text.value.slice(0, -1)
      return
    }
    // 只收单字符键（可打印字符）。Shift 单独按下时 key 是 "Shift"，
    // 长度大于 1，会被这条挡掉。
    if (e.key.length !== 1) return

    e.preventDefault()
    if (text.value.length >= maxLen) return
    text.value += e.key
  }

  function start() {
    if (bound) return
    // 挂在 window 上而不是某个元素：兜底的意义就是「焦点不在输入框里」
    // 也能接住，所以不能依赖焦点。
    window.addEventListener('keydown', onKeydown, true)
    bound = true
    reading.value = true
  }

  function stop() {
    if (bound) {
      window.removeEventListener('keydown', onKeydown, true)
      bound = false
    }
    reading.value = false
  }

  function reset() {
    text.value = ''
    lastKeyAt = 0
  }

  // 组件卸载时必须摘掉监听，否则模态框关了监听还在，
  // 页面上其它地方的输入会被静默吞掉。
  onUnmounted(stop)

  return { text, reading, start, stop, reset }
}
