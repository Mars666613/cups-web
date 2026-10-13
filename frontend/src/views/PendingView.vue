<template>
  <div class="p-3 sm:p-4 md:p-6 max-w-3xl mx-auto space-y-4">
    <!-- ── 顶部：这批任务是取到了还是还没取 ───────────────────────── -->
    <div class="flex flex-col items-center text-center pt-6 pb-2">
      <div
        class="w-16 h-16 rounded-full flex items-center justify-center"
        :class="allDone ? 'bg-success/15' : 'bg-warning/15'"
      >
        <UIcon
          :name="allDone ? 'i-lucide-check' : 'i-lucide-scan-line'"
          class="w-9 h-9"
          :class="allDone ? 'text-success' : 'text-warning'"
        />
      </div>
      <h1 class="text-xl font-bold mt-3">{{ headline }}</h1>
      <p class="text-sm text-muted mt-1">{{ subline }}</p>
    </div>

    <!-- ── 去哪台打印机取 ──────────────────────────────────────────
         这是整页最重要的一块。员工刚提交完，最想知道的就是
         「我现在该去哪儿」。所以它单独占一张卡、字号大、带位置。 -->
    <UCard v-if="!allDone && orders.length > 0">
      <div class="flex items-start gap-3">
        <div class="w-11 h-11 rounded-lg bg-primary/10 flex items-center justify-center shrink-0">
          <UIcon name="i-lucide-printer" class="w-6 h-6 text-primary" />
        </div>
        <div class="flex-1 min-w-0">
          <p class="text-xs text-muted">请到这台打印机前刷卡取件</p>
          <p class="text-lg font-bold truncate" :title="printerUri">{{ printerName }}</p>
          <p v-if="printerLocation" class="text-sm text-muted mt-0.5">{{ printerLocation }}</p>
          <p class="text-sm mt-2">
            <UIcon name="i-lucide-scan-line" class="w-4 h-4 inline-block align-[-3px]" />
            把工牌放到读卡器上，任务就会自动打印出来。
          </p>
        </div>
      </div>
    </UCard>

    <!-- ── 过期时间 ────────────────────────────────────────────────
         单独说一句，而且给的是【具体时间】而不是「72 小时」——
         员工记不住「我是什么时候提交的」。 -->
    <UAlert
      v-if="!allDone && expiresAt"
      color="warning"
      variant="soft"
      icon="i-lucide-clock"
      :title="`请在 ${formatTime(expiresAt)} 前取走`"
      :description="`超过这个时间任务会自动作废、文件被清理。` + (remainText ? `（还剩 ${remainText}）` : '')"
    />

    <UAlert
      v-if="loadError"
      color="error"
      variant="soft"
      icon="i-lucide-x-circle"
      title="任务详情加载失败"
      :description="loadError"
    />

    <!-- ── 这批里有哪几个文件 ────────────────────────────────────── -->
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
              {{ o.pages }} 页<template v-if="o.copies > 1"> × {{ o.copies }} 份</template>
            </div>
            <div v-if="o.status === 'failed'" class="text-xs text-error mt-1">
              {{ o.error || '打印失败' }}
            </div>
          </div>
          <UBadge :color="orderStatusColor(o.status)" variant="subtle" size="sm">
            {{ orderStatusText(o.status) }}
          </UBadge>
        </div>
      </div>
    </UCard>

    <UAlert
      color="neutral"
      variant="soft"
      icon="i-lucide-info"
      title="还能在哪看到"
      description="没取走的任务都在「个人中心 → 打印记录」的「待取件」里，随时可以撤销。"
    />

    <div class="flex flex-col sm:flex-row gap-2">
      <UButton
        v-if="!allDone"
        color="primary"
        block
        size="lg"
        icon="i-lucide-printer"
        @click="router.replace('/print')"
      >
        知道了
      </UButton>
      <UButton
        v-else
        color="primary"
        block
        size="lg"
        icon="i-lucide-house"
        @click="router.replace('/print')"
      >
        返回首页
      </UButton>
      <UButton color="neutral" variant="outline" block size="lg" icon="i-lucide-inbox" @click="goRecords">
        查看待取件
      </UButton>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { apiFetch, readError, readJSON } from '../utils/api'
// ⚠️ 订单状态用 orderStatus*，不是 status*（后者映射的是【打印记录】的状态）。
import { formatTime, orderStatusColor, orderStatusText, printerLabel } from '../utils/format'

// 刷卡取件的【中间页】。
//
// 与 ResultView（支付完成后的中间页）是兄弟，但讲的是完全不同的一件事：
// 那边说「钱付了、纸在打」，这边说「**纸还没出来，你得去刷卡**」。
// 两者绝不能共用 —— 混在一起就会出现「提示已提交打印，员工转身就走」，
// 而任务其实还躺在待取件里。
//
// 为什么不能只用一条 toast：员工刚点完提交，最想知道的是「去哪儿、什么时候之前」。
// 一句飘走的气泡既说不下这两件事，也留不住。
//
// 用 router.replace 而不是 push：这是流程终点，不该留在历史里
// （否则在首页按返回又回到这儿）。
const route = useRoute()
const router = useRouter()
const emit = defineEmits(['logout'])

const orders = ref([])
const loadError = ref('')
const printerUri = ref('')
const printerLocation = ref('')
const expiresAt = ref('')
const now = ref(Date.now())
let timer = null
let clock = null

// 这批里还有没有没取走的。有 held 就说明纸还没出来。
const heldOrders = computed(() => orders.value.filter(o => o.status === 'held'))
const allDone = computed(() => orders.value.length > 0 && heldOrders.value.length === 0)

const headline = computed(() => {
  if (loadError.value) return '任务已暂存'
  if (orders.value.length === 0) return '任务已暂存'
  if (allDone.value) return '这批已经取走了'
  return '任务已暂存，请到打印机刷卡取件'
})

const subline = computed(() => {
  if (allDone.value) return '纸已经出来过了。还有别的任务没取的话，去「打印记录」里看。'
  return '现在【还不会打印】—— 到打印机旁刷工牌，它才会出纸。'
})

const printerName = computed(() => printerUri.value ? printerLabel({ uri: printerUri.value }) : '（未知打印机）')

// 剩余时间。给的是「还剩 2 小时」这种人话，而不是一个时间戳 ——
// 时间戳要用户自己减，而他现在正站在打印机旁边。
const remainText = computed(() => {
  if (!expiresAt.value) return ''
  const ms = new Date(expiresAt.value).getTime() - now.value
  if (Number.isNaN(ms) || ms <= 0) return ''
  const mins = Math.floor(ms / 60000)
  if (mins >= 60) {
    const h = Math.floor(mins / 60)
    const m = mins % 60
    return m ? `${h} 小时 ${m} 分钟` : `${h} 小时`
  }
  return `${Math.max(1, mins)} 分钟`
})

async function fetchBatch() {
  const batchId = route.query.batch
  // 没带批次号就直接进这个页面（比如手敲 URL）：不猜「最近一条」，
  // 多设备或连着打几次时必然猜错，用户看到的是别人的单子。
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
    loadError.value = ''

    // 打印机的信息从订单里取（订单自带 printer_uri），再补一次位置。
    const first = orders.value[0]
    if (first?.printerUri) {
      printerUri.value = first.printerUri
      loadPrinterLocation(first.printerUri)
    }
    // 过期时间各单相同（同一批一起建的）；取还在待取件的那一条。
    const pending = heldOrders.value[0] || first
    expiresAt.value = pending?.expiresAt || ''

    if (allDone.value) stopPolling()
  } catch (e) {
    loadError.value = e?.message || String(e)
    stopPolling()
  }
}

// 位置是可选的补充信息：拿不到就不显示，不影响「去哪台」这件事本身
//（队列名已经在上面了）。
async function loadPrinterLocation(uri) {
  try {
    const resp = await apiFetch(`/api/printer-info?uri=${encodeURIComponent(uri)}`, {}, () => emit('logout'))
    if (!resp.ok) return
    const info = await readJSON(resp)
    printerLocation.value = info?.location || ''
  } catch {
    // 忽略
  }
}

function stopPolling() {
  if (timer) { clearInterval(timer); timer = null }
}

function goRecords() {
  router.replace('/records')
}

onMounted(async () => {
  await fetchBatch()
  // 还停在待取件就继续轮询：员工在打印机那边刷完卡回来，这一页要能自己变成
  //「已取走」。已经取走就不必再打了。
  if (route.query.batch && !allDone.value && !loadError.value) {
    timer = setInterval(fetchBatch, 3000)
  }
  // 剩余时间也要跟着走，否则「还剩 2 小时」会一直停在打开页面那一刻的值。
  clock = setInterval(() => { now.value = Date.now() }, 30000)
})

onUnmounted(() => {
  stopPolling()
  if (clock) clearInterval(clock)
})
</script>
