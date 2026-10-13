<template>
  <div class="p-3 sm:p-4 md:p-6 max-w-5xl mx-auto space-y-4">
    <h1 class="text-lg font-bold flex items-center gap-2">
      <UIcon name="i-lucide-history" class="w-5 h-5 text-primary" />
      打印记录
    </h1>

    <!-- 加载失败要盖在列表上面说清楚：下面那个列表在拉不到数据时会显示
         「暂无打印记录」，用户会以为记录被清空了。 -->
    <UAlert
      v-if="loadError"
      color="error"
      variant="soft"
      icon="i-lucide-x-circle"
      title="打印记录加载失败，下面的列表可能不是最新的"
      :description="loadError"
    />

    <!-- ── 待取件（刷卡取件模式）──────────────────────────────────────
         放在记录列表【上面】：员工最关心的永远是「我还有什么没取」，
         而不是「我以前打过什么」。
         数据源是 /api/pull-orders（查 print_orders），【不是】打印记录 ——
         后者受 save_history 开关影响，关掉时压根不写记录，
         而「看不到自己待取的任务」恰恰是这个功能要解决的问题本身。 -->
    <UCard v-if="pull.enabled">
      <template #header>
        <h2 class="text-base font-semibold flex items-center gap-2">
          <UIcon name="i-lucide-inbox" class="w-4 h-4" />
          待取件
          <UBadge v-if="pending.length" color="warning" variant="subtle" size="sm">{{ pending.length }}</UBadge>
        </h2>
      </template>

      <p v-if="pendingError" class="text-sm text-error">{{ pendingError }}</p>
      <p v-else-if="pending.length === 0" class="text-sm text-muted">
        没有待取件的任务。这个模式下提交的打印任务会先暂存在服务器上，到打印机刷工牌才出纸。
      </p>
      <ul v-else class="divide-y divide-default">
        <li v-for="o in pending" :key="o.orderId" class="flex items-start gap-3 py-3">
          <div class="flex-1 min-w-0">
            <p class="truncate text-sm font-medium" :title="o.filename">{{ o.filename }}</p>
            <p class="text-xs text-muted truncate">
              {{ formatPrinterName(o.printerUri) }} · {{ o.pages }} 页 · 提交于 {{ formatTime(o.createdAt) }}
            </p>
            <p v-if="o.expiresAt" class="text-xs text-muted">
              请在 {{ formatTime(o.expiresAt) }} 前取件，过期后作废
            </p>
          </div>
          <UButton
            size="xs"
            variant="outline"
            color="error"
            :loading="cancellingId === o.orderId"
            @click="cancelPending(o)"
          >撤销</UButton>
        </li>
      </ul>
    </UCard>

    <PrintRecordList
      ref="recordListRef"
      :records="printRecords"
      :loading="loadingRecords"
      :printers="printers"
      current-printer=""
      :media-source-supported="mediaSourceSupported"
      @refresh="loadPrintRecords"
      @reprint="handleReprint"
      @reprint-open="loadReprintPrinterInfo"
    />

    <!-- 计费开启时重打会转成订单，弹同一个支付窗 -->
    <PaymentModal
      v-if="billing.enabled"
      v-model:open="showPaymentModal"
      :batch="orderBatch"
      :payment-methods="wallet.paymentMethods || []"
      @paid="onPaid"
      @go-wallet="goWallet"
    />
  </div>
</template>

<script setup>
import { ref, onMounted, onUnmounted } from 'vue'
import { apiFetch, readError, readJSON } from '../utils/api'
import { formatPrinterName, formatTime } from '../utils/format'
import PrintRecordList from '../components/print/PrintRecordList.vue'
import PaymentModal from '../components/print/PaymentModal.vue'

// 这个页面是从打印页拆出来的。
//
// 记录属于「回头再看」，不该占着打印页 —— 手机上一次打印要滚很久才能
// 翻过去。拆出来后打印页只留操作，记录归到个人中心。
const emit = defineEmits(['logout', 'go-wallet'])
const toast = useToast()

const printRecords = ref([])
const loadingRecords = ref(false)
// 拉取失败的原因；非空时在列表上方显示。理由见 runLoadPrintRecords 里的注释。
const loadError = ref('')
const recordListRef = ref(null)
const printers = ref([])

// 重打弹窗里的进纸盒可选项：按本次要打的那台打印机现查。
// 查不到就留空 —— 进纸盒下拉消失，其余表单照常可用，不影响重打。
const mediaSourceSupported = ref([])

const billing = ref({ enabled: false })
const wallet = ref({ balanceCents: 0 })
const orderBatch = ref(null)
const showPaymentModal = ref(false)

// 刷卡取件模式。与计费互斥，两个不会同时为真。
const pull = ref({ enabled: false, ttlHours: 72 })
const pending = ref([])
const pendingError = ref('')
const cancellingId = ref(null)

// 5 秒轮询：任务在 CUPS 里从 queued 变 printed/failed 是后端确认的，
// 不轮询的话用户要手动刷新才能看到结果。
let recordsTimer = null

// 无参入口。模板里是 @refresh="loadPrintRecords" 这种【裸绑定】，Vue 会把
// 事件对象塞进第一个参数 —— 签名若带默认参数就会被它顶掉（事件对象是真值），
// 手动点刷新反而静默走进「静默模式」，不显示加载态。
// 静默行为拆成独立的 runLoadPrintRecords，与 WalletView 的 runTopup 同一套写法。
async function loadPrintRecords() {
  await runLoadPrintRecords(false)
}

async function runLoadPrintRecords(silent) {
  if (!silent) loadingRecords.value = true
  try {
    const resp = await apiFetch('/api/print-records', {}, () => emit('logout'))
    if (resp.ok) {
      const data = await readJSON(resp)
      printRecords.value = (data || []).map(r => ({
        id: r.id, filename: r.filename, printerUri: r.printerUri,
        pages: r.pages, status: r.status, isColor: r.isColor,
        isDuplex: r.isDuplex, jobId: r.jobId, createdAt: r.createdAt
      }))
      loadError.value = ''
    } else if (!silent) {
      // ⚠️ 拉不到就必须说。列表组件的空态是「暂无打印记录」——接口挂了的时候
      // 它会用肯定的语气告诉用户「你没有记录」，用户会以为记录被清掉了。
      // 那是【错误结论】，比「没反应」更糟。
      //
      // 只在非静默时提示：5 秒轮询遇到一次网络抖动不该在界面上闪一条红字。
      loadError.value = await readError(resp)
    }
  } catch (e) {
    if (!silent) loadError.value = e?.message || String(e)
  } finally {
    loadingRecords.value = false
  }
}

// 待取件列表。只在取件模式下请求（其余模式这个接口是 404）。
async function loadPending() {
  if (!pull.value.enabled) return
  pendingError.value = ''
  try {
    const resp = await apiFetch('/api/pull-orders', {}, () => emit('logout'))
    if (!resp.ok) {
      // ⚠️ 失败要有可见的错误态：这一段空着的话，员工看到的是
      // 「没有待取件的任务」——一个肯定的错误结论，比什么都不显示更糟。
      pendingError.value = await readError(resp)
      return
    }
    pending.value = (await readJSON(resp)) || []
  } catch (e) {
    pendingError.value = `无法连接到服务端：${e.message || e}`
  }
}

async function cancelPending(o) {
  cancellingId.value = o.orderId
  try {
    const resp = await apiFetch(`/api/pull-orders/${o.orderId}`, { method: 'DELETE' }, () => emit('logout'))
    if (!resp.ok) {
      toast.add({ title: '撤销失败', description: await readError(resp), color: 'error', icon: 'i-lucide-x-circle' })
      return
    }
    toast.add({
      title: '已撤销',
      description: `${o.filename} 不会再打印了`,
      color: 'success',
      icon: 'i-lucide-check-circle'
    })
    await loadPending()
  } catch (e) {
    toast.add({ title: '撤销失败', description: e.message, color: 'error', icon: 'i-lucide-x-circle' })
  } finally {
    cancellingId.value = null
  }
}

async function loadPrinters() {
  try {
    const resp = await apiFetch('/api/printers', {}, () => emit('logout'))
    if (resp.ok) printers.value = (await readJSON(resp)) || []
  } catch (e) {
    console.error('加载打印机失败', e)
  }
}

async function loadReprintPrinterInfo(printerUri) {
  mediaSourceSupported.value = []
  if (!printerUri) return
  try {
    const resp = await apiFetch(
      `/api/printer-info?uri=${encodeURIComponent(printerUri)}`,
      {}, () => emit('logout')
    )
    if (!resp.ok) return
    const info = await readJSON(resp)
    mediaSourceSupported.value = info?.mediaSourceSupported || []
  } catch {
    // 拿不到就不提供进纸盒选项，重打的其它参数不受影响。
  }
}

async function handleReprint(payload) {
  const { id } = payload
  try {
    const resp = await apiFetch(`/api/print-records/${id}/reprint`, {
      method: 'POST',
      body: JSON.stringify({
        printer: payload.printer,
        duplex: payload.duplex,
        color: payload.color,
        copies: payload.copies,
        orientation: payload.orientation,
        paperSize: payload.paperSize,
        paperType: payload.paperType,
        mediaSource: payload.mediaSource,
        printScaling: payload.printScaling,
        pageRange: payload.pageRange,
        pageSet: payload.pageSet,
        mirror: payload.mirror,
        watermarkText: payload.watermarkText,
        numberUp: payload.numberUp,
        numberUpLayout: payload.numberUpLayout,
        pageBorder: payload.pageBorder
      })
    }, () => emit('logout'))
    if (!resp.ok) {
      throw new Error(await readError(resp))
    }
    const j = await readJSON(resp)
    // 三种模式的响应形状不同，靠形状分流，前端不需要自己去判断模式开关：
    //   刷卡取件  held=true + orders        → 已暂存，去待取件里看
    //   计费      batchId + orders          → 弹支付窗
    //   免费      pages + jobId             → 已经打出去了
    if (j.held) {
      toast.add({
        title: '已暂存，请到打印机刷卡取件',
        // ⚠️ 这里也必须是「已暂存」，不能沿用下面的「重新打印已提交」——
        // 那句话会让员工以为纸已经出来了。
        description: `${j.orders?.length || 1} 个任务已暂存，请到打印机刷工牌取件`,
        color: 'success',
        icon: 'i-lucide-scan-line',
        duration: 8000
      })
      await loadPending()
      return
    }
    if (j.batchId) {
      orderBatch.value = j
      showPaymentModal.value = true
      return
    }
    toast.add({
      title: '重新打印已提交',
      description: `${j.pages} 页，任务ID：${j.jobId || '—'}`,
      color: 'success',
      icon: 'i-lucide-check-circle'
    })
    await loadPrintRecords()
  } catch (e) {
    toast.add({ title: '重新打印失败', description: e.message, color: 'error', icon: 'i-lucide-x-circle' })
  } finally {
    recordListRef.value?.clearReprintLoading()
  }
}

async function loadBilling() {
  try {
    const pub = await fetch('/api/public-settings', { credentials: 'include' })
    if (pub.ok) {
      const s = await readJSON(pub)
      billing.value.enabled = s.billingEnabled === true
      pull.value.enabled = s.pullModeEnabled === true
      pull.value.ttlHours = s.pullTtlHours || 72
    }
    // 取件模式不需要钱包 —— 它压根不收费。
    if (!billing.value.enabled) return
    const walletResp = await apiFetch('/api/wallet', {}, () => emit('logout'))
    if (walletResp.ok) wallet.value = await readJSON(walletResp)
  } catch {
    billing.value.enabled = false
  }
}

async function onPaid() {
  await runLoadPrintRecords(true)
}

function goWallet() {
  showPaymentModal.value = false
  emit('go-wallet')
}

onMounted(async () => {
  // loadBilling 必须排在 loadPending 前面 —— 后者要靠 pull.enabled 才知道
  // 该不该请求（非取件模式下 /api/pull-orders 是 404）。
  await loadBilling()
  await loadPrinters()
  await loadPrintRecords()
  await loadPending()
  // 待取件跟着一起轮询：员工在别的设备上刷卡取走后，这一页也能自己变。
  recordsTimer = setInterval(() => {
    runLoadPrintRecords(true)
    loadPending()
  }, 5000)
})

onUnmounted(() => {
  clearInterval(recordsTimer)
})
</script>
