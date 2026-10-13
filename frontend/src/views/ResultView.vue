<template>
  <div class="p-3 sm:p-4 md:p-6 max-w-3xl mx-auto space-y-4">
    <div class="flex flex-col items-center text-center pt-6 pb-2">
      <div class="w-16 h-16 rounded-full bg-success/15 flex items-center justify-center">
        <UIcon name="i-lucide-check" class="w-9 h-9 text-success" />
      </div>
      <h1 class="text-xl font-bold mt-3">{{ headline }}</h1>
      <p class="text-sm text-muted mt-1">{{ subline }}</p>
    </div>

    <!-- 刚才那几笔。批次号由支付回跳/支付完成时带过来 —— 不用「最近一条」
         去猜，多设备或连着打几次时会猜错，用户看到的是别人的单子。 -->
    <UCard v-if="orders.length > 0" :ui="{ body: 'p-0' }">
      <div class="divide-y divide-default">
        <div
          v-for="o in orders"
          :key="o.orderId"
          class="flex items-start justify-between gap-3 p-4"
        >
          <div class="min-w-0 flex-1">
            <div class="font-medium truncate">{{ o.filename }}</div>
            <div class="text-xs text-muted mt-0.5">
              {{ o.billedPages }} 页<template v-if="o.copies > 1"> × {{ o.copies }} 份</template>
              <!-- 刚付完款，用户会想知道这笔是从哪扣的 -->
              <template v-if="payMethodText(o.payMethod)">
                · <PayIcon :provider="o.payMethod" size="w-3.5 h-3.5" class="inline-block align-[-2px]" />
                {{ payMethodText(o.payMethod) }}
              </template>
            </div>
            <div v-if="o.status === 'failed'" class="text-xs text-error mt-1">
              {{ o.error || '打印失败' }}
              <template v-if="o.refundedCents > 0"> · 已退款 {{ formatCents(o.refundedCents) }}</template>
            </div>
            <div v-else-if="o.status === 'printing' || o.status === 'paid'" class="text-xs text-muted mt-1">
              已送印，正在排队
            </div>
          </div>
          <div class="text-right shrink-0">
            <div class="font-medium">{{ formatCents(o.amountCents) }}</div>
            <UBadge :color="orderStatusColor(o.status)" variant="subtle" size="sm">{{ orderStatusText(o.status) }}</UBadge>
          </div>
        </div>
      </div>
    </UCard>

    <UAlert
      v-if="loadError"
      color="warning"
      variant="soft"
      icon="i-lucide-triangle-alert"
      title="任务详情加载失败"
      :description="loadError + '（打印任务本身不受影响，可到「打印记录」里查看）'"
    />

    <UAlert
      color="neutral"
      variant="soft"
      icon="i-lucide-info"
      title="还能在哪看到"
      description="本次任务的进度和历史都留在「个人中心 → 打印记录」里，随时可以回看或重打。"
    />

    <div class="flex flex-col sm:flex-row gap-2">
      <UButton color="primary" block size="lg" icon="i-lucide-house" @click="router.replace('/print')">
        返回首页
      </UButton>
      <UButton color="neutral" variant="outline" block size="lg" icon="i-lucide-history" @click="goRecords">
        查看打印记录
      </UButton>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { apiFetch, readError, readJSON } from '../utils/api'
// ⚠️ 用 orderStatus* 而不是 status*（后者映射的是打印记录的状态）：
// 订单状态里的 printing / refunded / expired 在记录那套映射里没有，
// 会被原样透出英文标签。
import { formatCents, orderStatusColor, orderStatusText, payMethodText } from '../utils/format'
import PayIcon from '../components/PayIcon.vue'

// 打印/支付完成后的【中间页】。
//
// 为什么需要它：手机网站支付付完款，支付宝把浏览器送回 return_url，页面是
// 重新加载的 —— 之前直接落在「在线打印」的落地页上（四个按钮），用户既看不到
// 刚才打的是什么，也没有「这就算完了」的收尾感。
//
// 用 router.replace 而不是 push 离开：这个页面是流程终点，不该留在历史里
// （否则用户在首页按返回又回到这里）。
const route = useRoute()
const router = useRouter()
const emit = defineEmits(['logout'])

const orders = ref([])
const loadError = ref('')
const status = ref('')
let timer = null

// ⚠️ held 也算终态：取件模式下任务停在待取件，它【不会】自己变成 printed ——
// 员工不去刷卡就永远不动。不把它列进来的话这一页会一直空转轮询。
// 正常流程下取件模式跳的是 /pending，但用户手敲 URL / 老书签都可能落到这儿。
const TERMINAL = ['printed', 'partial', 'failed', 'expired', 'cancelled', 'held']

const headline = computed(() => {
  if (loadError.value) return '打印任务已提交'
  // 「还没打印」绝不能显示成「支付成功，正在打印」—— 那会让员工
  // 以为纸在出来，而它其实还在等着刷卡。
  if (status.value === 'held') return '任务已暂存'
  if (status.value === 'printed') return '打印完成'
  if (status.value === 'partial') return '部分完成'
  if (status.value === 'failed') return '打印失败'
  return '支付成功，正在打印'
})

const subline = computed(() => {
  if (status.value === 'held') return '现在还没打印 —— 请到打印机旁刷工牌取件。'
  if (status.value === 'failed') return '本次费用已全额退回余额。'
  if (status.value === 'partial') return '有文件打印失败，对应的费用已退回余额。'
  if (status.value === 'expired' || status.value === 'cancelled') return '这笔订单没有完成。'
  return '到账后已开始送印，进度会自动更新。'
})

async function fetchStatus() {
  const batchId = route.query.batch
  if (!batchId) return
  try {
    const resp = await apiFetch(`/api/orders/batch/${batchId}`, {}, () => emit('logout'))
    if (!resp.ok) {
      loadError.value = await readError(resp)
      stopPolling()
      return
    }
    const data = await readJSON(resp)
    orders.value = data?.orders || []
    status.value = data?.status || ''
    loadError.value = ''
    if (TERMINAL.includes(status.value)) stopPolling()
  } catch (e) {
    loadError.value = e?.message || String(e)
    stopPolling()
  }
}

function stopPolling() {
  if (timer) {
    clearInterval(timer)
    timer = null
  }
}

function goRecords() {
  router.replace('/records')
}

onMounted(async () => {
  await fetchStatus()
  // 还停在中间态就继续轮询，让「正在打印」能自己变成「打印完成」。
  // 已经终态就不必再打了 —— 这个页面不是常驻的，没必要一直占着请求。
  if (route.query.batch && !TERMINAL.includes(status.value) && !loadError.value) {
    timer = setInterval(fetchStatus, 2000)
  }
})

onUnmounted(stopPolling)
</script>
