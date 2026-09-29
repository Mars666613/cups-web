<template>
  <div class="p-3 sm:p-4 md:p-6 max-w-5xl mx-auto space-y-4">
    <div class="flex items-center justify-between gap-2 flex-wrap">
      <h1 class="text-lg font-bold flex items-center gap-2">
        <UIcon name="i-lucide-wallet" class="w-5 h-5 text-primary" />
        我的钱包
      </h1>
      <UButton variant="ghost" size="xs" icon="i-lucide-refresh" :loading="loading" @click="load">刷新</UButton>
    </div>

    <!-- 余额 -->
    <UCard>
      <div class="flex flex-wrap items-end justify-between gap-4">
        <div>
          <div class="text-sm text-muted">账户余额</div>
          <div class="text-3xl font-bold text-primary mt-1">{{ formatCents(balanceCents) }}</div>
        </div>
        <div class="flex items-center gap-2">
          <!-- 退款按钮【常驻】。
               早先按可退额度决定它的显隐，结果是按钮时有时无 ——
               用户看到的是「刚才还在的东西没了」，而不是「我现在没钱可退」。
               入口固定、点进去再说明能退多少，才是可预期的。 -->
          <UButton variant="outline" icon="i-lucide-undo-2" @click="openRefund">退款</UButton>
          <div v-if="paymentMethods.length === 0" class="text-xs text-muted max-w-sm text-right">
            余额由管理员线下收款后入账。
          </div>
        </div>
      </div>
    </UCard>

    <!-- 退款弹窗 -->
    <UModal v-model:open="showRefundModal" :ui="{ content: 'max-w-md' }">
      <template #content>
        <div class="p-6 space-y-4">
          <div class="flex items-center justify-between gap-2">
            <h3 class="text-lg font-semibold">{{ refundDone ? '退款结果' : '余额退款' }}</h3>
            <UButton v-if="!refundDone" variant="ghost" size="xs" icon="i-lucide-x" @click="showRefundModal = false" />
          </div>

          <!-- 成功反馈：与支付流程保持一致，给明确的结论 + 手动返回 -->
          <div v-if="refundDone" class="flex flex-col items-center gap-2 py-4 text-center">
            <div class="w-14 h-14 rounded-full bg-success/15 flex items-center justify-center">
              <UIcon name="i-lucide-check" class="w-8 h-8 text-success" />
            </div>
            <div class="text-lg font-semibold">退款成功</div>
            <div class="text-sm text-muted">
              {{ formatCents(refundDoneCents) }} 已原路退回你的支付宝账户，通常几分钟内到账。
            </div>
            <UButton color="primary" class="mt-2" @click="showRefundModal = false">返回</UButton>
          </div>

          <template v-else>
            <!-- 可退金额是进弹窗才该看到的信息，卡片上不铺。 -->
            <div>
              <div class="text-sm text-muted">可退款金额</div>
              <div class="text-2xl font-bold text-primary mt-0.5">{{ formatCents(refundableCents) }}</div>
            </div>

            <!-- 没额度时把【为什么】说清楚。
                 只给一个灰着的输入框，用户只会觉得功能坏了。 -->
            <UAlert
              v-if="refundableCents <= 0"
              color="info"
              variant="soft"
              icon="i-lucide-info"
              title="当前没有可退回的金额"
              description="只有通过支付宝充值、且尚未花掉的金额可以原路退回。管理员赠送的余额、以及打印失败退回余额的钱，都没有对应的支付记录，无法原路退回。"
            />

            <template v-else>
              <p class="text-sm text-muted">
                退款将<span class="text-default">原路退回</span>到你当初付款的支付宝账户，不能退到别处。
              </p>
              <div class="flex flex-wrap items-center gap-2">
                <UInput
                  type="number"
                  step="1"
                  min="1"
                  v-model="refundYuan"
                  class="w-32"
                  placeholder="退款金额"
                />
                <span class="text-sm text-muted">元</span>
                <UButton size="sm" variant="outline" @click="fillFullRefund">全部退款</UButton>
              </div>
            </template>

            <UAlert
              v-if="refundError"
              color="error"
              variant="soft"
              icon="i-lucide-x-circle"
              :title="refundError"
            />
            <div class="flex justify-end gap-2 pt-1">
              <UButton variant="ghost" :disabled="refunding" @click="showRefundModal = false">
                {{ refundableCents > 0 ? '取消' : '关闭' }}
              </UButton>
              <UButton
                v-if="refundableCents > 0"
                color="primary"
                :loading="refunding"
                :disabled="refunding || refundCents <= 0 || refundCents > refundableCents"
                @click="submitRefund"
              >确认退款 {{ formatCents(refundCents) }}</UButton>
            </div>
          </template>
        </div>
      </template>
    </UModal>

    <!-- 充值：这里只负责选金额，二维码在弹窗里 -->
    <UCard v-if="paymentMethods.length > 0">
      <template #header>
        <h2 class="text-lg font-bold flex items-center gap-2">
          <UIcon name="i-lucide-plus-circle" class="w-5 h-5" />
          充值
        </h2>
      </template>

      <div class="flex flex-wrap items-center gap-2">
        <UButton
          v-for="v in quickAmounts"
          :key="v"
          size="sm"
          :variant="amountYuan === String(v) ? 'soft' : 'outline'"
          :color="amountYuan === String(v) ? 'primary' : 'neutral'"
          @click="amountYuan = String(v)"
        >{{ v }} 元</UButton>
        <UInput type="number" step="1" min="1" v-model="amountYuan" class="w-28" placeholder="自定义" />
        <USelect
          v-if="paymentMethods.length > 1"
          v-model="provider"
          :items="providerItems"
          value-key="value"
          label-key="label"
          class="w-40"
        />
        <UButton
          color="primary"
          :loading="paying"
          :disabled="paying || amountCents <= 0"
          @click="startTopup"
        >充值 {{ formatCents(amountCents) }}</UButton>
      </div>
      <p class="text-xs text-muted mt-3">
        充值金额到账后进入余额，可用于支付打印费用。若打印失败，费用会全额退回余额。
      </p>

      <!-- 弹窗【外】的失败提示：卡片发起的请求失败了，用户得在这里看到原因，
           否则弹窗没打开、页面上什么都没发生，看起来像按钮坏了。 -->
      <UAlert v-if="topupError && !showTopupModal" class="mt-3" color="error" variant="soft" icon="i-lucide-x-circle" :title="topupError" />
    </UCard>

    <!-- 充值弹窗：二维码、倒计时、自动刷新、成功提示都在这里 -->
    <UModal v-model:open="showTopupModal" :ui="{ content: 'max-w-md' }">
      <template #content>
        <div class="p-6 space-y-3">
          <div class="flex items-center justify-between gap-2">
            <h3 class="text-lg font-semibold">{{ topupPaid ? '充值结果' : '扫码充值' }}</h3>
            <UButton
              v-if="!topupPaid"
              variant="ghost"
              size="xs"
              icon="i-lucide-x"
              @click="closeTopupModal"
            />
          </div>

          <PaymentPanel
            v-if="cred"
            :cred="cred"
            :busy="paying"
            :refreshing="refreshingQr"
            :error="qrError"
            :success="topupPaid"
            success-title="充值成功"
            :success-desc="`${formatCents(topupPaidCents)} 已到账。`"
            qr-alt="充值二维码"
            @refresh="refreshTopupQr"
            @sync="syncTopup()"
            @mock-pay="mockPayTopup"
            @cancel="closeTopupModal"
            @done="closeTopupModal"
          />

          <UAlert v-if="topupError && showTopupModal" color="error" variant="soft" icon="i-lucide-x-circle" :title="topupError" />
        </div>
      </template>
    </UModal>

    <!-- 订单 -->
    <UCard>
      <template #header>
        <h2 class="text-lg font-bold flex items-center gap-2">
          <UIcon name="i-lucide-receipt" class="w-5 h-5" />
          打印订单
        </h2>
      </template>

      <div v-if="loading" class="py-8 text-center text-muted">
        <UIcon name="i-lucide-loader-circle" class="w-6 h-6 animate-spin inline-block" />
        <div class="text-sm mt-2">加载中…</div>
      </div>
      <div v-else-if="orders.length === 0" class="py-8 text-center text-muted">
        <UIcon name="i-lucide-inbox" class="w-10 h-10 opacity-40" />
        <div class="text-sm mt-2">还没有订单</div>
      </div>
      <div v-else class="space-y-2">
        <div
          v-for="o in orders"
          :key="o.orderId"
          class="flex items-start justify-between gap-3 border border-default rounded-lg p-3"
        >
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2 flex-wrap">
              <span class="font-medium truncate">{{ o.filename }}</span>
              <UBadge :color="statusColor(o.status)" variant="subtle" size="sm">{{ statusText(o.status) }}</UBadge>
            </div>
            <div class="text-xs text-muted mt-1 flex flex-wrap gap-x-3">
              <span>{{ formatTime(o.createdAt) }}</span>
              <span>{{ o.billedPages }} 页<template v-if="o.copies > 1"> × {{ o.copies }} 份</template></span>
              <span v-if="o.jobId">任务 {{ o.jobId }}</span>
            </div>
            <div v-if="o.error" class="text-xs text-error mt-1">{{ o.error }}</div>
            <div v-if="o.refundedCents > 0" class="text-xs text-success mt-1">
              已退款 {{ formatCents(o.refundedCents) }}（退回余额）
            </div>
          </div>
          <div class="text-right shrink-0 font-medium">{{ formatCents(o.amountCents) }}</div>
        </div>
      </div>
    </UCard>

    <!-- 流水 -->
    <UCard>
      <template #header>
        <h2 class="text-lg font-bold flex items-center gap-2">
          <UIcon name="i-lucide-list" class="w-5 h-5" />
          余额流水
        </h2>
      </template>

      <div v-if="transactions.length === 0" class="py-6 text-center text-muted text-sm">暂无流水</div>
      <div v-else class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead>
            <tr class="text-left text-muted border-b border-default">
              <th class="py-2 pr-3 font-medium">时间</th>
              <th class="py-2 pr-3 font-medium">类型</th>
              <th class="py-2 pr-3 font-medium text-right">金额</th>
              <th class="py-2 pr-3 font-medium text-right">余额</th>
              <th class="py-2 font-medium">备注</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="t in transactions" :key="t.id" class="border-b border-default/50">
              <td class="py-2 pr-3 whitespace-nowrap text-muted">{{ formatTime(t.createdAt) }}</td>
              <td class="py-2 pr-3 whitespace-nowrap">{{ kindText(t.kind) }}</td>
              <td class="py-2 pr-3 text-right whitespace-nowrap" :class="t.amountCents < 0 ? 'text-error' : 'text-success'">
                {{ t.amountCents > 0 ? '+' : '' }}{{ formatCents(t.amountCents) }}
              </td>
              <td class="py-2 pr-3 text-right whitespace-nowrap text-muted">{{ formatCents(t.balanceAfterCents) }}</td>
              <td class="py-2 text-muted">{{ t.note || '—' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </UCard>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useRoute } from 'vue-router'
import { apiFetch, readError, readJSON } from '../utils/api'
import { formatCents, formatTime } from '../utils/format'
import { payChannel } from '../utils/device'
import PaymentPanel from '../components/PaymentPanel.vue'

const emit = defineEmits(['logout'])
const route = useRoute()

const loading = ref(true)
const wallet = ref({ balanceCents: 0, transactions: [], paymentMethods: [] })
const orders = ref([])

// ─── 充值 ───────────────────────────────────────────────
const amountYuan = ref('10')
const provider = ref('')
const paying = ref(false)
const cred = ref(null)
const topupError = ref('')
// 二维码在弹窗里，不再直接铺在页面上：金额选择留在卡片里（那是常驻内容），
// 而「正在扫码」是一个需要专注的临时状态，弹窗更合适。
const showTopupModal = ref(false)
const refreshingQr = ref(false)
const qrError = ref('')
// 到账后不立刻关弹窗，先给一个明确的成功反馈（PaymentPanel 负责倒计时）。
const topupPaid = ref(false)
const topupPaidCents = ref(0)

const quickAmounts = [5, 10, 20, 50]

const providerLabels = {
  mock: '模拟支付（仅测试）',
  alipay: '支付宝',
  wechat: '微信支付'
}

// ─── 退款（原路退回支付宝）─────────────────────────────
//
// 可退额度是服务端算的，前端只管展示与提交 —— 判断依据是「有没有对应的
// 渠道交易」，那需要查 payments 表，前端拿不到也不该自己猜。
const showRefundModal = ref(false)
const refundYuan = ref('')
const refunding = ref(false)
const refundError = ref('')
const refundDone = ref(false)
const refundDoneCents = ref(0)

const refundCents = computed(() => Math.round((Number(refundYuan.value) || 0) * 100))
const refundableCents = computed(() => wallet.value.refundableCents || 0)

function openRefund() {
  showRefundModal.value = true
  refundYuan.value = ''
  refundError.value = ''
  refundDone.value = false
}

// fillFullRefund 一键填满可退额度。
// 取 Math.floor 而不是四舍五入：宁可少退一分，也不要因为凑整而超额被拒。
function fillFullRefund() {
  refundYuan.value = String(Math.floor(refundableCents.value / 100))
}

async function submitRefund() {
  refunding.value = true
  refundError.value = ''
  try {
    const resp = await apiFetch('/api/wallet/refund', {
      method: 'POST',
      body: JSON.stringify({ amountCents: refundCents.value })
    })
    if (!resp.ok) {
      refundError.value = await readError(resp)
      return
    }
    const data = await readJSON(resp)
    refundDoneCents.value = data.refundedCents || refundCents.value
    refundDone.value = true
    await load()
  } finally {
    refunding.value = false
  }
}

const balanceCents = computed(() => wallet.value.balanceCents || 0)
const transactions = computed(() => wallet.value.transactions || [])
const paymentMethods = computed(() => wallet.value.paymentMethods || [])
const providerItems = computed(() =>
  paymentMethods.value.map((m) => ({ label: providerLabels[m] || m, value: m }))
)
// 元 → 分。先 Math.round 避开浮点尾巴（0.1*100 = 10.000000000000002）。
const amountCents = computed(() => Math.round((Number(amountYuan.value) || 0) * 100))

async function load() {
  loading.value = true
  try {
    const [walletResp, ordersResp] = await Promise.all([
      apiFetch('/api/wallet', {}, () => emit('logout')),
      apiFetch('/api/orders', {}, () => emit('logout'))
    ])
    if (walletResp.ok) {
      wallet.value = await readJSON(walletResp)
      if (!provider.value && paymentMethods.value.length > 0) {
        provider.value = paymentMethods.value[0]
      }
    }
    if (ordersResp.ok) {
      const data = await readJSON(ordersResp)
      orders.value = Array.isArray(data) ? data : []
    }
  } finally {
    loading.value = false
  }
}

// ⚠️ 下面这几个函数刻意【不带可选参数】。
//
// 模板里写 @click="startTopup" 时，Vue 会把 MouseEvent 作为第一个实参传进来。
// 若函数签名是 startTopup(silent = false)，那个事件对象就是真值 ——
// 手动点击会悄无声息地退化成「自动刷新」模式：不弹错误、不打日志。
// 实测踩过两次（syncTopup 与 startTopup 各一次）。
//
// 所以公开入口一律无参，静默行为拆成独立的内部函数 runTopup / runSyncTopup，
// 只能被代码显式调用。以后加参数也请沿用这个形状。

// startTopup 发起充值并打开二维码弹窗。
async function startTopup() {
  await runTopup(false)
}

// refreshTopupQr 换一张二维码。
//
// 服务端每次调用都会新建一笔支付单（并关掉上一笔），
// 所以「刷新二维码」与「首次发起支付」在协议上是同一个动作。
async function refreshTopupQr() {
  if (refreshingQr.value) return
  refreshingQr.value = true
  try {
    await runTopup(true)
  } finally {
    refreshingQr.value = false
  }
}

// runTopup 是充值的真正实现。
//
// silent=true 用于二维码到期后的自动刷新：失败时不弹错误条，
// 只把原因交给面板显示「刷新二维码」按钮 —— 自动流程反复弹红字
// 会让人以为整个支付坏了，其实多半只是一次网络抖动。
async function runTopup(silent) {
  if (!silent) { paying.value = true; topupError.value = '' }
  try {
    const resp = await apiFetch('/api/wallet/topup', {
      method: 'POST',
      body: JSON.stringify({
        amountCents: amountCents.value,
        provider: provider.value,
        // 收款方式按设备分：手机跳支付宝收银台，电脑给二维码。
        // 服务端拿不到回跳地址时会自己降级成二维码，前端不必兜。
        channel: payChannel(),
        // 付完回到当前这一页 —— 手机网站支付会由支付宝把浏览器送回这儿。
        returnPath: route.path
      })
    })
    if (!resp.ok) {
      const msg = await readError(resp)
      if (silent) qrError.value = msg
      else topupError.value = msg
      return
    }
    cred.value = await readJSON(resp)
    topupPaidCents.value = cred.value.amountCents || 0
    qrError.value = ''
    if (!silent) {
      topupPaid.value = false
      showTopupModal.value = true
    }
  } finally {
    if (!silent) paying.value = false
  }
}

// closeTopupModal 关闭弹窗并清空支付状态。
//
// ⚠️ 只有【用户主动关闭】才走到这里。到账成功后是 PaymentPanel 的
// 5 秒倒计时触发的 done，那时钱已经进了余额 —— 关窗只是收尾。
function closeTopupModal() {
  showTopupModal.value = false
  cred.value = null
  topupPaid.value = false
  topupError.value = ''
  qrError.value = ''
  refreshingQr.value = false
  stopTopupPolling()
  load()
}

// 模拟渠道到账。服务端走的是与真实回调【完全相同】的结算内核。
async function mockPayTopup() {
  if (!cred.value?.outTradeNo) return
  paying.value = true
  try {
    const resp = await apiFetch(`/api/payments/mock/${cred.value.outTradeNo}/pay`, { method: 'POST' })
    if (!resp.ok) {
      topupError.value = await readError(resp)
      return
    }
    await finishTopup()
  } finally {
    paying.value = false
  }
}

// 主动查单：内网部署收不到渠道回调时，这是唯一的结算途径。
// 无参入口，理由见上面 startTopup 处的说明。
async function syncTopup() {
  await runSyncTopup(false)
}

// runSyncTopup 是查单的真正实现。
//
// silent=true 用于后台自动轮询：失败时【不写 topupError】。
// 自动轮询遇到网络抖动是常态，把红色错误糊在用户脸上只会造成恐慌，
// 下一轮成功就自动消失了 —— 手动点击时才需要即时反馈。
async function runSyncTopup(silent) {
  const no = cred.value?.outTradeNo
  if (!no) return
  if (!silent) paying.value = true
  try {
    const resp = await apiFetch(`/api/payments/${no}/sync`, { method: 'POST' })
    if (!resp.ok) {
      if (!silent) topupError.value = await readError(resp)
      return
    }
    const data = await readJSON(resp)
    // 期间用户可能已经取消、换了金额重新发起 —— 丢弃过期结果。
    if (cred.value?.outTradeNo !== no) return
    if (data.paid) {
      await finishTopup()
    } else if (!silent) {
      // 「还没付」是预期状态，不是故障：提示一下就走，别让它一直挂着。
      topupError.value = data.message || '尚未收到支付结果，请稍后再试'
      clearTopupErrorLater()
    } else {
      // 自动轮询下把上一轮的提示清掉，免得用户扫完码后界面上
      // 还挂着一条「尚未收到支付结果」。
      topupError.value = ''
    }
  } finally {
    if (!silent) paying.value = false
  }
}

// 卡片里的错误提示自动消失，理由与支付弹窗相同：一句永远不走的
// 「尚未收到支付结果」既像故障，又挡住后面的状态变化。
let topupErrorTimer = null
function clearTopupErrorLater(ms = 4000) {
  if (topupErrorTimer) clearTimeout(topupErrorTimer)
  topupErrorTimer = setTimeout(() => { topupError.value = '' }, ms)
}
onUnmounted(() => { if (topupErrorTimer) clearTimeout(topupErrorTimer) })

// 结算完成后重新拉取：以服务端返回的真实余额与流水为准，不做乐观更新 ——
// 钱的事情上，"界面显示到账了但服务端没记上"是最不能接受的。
//
// 现在它【不再直接关弹窗】：先把 topupPaid 置真，让 PaymentPanel 显示
// 「充值成功」并走 5 秒倒计时，用户也能自己点「返回」提前走。
async function finishTopup() {
  stopTopupPolling()
  topupPaid.value = true
  await load()
}

// 二维码展示期间的自动查单。
//
// 没有它的话，用户扫完码要等到后台巡检（每 30 秒）才会到账，
// 中间这段时间界面一动不动，看起来像卡住了。
// 5 秒的间隔足够快（扫完基本立刻反应过来），又不会把查单接口打爆。
let topupPollTimer = null
function startTopupPolling() {
  stopTopupPolling()
  topupPollTimer = setInterval(() => { runSyncTopup(true) }, 5000)
}
function stopTopupPolling() {
  if (topupPollTimer) { clearInterval(topupPollTimer); topupPollTimer = null }
}

// 弹窗打开且还没到账时才开始轮询。
watch([showTopupModal, topupPaid, cred], () => {
  if (showTopupModal.value && !topupPaid.value && cred.value) startTopupPolling()
  else stopTopupPolling()
})

// ── 从支付宝切回来时立刻查一次 ──
//
// 与打印支付弹窗同一套：手机浏览器会冻结后台标签页的定时器，
// 用户跳去支付宝付完再切回来时，5 秒轮询可能早就停了。
// 不主动补一次，用户会以为充值没到账。
function onPageResumed() {
  if (document.visibilityState !== 'visible') return
  if (!showTopupModal.value || topupPaid.value || !cred.value) return
  runSyncTopup(true)
}

onMounted(() => {
  document.addEventListener('visibilitychange', onPageResumed)
  window.addEventListener('focus', onPageResumed)
  window.addEventListener('pageshow', onPageResumed)
})

onUnmounted(() => {
  document.removeEventListener('visibilitychange', onPageResumed)
  window.removeEventListener('focus', onPageResumed)
  window.removeEventListener('pageshow', onPageResumed)
  stopTopupPolling()
})

onMounted(load)

// ⚠️ refund 与 withdraw 是两件不同的事，文案上必须分开：
//   refund   = 打印失败，钱退回【余额】（钱没离开系统）
//   withdraw = 余额原路退回支付宝（钱真的出去了）
// 都写成「退款」的话，用户对不上账。
const kindText = (k) => ({
  topup: '充值',
  spend: '打印扣费',
  refund: '退回余额',
  withdraw: '原路退款',
  withdraw_rollback: '退款失败回滚',
  admin_adjust: '管理员调整'
}[k] || k)

const statusColor = (s) => ({
  pending_payment: 'warning',
  paid: 'info',
  printing: 'info',
  printed: 'success',
  failed: 'error',
  refunded: 'neutral',
  expired: 'neutral',
  cancelled: 'neutral'
}[s] || 'neutral')

const statusText = (s) => ({
  pending_payment: '待支付',
  paid: '已支付',
  printing: '打印中',
  printed: '已打印',
  failed: '失败',
  refunded: '已退款',
  expired: '已过期',
  cancelled: '已取消'
}[s] || s)
</script>
