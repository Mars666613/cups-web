<template>
  <div class="p-3 sm:p-4 md:p-6 max-w-5xl mx-auto space-y-4">
    <div class="flex items-center justify-between gap-2 flex-wrap">
      <h1 class="text-lg font-bold flex items-center gap-2">
        <UIcon name="i-lucide-wallet" class="w-5 h-5 text-primary" />
        我的钱包
      </h1>
      <UButton variant="ghost" size="xs" icon="i-lucide-refresh" :loading="loading" @click="load">刷新</UButton>
    </div>

    <!-- 加载失败必须显眼：下面的余额卡片在失败时会显示成 ¥0.00，
         用户会以为钱丢了。这条提示要盖在它上面说清楚「这不是你的余额」。 -->
    <UAlert
      v-if="loadError"
      color="error"
      variant="soft"
      icon="i-lucide-x-circle"
      title="加载失败，下面的余额与订单可能不是最新的"
      :description="loadError"
    />

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
                <!-- step / min 与「按分记账」对齐：步进器一次一分，而不是一次一元。
                     ⚠️ 别把这两个属性当成拦路虎 —— 它们【只】影响步进器与浏览器的
                     校验 API，拦不住用户直接输入的 0.01。真正把那一分吞掉的是
                     fillFullRefund 里的 Math.floor，见下面的注释。 -->
                <UInput
                  type="number"
                  step="0.01"
                  min="0.01"
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
        <!-- 充值也按分记账：后端只校验 > 0 且 ≤ 100 万元，并没有 1 元起充。
             原先写 step="1" min="1" 与退款那处同源，都是在声明一个程序里
             并不存在的约束（也拦不住什么，只是步进器会整元跳）。 -->
        <UInput type="number" step="0.01" min="0.01" v-model="amountYuan" class="w-28" placeholder="自定义" />
        <!-- 渠道下拉带渠道 logo：这台机器上可能同时开着支付宝和微信，
             只有文字的话用户得逐字读完才知道选的是哪个。
             `leading` 管【收起时】显示的那个，`item-leading` 管展开后的每一项，
             两个都要给 —— 只给一个的话总有一边是光秃秃的文字。 -->
        <USelect
          v-if="paymentMethods.length > 1"
          v-model="provider"
          :items="providerItems"
          value-key="value"
          label-key="label"
          class="w-40"
        >
          <template #leading>
            <PayIcon :provider="provider" size="w-4 h-4" />
          </template>
          <template #item-leading="{ item }">
            <PayIcon :provider="item.value" size="w-4 h-4" />
          </template>
        </USelect>
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
            :provider="provider"
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
        <div class="flex items-center justify-between gap-2 cursor-pointer select-none" @click="ordersExpanded = !ordersExpanded">
          <h2 class="text-lg font-bold flex items-center gap-2 min-w-0">
            <UIcon name="i-lucide-receipt" class="w-5 h-5 shrink-0" />
            打印订单
            <!-- 折叠时补一行条数：收起之后也得能看出里面有没有东西，
                 否则用户只能靠展开来确认，等于没省事。 -->
            <span v-if="!ordersExpanded && orders.length > 0" class="text-xs font-normal text-muted truncate">
              共 {{ orders.length }} 笔
            </span>
          </h2>
          <UIcon
            :name="ordersExpanded ? 'i-lucide-chevron-down' : 'i-lucide-chevron-right'"
            class="w-4 h-4 text-muted transition-transform duration-200 shrink-0"
          />
        </div>
      </template>

      <div
        class="transition-all duration-300 ease-in-out overflow-hidden"
        :style="{ maxHeight: ordersExpanded ? listMaxHeight : '0px', visibility: ordersExpanded ? 'visible' : 'hidden' }"
      >
        <div :style="{ maxHeight: listMaxHeight }" class="overflow-y-auto">
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
                  <!-- 支付方式：用户对账时最先要找的就是「这笔是从哪扣的」。
                       未付款的订单 payMethod 是空的，标签整个不渲染。 -->
                  <span v-if="payMethodText(o.payMethod)" class="inline-flex items-center gap-1">
                    <PayIcon :provider="o.payMethod" size="w-3.5 h-3.5" />
                    {{ payMethodText(o.payMethod) }}
                  </span>
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
        </div>
      </div>
    </UCard>

    <!-- 流水 -->
    <UCard>
      <template #header>
        <div class="flex items-center justify-between gap-2 cursor-pointer select-none" @click="txExpanded = !txExpanded">
          <h2 class="text-lg font-bold flex items-center gap-2 min-w-0">
            <UIcon name="i-lucide-list" class="w-5 h-5 shrink-0" />
            余额流水
            <span v-if="!txExpanded && transactions.length > 0" class="text-xs font-normal text-muted truncate">
              共 {{ transactions.length }} 条
            </span>
          </h2>
          <UIcon
            :name="txExpanded ? 'i-lucide-chevron-down' : 'i-lucide-chevron-right'"
            class="w-4 h-4 text-muted transition-transform duration-200 shrink-0"
          />
        </div>
      </template>

      <div
        class="transition-all duration-300 ease-in-out overflow-hidden"
        :style="{ maxHeight: txExpanded ? listMaxHeight : '0px', visibility: txExpanded ? 'visible' : 'hidden' }"
      >
        <div :style="{ maxHeight: listMaxHeight }" class="overflow-y-auto">
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
        </div>
      </div>
    </UCard>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useRoute } from 'vue-router'
import { apiFetch, readError, readJSON } from '../utils/api'
import { paymentSettledTick } from '../utils/paymentSignal'
import {
  formatCents, formatTime, payMethodText,
  // ⚠️ 订单状态要用 orderStatus*，不是 status*（后者是【打印记录】的状态）
  orderStatusColor as statusColor,
  orderStatusText as statusText
} from '../utils/format'
import { payChannel } from '../utils/device'
import PaymentPanel from '../components/PaymentPanel.vue'
import PayIcon from '../components/PayIcon.vue'

const emit = defineEmits(['logout'])
const route = useRoute()

const loading = ref(true)
// 拉取失败的原因。非空时页面顶部弹一条错误 —— 因为下面的卡片在失败时
// 会把余额显示成 ¥0.00（wallet 的初值），那是在把「取不到」谎报成「零」。
const loadError = ref('')
const wallet = ref({ balanceCents: 0, transactions: [], paymentMethods: [] })

// ── 两个长列表默认收起 ────────────────────────────────────────────
//
// 订单最多 100 条、流水最多 50 条（服务端上限），全展开往下滚要划很久；
// 而钱包页真正要一眼看到的是【余额】和【充值按钮】，它们在页面最上面。
// 收起态在标题旁留一行条数摘要，展开后限高并内部滚动 ——
// 列表再长也撑不破页面。
//
// 折叠动画与标题样式照抄 PrintRecordList（同一套写法，改的时候对照着改）。
const ordersExpanded = ref(false)
const txExpanded = ref(false)
const listMaxHeight = '30rem'
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
//
// ⚠️ 不能写 `Math.floor(cents / 100)` —— 那是在【元】这一级取整，会把不足
// 1 元的部分整个抹掉：可退 1 分时算出来是 0，用户点「全部退款」看到的是
// 0 元，然后按钮是禁用的（refundCents <= 0），等于这一分永远退不了。
//
// 原注释写的是「宁可少退一分，也不要因为凑整而超额被拒」，方向恰好搞反了：
// 该保住的正是那一分。用定点小数转成元，与 refundCents 的 ×100+round
// 互为逆运算，不会多也不会少。
function fillFullRefund() {
  refundYuan.value = (refundableCents.value / 100).toFixed(2)
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
  loadError.value = ''
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
    } else {
      // ⚠️ 拉不到就必须说，不能保持初值。
      //
      // wallet 的初值是 { balanceCents: 0, ... }，静默失败时页面顶部会把
      // 「¥0.00」当成真实余额显示出来 —— 用户看到的第一反应是「我的钱没了」，
      // 然后来报障。这比「没反应」严重得多：它是【错误数据】。
      loadError.value = await readError(walletResp)
    }
    if (ordersResp.ok) {
      const data = await readJSON(ordersResp)
      orders.value = Array.isArray(data) ? data : []
    } else if (!loadError.value) {
      // 订单列表拉不到时，卡片会显示「还没有订单」——同样是在撒谎。
      loadError.value = await readError(ordersResp)
    }
  } catch (e) {
    loadError.value = e?.message || String(e)
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

// 回跳结算完成后重拉一次余额与流水。
//
// ⚠️ 不能指望 onMounted 那次 load 就能拿到新余额：App.vue 的补查单跑在
// 它自己的 onMounted 里，而 Vue 是【子组件先挂载】—— 我们的 load 比结算更早，
// 拿到的是到账前的旧值。详见 utils/paymentSignal.js。
watch(paymentSettledTick, () => { load() })

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

// 订单状态的文案与配色统一放在 utils/format（orderStatus*）—— 这里原先是
// 第三份拷贝（另有 PaymentModal 与 ResultView 各一份）。用户可见的文案
// 分叉过就会漂，所以收到一处。
</script>
