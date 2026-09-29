<template>
  <!-- ── 支付成功 ────────────────────────────────────────────────
       刻意【不】立刻消失：用户需要一个明确的成功反馈，否则会怀疑
       到底付上没有。给 5 秒倒计时自动返回，也允许手动点「返回」
       提前走 —— 两条路都给，用户不必等。 -->
  <div v-if="success" class="flex flex-col items-center gap-2 py-6 text-center">
    <div class="w-14 h-14 rounded-full bg-success/15 flex items-center justify-center">
      <UIcon name="i-lucide-check" class="w-8 h-8 text-success" />
    </div>
    <div class="text-lg font-semibold">{{ successTitle }}</div>
    <div v-if="successDesc" class="text-sm text-muted max-w-xs">{{ successDesc }}</div>
    <div class="text-xs text-muted mt-1">{{ successLeft }} 秒后自动返回</div>
    <UButton color="primary" class="mt-2" @click="emit('done')">返回</UButton>
  </div>

  <!-- ── 等待支付 ──────────────────────────────────────────────── -->
  <div v-else class="space-y-3">
    <UAlert
      v-if="refreshFailed"
      color="warning"
      variant="soft"
      icon="i-lucide-triangle-alert"
      title="二维码刷新失败"
      :description="error || '请检查网络后点下面的「刷新二维码」重试。'"
    />
    <UAlert
      v-else
      color="info"
      variant="soft"
      icon="i-lucide-info"
      title="请完成支付"
      :description="tipText"
    />

    <!-- 真实渠道。渲染依据是【服务端返回了哪种凭据】，见脚本里的 isWap。 -->
    <template v-if="cred && !cred.mock">
      <!-- 手机网站支付：服务端给了收银台地址，当前标签直接跳过去。
           跳转由支付宝负责唤起 App，付完再把浏览器送回 return_url。
           ⚠️ 这条路上【没有】二维码，也【不】开新标签。 -->
      <template v-if="isWap">
        <UButton
          color="primary"
          size="lg"
          block
          icon="i-lucide-smartphone"
          @click="goToCashier"
        >去支付宝付款 {{ formatCents(cred.amountCents) }}</UButton>
        <div class="text-xs text-muted text-center">
          点击后在本页打开支付宝收银台，付完会自动回到这里
        </div>
      </template>

      <!-- 当面付二维码：电脑上主操作是扫码；手机上是收银台走不通时的退路，
           用 URL Scheme 直接唤起 App（手机扫不了自己屏幕上的码）。 -->
      <template v-else>
        <UButton
          v-if="isMobile && canOpenApp"
          color="primary"
          size="lg"
          block
          icon="i-lucide-smartphone"
          @click="openAlipayApp"
        >去支付宝付款 {{ formatCents(cred.amountCents) }}</UButton>

        <QrCode v-if="showQrCode" :value="cred.qrCodeText" :alt="qrAlt">
          <div class="text-xs text-muted">
            扫码支付 <span class="font-semibold text-primary">{{ formatCents(cred.amountCents) }}</span>
          </div>
          <!-- 倒计时：到期后会自动换一张，把剩余时间和「正在刷新」都写出来，
               避免用户对着一个失效的码反复扫。 -->
          <div class="text-[11px]" :class="expired ? 'text-warning' : 'text-muted'">
            <template v-if="refreshing">
              <UIcon name="i-lucide-loader-circle" class="w-3 h-3 inline animate-spin" />
              正在刷新二维码…
            </template>
            <template v-else-if="expired">二维码已过期</template>
            <template v-else>{{ remainingText }} 后过期，会自动刷新</template>
          </div>
        </QrCode>

        <!-- 手机上补一个切换入口：万一唤起失败，用户仍有出路 -->
        <button
          v-if="isMobile && canOpenApp"
          type="button"
          class="text-xs text-muted underline cursor-pointer"
          @click="showQrCode = !showQrCode"
        >{{ showQrCode ? '收起二维码' : '用另一台设备扫码' }}</button>
      </template>
    </template>

    <div class="flex flex-wrap gap-2">
      <UButton
        v-if="cred && cred.mock"
        color="primary"
        :loading="busy"
        icon="i-lucide-badge-check"
        @click="emit('mock-pay')"
      >模拟支付成功</UButton>
      <UButton
        v-if="refreshFailed"
        color="primary"
        :loading="busy"
        icon="i-lucide-refresh-cw"
        @click="emit('refresh')"
      >刷新二维码</UButton>
      <UButton
        v-else
        variant="outline"
        :loading="busy"
        icon="i-lucide-refresh-cw"
        @click="emit('sync')"
      >我已支付</UButton>
      <UButton variant="ghost" :disabled="busy" @click="emit('cancel')">取消</UButton>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, watch, onUnmounted } from 'vue'
import { formatCents } from '../utils/format'
import { isMobileDevice } from '../utils/device'
import QrCode from './QrCode.vue'

const props = defineProps({
  // 渠道凭据：{ outTradeNo, qrCodeText, amountCents, expiresAt, mock }
  cred: { type: Object, default: null },
  // 是否有请求在飞（禁用按钮、显示 loading）
  busy: { type: Boolean, default: false },
  // 是否正在刷新二维码（父组件在持有这个状态，避免重复触发）
  refreshing: { type: Boolean, default: false },
  // 刷新失败的错误文案；非空时切换成「刷新二维码」按钮
  error: { type: String, default: '' },
  success: { type: Boolean, default: false },
  successTitle: { type: String, default: '支付成功' },
  successDesc: { type: String, default: '' },
  qrAlt: { type: String, default: '支付二维码' }
})

const emit = defineEmits(['refresh', 'sync', 'mock-pay', 'cancel', 'done'])

// ─── 收款方式 ──────────────────────────────────────────────────────
//
// 判断依据是【服务端返回了哪种凭据】，而不是在这里再看一遍设备：
//   qrCodeText 非空            → 当面付二维码
//   qrCodeText 为空、payUrl 有值 → 手机网站支付的收银台地址
//
// ⚠️ 为什么不在这里自己判断设备：服务端在拿不到回跳地址时会把手机网站支付
// 【降级】成二维码（见 cmd/server 的 createPayment）。界面必须跟着降级，
// 否则会渲染出一个 value 为空字符串的二维码 —— Reka UI 把空串保留给
// 「清空选择」，传进去会抛错并打崩整棵组件树（前端静态检查就是为这个加的）。
// 以服务端的结论为准，降级就自然对上了。
const isWap = computed(() => !props.cred?.qrCodeText && Boolean(props.cred?.payUrl))

const isMobile = isMobileDevice()

// 二维码这条路上才有「用 Scheme 唤起 App」这个动作可做 ——
// scheme 喂进去的正是 qr_code。
const canOpenApp = computed(() => Boolean(props.cred?.qrCodeText && props.cred?.payUrl))

// 操作指引必须跟着收款方式走：手机上让用户「扫码」是句做不到的话
// （他没法扫自己屏幕上那张码），反过来在电脑上让他「点按钮跳转」
// 也不对（电脑上未必装了支付宝）。
const tipText = computed(() => {
  if (props.cred?.mock) return '当前是本地模拟渠道，点下面的按钮即可模拟渠道回调到账。'
  if (isWap.value) return '点下面的按钮在支付宝完成付款，付完会自动回到本页面。'
  return isMobile
    ? '点下面的按钮直接打开支付宝付款，付完切回本页面即可，到账会自动开始打印。'
    : '用支付宝扫码完成支付，到账后会自动开始打印，无需其他操作。'
})

// goToCashier 跳到支付宝收银台（手机网站支付）。
//
// ⚠️ 用 location.href 在【当前标签】跳，不开新标签。付完支付宝会把浏览器
// 送回我们配的 return_url（本站某一页），于是本标签自己就回来了；
// 开新标签的话回来的是那个新标签，我们的页面反而被晾在一边，用户还得自己
// 去翻 —— 那正是改用官方方案要解决的问题。
//
// 代价要说清楚：本页会被卸载，弹窗连同轮询一起没了。所以回跳之后由
// App.vue 的全局处理凭 URL 上的 out_trade_no 补一次查单（见 usePaymentReturn）。
function goToCashier() {
  const url = props.cred?.payUrl
  if (!url) return
  window.location.href = url
}

const showQrCode = ref(true)
// 电脑上永远是二维码；手机上一开始收起二维码，把位置让给跳转按钮。
watch(
  () => [isMobile, props.cred?.outTradeNo],
  () => { showQrCode.value = !isMobile },
  { immediate: true }
)

// openAlipayApp 直接唤起支付宝 App 完成付款（手机上的主路径）。
//
// ⚠️ 不能用 cred.payUrl（= https://qr.alipay.com/...）：那是个【网页】。
// 实测路径是「新标签打开支付宝网页 → 页面上再点一次『打开支付宝』→ 系统弹
// 应用选择器 → 付款 → 切回浏览器发现停在支付宝网页上，还要再按返回」——
// 四五个动作，付完还看不到我们的页面，用户根本不知道充上没有。
//
// 支付宝 App 注册了 URL Scheme，把「扫到了这个码」这件事直接喂给它，
// 就能一步落到收银台，中间不经过任何网页：
//
//   alipays://platformapi/startapp?saId=10000007&qrcode=<urlencode 后的 qr_code>
//
// saId=10000007 是支付宝「扫一扫」的固定 id，qrcode 参数等价于扫到了这个码。
// ⚠️ qrcode 的值【必须 urlencode】，否则不生效。
//
// 🚫 不要用 intent://...;package=com.eg.android.AlipayGphone;end 去绕过应用
// 选择器：那等于把包名写死成正式版支付宝，直接堵死沙箱版（沙箱订单只能用
// 沙箱 App 付）。测试机上正式版和沙箱版都装了，弹「二选一」是正常的 ——
// 线上只装一个，不会弹。
//
// ⚠️ 用 location.href 在【当前标签】跳，而不是 window.open 开新标签：
// 自定义 scheme 唤起 App 时浏览器不会卸载当前页，用户付完切回来看到的
// 还是我们这个页面，visibilitychange 一响就查单出结果。开新标签的话，
// 回来的永远是那个支付宝网页，还得手动切回我们的标签。
function openAlipayApp() {
  const qr = props.cred?.qrCodeText
  const webFallback = props.cred?.payUrl
  if (!qr) {
    if (webFallback) window.location.href = webFallback
    return
  }

  // App 被唤起时页面会切到后台 —— 用它当判据，决定要不要走兜底。
  let appOpened = false
  const watchHidden = () => {
    if (document.visibilityState === 'hidden') appOpened = true
  }
  document.addEventListener('visibilitychange', watchHidden)

  window.location.href =
    'alipays://platformapi/startapp?saId=10000007&qrcode=' + encodeURIComponent(qr)

  // 兜底：没装支付宝（或系统不认这个 scheme）时，上面那次跳转什么都不会发生，
  // 页面停在原地看着像没反应。等 3 秒确认它没被切走，就退回二维码网页链接，
  // 至少让用户还有办法付款。
  setTimeout(() => {
    document.removeEventListener('visibilitychange', watchHidden)
    if (appOpened || document.visibilityState !== 'visible') return
    if (webFallback) window.location.href = webFallback
  }, 3000)
}

// ─── 二维码倒计时 ──────────────────────────────────────────────────

const now = ref(Date.now())
let tickTimer = null

function startTick() {
  stopTick()
  tickTimer = setInterval(() => { now.value = Date.now() }, 1000)
}
function stopTick() {
  if (tickTimer) { clearInterval(tickTimer); tickTimer = null }
}

const remainingMs = computed(() => {
  const t = props.cred?.expiresAt
  if (!t) return Infinity
  const ms = new Date(t).getTime() - now.value
  return Number.isFinite(ms) ? ms : Infinity
})
const expired = computed(() => remainingMs.value <= 0)
const remainingText = computed(() => {
  const s = Math.max(0, Math.ceil(remainingMs.value / 1000))
  const m = Math.floor(s / 60)
  return m > 0 ? `${m} 分 ${s % 60} 秒` : `${s} 秒`
})

// 每个 outTradeNo 只自动触发【一次】刷新。
// 少了这个守卫，倒计时归零后每一秒都会再 emit 一次 refresh ——
// 刷新失败时那就是个死循环。
let refreshFiredFor = null

watch(
  () => [expired.value, props.cred?.outTradeNo, props.refreshing, props.busy],
  () => {
    if (!props.cred || props.cred.mock) return
    if (props.refreshing || props.busy) return
    if (props.error) return // 已经失败了，等用户手动点
    if (!expired.value) return
    if (refreshFiredFor === props.cred.outTradeNo) return
    refreshFiredFor = props.cred.outTradeNo
    emit('refresh')
  }
)

const refreshFailed = computed(() => Boolean(props.error) && expired.value)

watch(
  () => props.cred?.outTradeNo,
  (v) => {
    // 换了一张新码：重置倒计时与刷新守卫。
    if (v) { now.value = Date.now(); startTick() }
    else stopTick()
  },
  { immediate: true }
)

// ─── 成功倒计时 ────────────────────────────────────────────────────

const successLeft = ref(5)
let successTimer = null

watch(
  () => props.success,
  (v) => {
    if (!v) {
      if (successTimer) { clearInterval(successTimer); successTimer = null }
      return
    }
    stopTick()
    successLeft.value = 5
    successTimer = setInterval(() => {
      successLeft.value -= 1
      if (successLeft.value <= 0) {
        clearInterval(successTimer)
        successTimer = null
        emit('done')
      }
    }, 1000)
  },
  { immediate: true }
)

onUnmounted(() => {
  stopTick()
  if (successTimer) clearInterval(successTimer)
})
</script>
