<template>
  <UModal v-model:open="open" :ui="{ content: 'max-w-lg' }">
    <template #content>
      <div class="flex flex-col max-h-[85vh]">
        <!-- 头部 -->
        <div class="p-6 pb-3 border-b border-default shrink-0">
          <h3 class="text-lg font-semibold">{{ done ? '支付结果' : '确认支付' }}</h3>
          <p class="text-sm text-muted mt-1">
            共 {{ orders.length }} 个文件，合计
            <span class="font-semibold text-primary">{{ formatCents(totalAmountCents) }}</span>
          </p>
        </div>

        <!-- 滚动体 -->
        <div class="flex-1 overflow-y-auto p-6 space-y-4">
          <div class="space-y-2">
            <div
              v-for="o in orders"
              :key="o.orderId"
              class="flex items-start justify-between gap-3 text-sm border border-default rounded-lg p-2.5"
            >
              <div class="min-w-0 flex-1">
                <div class="truncate font-medium">{{ o.filename }}</div>
                <div class="text-xs text-muted mt-0.5">
                  {{ formatCents(o.unitPriceCents) }}/页 × {{ o.billedPages }} 页<template v-if="o.copies > 1"> × {{ o.copies }} 份</template>
                </div>
                <div v-if="o.status === 'failed'" class="text-xs text-error mt-1">
                  {{ o.error || '打印失败' }}
                  <template v-if="o.refundedCents > 0"> · 已退款 {{ formatCents(o.refundedCents) }}</template>
                </div>
                <div v-else-if="o.status === 'printed'" class="text-xs text-success mt-1">
                  已提交打印{{ o.jobId ? `（任务 ${o.jobId}）` : '' }}
                </div>
                <div v-else-if="o.status === 'expired'" class="text-xs text-muted mt-1">已过期</div>
              </div>
              <div class="text-right shrink-0">
                <div class="font-medium">{{ formatCents(o.amountCents) }}</div>
                <UBadge :color="statusColor(o.status)" variant="subtle" size="sm">{{ statusText(o.status) }}</UBadge>
              </div>
            </div>
          </div>

          <!-- 待支付：选支付方式 -->
          <div v-if="!done && !onlineCred" class="space-y-3">
            <div class="text-sm font-medium">支付方式</div>
            <div class="grid grid-cols-1 gap-2">
              <label
                v-for="m in methodOptions"
                :key="m.value"
                class="flex items-center gap-3 border rounded-lg p-3 cursor-pointer transition-colors"
                :class="method === m.value ? 'border-primary bg-primary/5' : 'border-default hover:bg-elevated/50'"
              >
                <input v-model="method" type="radio" :value="m.value" class="accent-primary" />
                <PayIcon :provider="m.value" />
                <div class="flex-1">
                  <div class="text-sm font-medium">{{ m.label }}</div>
                  <div class="text-xs" :class="m.value === 'balance' && insufficient ? 'text-error' : 'text-muted'">
                    {{ m.hint }}
                  </div>
                </div>
                <UButton
                  v-if="m.value === 'balance' && insufficient"
                  size="xs"
                  color="primary"
                  variant="soft"
                  @click.prevent="emit('go-wallet')"
                >去充值</UButton>
              </label>
            </div>
            <!-- 余额不足时把【出路】写出来。
                 在线渠道本来就不需要余额，用户没理由先去充值再回来付 ——
                 但不说这一句，他看到禁用掉的「确认支付」只会以为整个支付坏了。 -->
            <UAlert
              v-if="method === 'balance' && insufficient"
              color="warning"
              variant="soft"
              icon="i-lucide-wallet"
              title="余额不足"
              description="可以点「去充值」入账后再付，也可以直接改选上面的在线支付方式（如支付宝），那条路不需要余额。"
            />
            <p class="text-xs text-muted">
              支付成功后立即送印。若打印失败，费用会<b>全额退回余额</b>——在线支付渠道的原路退款需人工处理，因此统一退到余额。
            </p>
          </div>

          <!-- 支付成功：钱一到账就展示，不等打印完成。
               打印进度只反映在文案上，用户不必盯着二维码干等。 -->
          <PaymentPanel
            v-if="showSuccess"
            success
            :success-title="printDone ? '打印完成' : '支付成功'"
            :success-desc="printDone
              ? '打印任务已完成。'
              : '正在打印，进度可在「打印记录」中查看。'"
            @done="onSuccessDone"
          />

          <!-- 已发起在线支付：等待到账（二维码 / 倒计时 / 自动刷新） -->
          <PaymentPanel
            v-else-if="!done && onlineCred"
            :cred="onlineCred"
            :provider="method"
            :busy="paying"
            :refreshing="refreshingQr"
            :error="qrError"
            @refresh="refreshQr"
            @sync="syncPayment"
            @mock-pay="mockPay"
            @cancel="onlineCred = null"
          />

          <!-- 已支付但打印没成：保留明细，让用户看清失败原因与退款 -->
          <template v-else>
            <div v-if="refundedCents > 0" class="text-sm text-muted border-t border-default pt-3">
              本次已退款 <span class="text-success font-medium">{{ formatCents(refundedCents) }}</span>，已退回余额。
            </div>
            <div class="text-xs text-muted">当前余额 {{ formatCents(balanceCents) }}</div>
          </template>

          <UAlert v-if="errorMsg" color="error" variant="soft" icon="i-lucide-x-circle" :title="errorMsg" />
        </div>

        <!-- 尾部 -->
        <div class="flex justify-end gap-2 p-6 pt-3 border-t border-default shrink-0">
          <template v-if="showSuccess">
            <!-- 成功页自带「返回」按钮，这里不再重复 -->
          </template>
          <template v-else-if="!done">
            <UButton variant="ghost" :disabled="paying" @click="onCancelClick">
              {{ allowCancel ? '取消' : '关闭' }}
            </UButton>
            <UButton
              v-if="!onlineCred"
              color="primary"
              :loading="paying"
              :disabled="confirmDisabled"
              @click="onConfirm"
            >确认支付 {{ formatCents(totalAmountCents) }}</UButton>
          </template>
          <template v-else>
            <UButton variant="ghost" @click="emit('go-wallet')">查看钱包</UButton>
            <UButton color="primary" @click="close">完成</UButton>
          </template>
        </div>
      </div>
    </template>
  </UModal>
</template>

<script setup>
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import { apiFetch, readError, readJSON } from '../../utils/api'
import { formatCents, orderStatusColor as statusColor, orderStatusText as statusText } from '../../utils/format'
import { payChannel } from '../../utils/device'
import PaymentPanel from '../PaymentPanel.vue'
import PayIcon from '../PayIcon.vue'

const props = defineProps({
  // 下单响应：{ batchId, orders, totalAmountCents, balanceCents, expiresAt }
  batch: { type: Object, default: null },
  // 可用的在线渠道，如 ['mock']；为空时只提供余额支付
  paymentMethods: { type: Array, default: () => [] },
  // 点「取消」时是否顺带【作废】这一批订单。
  //
  // 刚下完单就弹出这个窗时（打印页、复印页），取消 = 反悔不打，
  // 顺手把订单作废、文件回收，是对的。
  //
  // 但从「我的钱包 → 打印订单」进来补付时不是：用户点「取消」多半只是想
  // 关掉弹窗（比如先去充值，或者先去看一眼余额）。这里若沿用同一条路，
  // 他点一下「取消」就把订单连同扫描件一起作废了 —— 而他本来是想付钱的。
  // 所以那个入口传 false，按钮文案也随之变成「关闭」。
  allowCancel: { type: Boolean, default: true }
})

const open = defineModel('open', { type: Boolean, default: false })
const emit = defineEmits(['paid', 'go-wallet', 'done'])

const toast = useToast()
const route = useRoute()

const method = ref('balance')
const paying = ref(false)
const done = ref(false)
const errorMsg = ref('')
const status = ref(null)
// 已发起在线支付后拿到的渠道凭据；非空表示进入「等待到账」阶段。
const onlineCred = ref(null)
// 二维码刷新中（PaymentPanel 据此不重复触发刷新）
const refreshingQr = ref(false)
// 二维码刷新失败的原因；非空时面板切成「刷新二维码」按钮
const qrError = ref('')
let pollTimer = null

const orders = computed(() => status.value?.orders || props.batch?.orders || [])
const totalAmountCents = computed(() => status.value?.totalAmountCents ?? props.batch?.totalAmountCents ?? 0)
const balanceCents = computed(() => status.value?.balanceCents ?? props.batch?.balanceCents ?? 0)
const refundedCents = computed(() => status.value?.refundedCents ?? 0)
const insufficient = computed(() => balanceCents.value < totalAmountCents.value)

// ⚠️ 余额不足只该挡住【余额支付】这一条路。
//
// 早先写的是 `paying || insufficient`，于是余额为 0 的账号（比如管理员自己）
// 选中支付宝之后，「确认支付」仍然是禁用的：鼠标落在上面是个红色禁止标，
// 而界面上没有一个字解释为什么。用户唯一能想到的结论是「支付宝这条路坏了」，
// 实际上钱根本还没走到支付宝那一步。真机实测就是这么卡住的。
const confirmDisabled = computed(() =>
  paying.value || (method.value === 'balance' && insufficient.value)
)

// ── 「钱到了」与「纸出来了」是两件事，必须分开 ──
//
// ⚠️ 早先把成功页绑在 `status === 'printed'` 上，结果是：用户付完款还要
// 盯着二维码等打印机把纸打完（实测等了近一分钟）才看到「支付成功」。
// 付款是即时到账的，反馈也必须是即时的 —— 打印进度是另一回事。
//
// 所以：批次【离开 pending_payment】就说明钱到了（expired/cancelled
// 是没付款的终态，不算）。一旦到账就锁定，后续打印结果只影响文案，
// 不会把已经展示的「支付成功」再翻回去。
const paymentSettled = ref(false)

// 打印确实失败/部分失败：这时候不能报「支付成功」，要给用户看原因与退款。
const printFailed = computed(() => done.value &&
  (status.value?.status === 'failed' || status.value?.status === 'partial'))

watch(
  () => status.value?.status,
  (s) => {
    // processing = 有订单已支付或正在打印；printed = 全部打完。
    // 两者都意味着钱已经到账。
    if (s === 'processing' || s === 'printed') paymentSettled.value = true
  }
)

const showSuccess = computed(() => paymentSettled.value && !printFailed.value)

// 成功页的文案随打印进度演进：到账时先报「支付成功」，
// 打完再补一句「打印完成」，让用户知道纸出来了。
const printDone = computed(() => status.value?.status === 'printed')

// ⚠️ 报错文案会自动消失。
//
// 原来 errorMsg 一旦写上就再也不会清掉，用户点了「我已支付」看到
// 「尚未收到支付结果」之后，那句话会一直挂在弹窗底部直到他关掉 ——
// 既容易让人以为界面坏了，也挡住后面的状态变化。
// 给一个明确的存续时间，够看清就行。
let errorTimer = null
function flashError(msg, ms = 4000) {
  errorMsg.value = msg
  if (errorTimer) clearTimeout(errorTimer)
  errorTimer = setTimeout(() => { errorMsg.value = '' }, ms)
}
function clearError() {
  if (errorTimer) { clearTimeout(errorTimer); errorTimer = null }
  errorMsg.value = ''
}
onUnmounted(() => { if (errorTimer) clearTimeout(errorTimer) })

const providerLabels = {
  mock: '模拟支付（仅测试）',
  alipay: '支付宝',
  wechat: '微信支付'
}
const methodOptions = computed(() => {
  const opts = [{
    value: 'balance',
    label: '余额支付',
    hint: `当前余额 ${formatCents(balanceCents.value)}`
  }]
  for (const m of props.paymentMethods) {
    opts.push({
      value: m,
      label: providerLabels[m] || m,
      hint: '跳转渠道完成支付，到账后自动开始打印'
    })
  }
  return opts
})

watch(open, (v) => {
  if (v) {
    done.value = false
    clearError()
    status.value = null
    onlineCred.value = null
    refreshingQr.value = false
    qrError.value = ''
    method.value = 'balance'
    // ⚠️ 必须一起复位，这是个「同一个组件实例连着开两次」的坑：
    // 组件在父组件里是常驻的（只切 open），付完一次之后 paymentSettled
    // 会一直停在 true —— 再打开一单，showSuccess 立刻为真，
    // 用户看到的是上一单的「支付成功」，连支付方式都挑不了。
    paymentSettled.value = false
  } else {
    stopPolling()
  }
})

onUnmounted(stopPolling)

function stopPolling() {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

const TERMINAL = ['printed', 'partial', 'failed', 'expired', 'cancelled']

async function fetchStatus() {
  if (!props.batch?.batchId) return null
  // ⚠️ 不要在这里传 `() => {}` 把 401 处理关掉。
  //
  // 那样会话过期时轮询会一直失败下去，弹窗永远停在「支付处理中」，
  // 每 2 秒打一发注定失败的请求，用户只能关窗重来。
  // 交给全局兜底（utils/api.js 的 setUnauthorizedHandler）把用户送去重新登录。
  const resp = await apiFetch(`/api/orders/batch/${props.batch.batchId}`)
  if (!resp.ok) return null
  return readJSON(resp)
}

// 余额支付：后端在同一事务里扣款 + 标记订单已支付，然后异步送印。
async function pay() {
  paying.value = true
  clearError()
  try {
    const resp = await apiFetch(
      `/api/orders/batch/${props.batch.batchId}/pay`,
      { method: 'POST', body: JSON.stringify({ method: 'balance' }) }
    )
    if (!resp.ok) {
      flashError(await readError(resp))
      return
    }
    done.value = true
    emit('paid')
    startPolling()
  } finally {
    paying.value = false
  }
}

// 在线支付：只拿凭据，钱还没到；到账由渠道回调或主动查单触发。
//
// silent=true 用于二维码到期后的自动刷新：失败时不弹错误条、只把
// qrError 交给面板显示「刷新二维码」按钮。自动流程反复弹红字会让人
// 以为整个支付坏了，而其实只是一次网络抖动。
async function payOnline(silent = false) {
  if (!silent) paying.value = true
  if (!silent) clearError()
  try {
    const resp = await apiFetch(
      `/api/orders/batch/${props.batch.batchId}/pay`,
      {
        method: 'POST',
        body: JSON.stringify({
          method: method.value,
          // 收款方式按设备分：手机跳支付宝收银台，电脑给二维码。
          // 服务端拿不到回跳地址时会自己降级成二维码，前端不必兜。
          channel: payChannel(),
          // 付完回到当前这一页 —— 手机网站支付会由支付宝把浏览器送回这儿。
          returnPath: route.path
        })
      }
    )
    if (!resp.ok) {
      const msg = await readError(resp)
      if (silent) qrError.value = msg
      else flashError(msg)
      return
    }
    onlineCred.value = await readJSON(resp)
    qrError.value = ''
    emit('paid')
  } finally {
    if (!silent) paying.value = false
  }
}

// 二维码到期后换一张。
//
// 服务端每次调用都会新建一笔支付单（并关掉上一笔），所以这里直接复用
// payOnline —— 刷新二维码和首次发起支付在协议上是同一个动作。
async function refreshQr() {
  if (refreshingQr.value) return
  refreshingQr.value = true
  try {
    await payOnline(true)
  } finally {
    refreshingQr.value = false
  }
}

function onConfirm() {
  if (method.value === 'balance') return pay()
  return payOnline()
}

// 模拟渠道到账。走的是与真实回调【完全相同】的服务端结算内核。
async function mockPay() {
  if (!onlineCred.value?.outTradeNo) return
  paying.value = true
  try {
    const resp = await apiFetch(
      `/api/payments/mock/${onlineCred.value.outTradeNo}/pay`,
      { method: 'POST' }
    )
    if (!resp.ok) {
      flashError(await readError(resp))
      return
    }
    done.value = true
    startPolling()
  } finally {
    paying.value = false
  }
}

// 主动查单：内网部署收不到渠道回调时，这是唯一的结算途径。
// 无参入口，理由见上面 payOnline 处的说明。
async function syncPayment() {
  await runSyncPayment(false)
}

// runSyncPayment 是查单的真正实现。
//
// silent=true 用于「从支付宝切回来」这种自动补查：用户此时刚付完，
// 查不到是正常的（渠道有延迟）。弹一句「尚未收到支付结果」只会让他以为
// 支付失败了 —— 而这个提示跟手动点按钮时的含义完全不同。
async function runSyncPayment(silent) {
  if (!onlineCred.value?.outTradeNo) return
  if (!silent) paying.value = true
  try {
    const resp = await apiFetch(
      `/api/payments/${onlineCred.value.outTradeNo}/sync`,
      { method: 'POST' }
    )
    if (!resp.ok) {
      if (!silent) flashError(await readError(resp))
      return
    }
    const data = await readJSON(resp)
    if (data.paid) {
      done.value = true
      startPolling()
    } else if (!silent) {
      // 「还没付」是预期状态，不是故障：给一句提示就走，让它自己消失。
      flashError(data.message || '尚未收到支付结果，请稍后再试', 3000)
    }
  } finally {
    paying.value = false
  }
}

// ⚠️ 拿到在线支付凭据就【自动】开始轮询批次状态。
//
// 这不是可有可无的：后端的「按需查单」是挂在这个轮询上的 ——
// 前端的 GET /api/orders/batch/{id} 会让服务端顺带问一次渠道。
// 没人轮询，就没人去问，用户扫完码后界面永远不会自己变，
// 只能手动点「我已支付」。
//
// 之前是在 payOnline 里手动调的，漏了一次就出了这个 bug。
// 改成监听 onlineCred：以后任何新路径只要设了凭据就自动接上，
// 不需要记得再补一行 startPolling()。
// 双向：拿到凭据开始轮询，用户点「换个方式」清空凭据就停下。
// （余额支付那条路从不碰 onlineCred，所以不会被这个 watch 干扰。）
watch(onlineCred, (v) => {
  if (v) startPolling()
  else stopPolling()
})

// ── 从支付宝切回来时立刻查一次 ──
//
// ⚠️ 手机浏览器会【冻结后台标签页的定时器】：用户点「去支付宝付款」跳走、
// 付完再切回来时，那个 2 秒的轮询可能已经停了几分钟。不主动补一次的话，
// 用户看到的是「我明明付了钱，页面却毫无反应」—— 而这恰恰是最不能出错的时刻。
//
// 三个事件都要听，覆盖不同浏览器的恢复路径：
//   visibilitychange  标签页切回前台（Chrome/Safari 主力）
//   focus             窗口重新获得焦点（桌面浏览器）
//   pageshow          bfcache 恢复（iOS Safari 返回时走这条）
function onPageResumed() {
  if (document.visibilityState !== 'visible') return
  if (!onlineCred.value) return // 没在等支付就不用管
  syncPayment()
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
})


function startPolling() {
  stopPolling()
  const tick = async () => {
    const s = await fetchStatus()
    if (!s) return
    status.value = s
    if (TERMINAL.includes(s.status)) {
      stopPolling()
      // 在这里统一置 done：在线支付路径下 done 之前一直是 false
      // （钱还没付），批次走到终态才该推进界面。
      // 余额支付的调用方已经置过了，这里是幂等的。
      done.value = true
      emit('paid')
      if (s.status === 'printed') {
        toast.add({ title: '打印任务已提交', color: 'success', icon: 'i-lucide-check-circle' })
      } else if (s.status === 'partial') {
        toast.add({ title: '部分完成', description: '有文件打印失败，费用已退回余额', color: 'warning', icon: 'i-lucide-alert-triangle' })
      } else if (s.status === 'failed') {
        toast.add({ title: '打印失败', description: '费用已全额退回余额', color: 'error', icon: 'i-lucide-x-circle' })
      }
    }
  }
  tick()
  pollTimer = setInterval(tick, 2000)
}

async function closeAndCancel() {
  if (props.batch?.batchId) {
    try {
      await apiFetch(`/api/orders/batch/${props.batch.batchId}/cancel`, { method: 'POST' })
    } catch {
      // 取消失败不影响关闭弹窗 —— 后台的过期回收会兜底删掉文件。
    }
    emit('paid')
  }
  open.value = false
}

// 「取消」按钮的两副面孔：作废订单并关闭，或者只关闭。见 allowCancel 的说明。
function onCancelClick() {
  if (props.allowCancel) return closeAndCancel()
  close()
}

function close() {
  open.value = false
}

// 成功页结束（5 秒倒计时走完，或用户点了「返回」）。
//
// ⚠️ 与 close() 分开，不要合并：close() 还被「完成」按钮（打印失败那条分支）
// 和取消流程调用，那些情况不该进中间页。只有成功页结束才 emit('done')，
// 由父组件带用户去中间页收尾。
function onSuccessDone() {
  close()
  emit('done')
}

// 订单状态的文案与配色统一放在 utils/format（orderStatus*）：
// 中间页 ResultView 也要用同一份，各写一份的话用户可见文案迟早漂。
// ⚠️ 别改用 statusColor/statusText —— 那两个映射的是【打印记录】的状态。
</script>
