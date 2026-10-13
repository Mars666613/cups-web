<template>
  <div class="p-3 sm:p-4 md:p-6 space-y-4 md:space-y-6">
    <AdminTabs current="/admin/orders" />

    <!-- 订单记录 -->
    <UCard>
      <template #header>
        <h2 class="text-xl font-bold flex items-center gap-2">
          <UIcon name="i-lucide-receipt" class="w-5 h-5" />
          打印订单
        </h2>
      </template>

      <div class="flex flex-wrap gap-3 items-end mb-4">
        <UInput v-model="orderFilters.username" placeholder="用户名" />
        <USelect v-model="orderFilters.status" :items="orderStatusItems" value-key="value" label-key="label" class="w-36" />
        <UButton variant="outline" @click="searchAdminOrders" icon="i-lucide-search">查询</UButton>
      </div>

      <div v-if="loadError" class="flex items-start gap-2 mb-4">
        <UAlert class="flex-1" color="error" variant="soft" icon="i-lucide-alert-triangle" title="订单加载失败" :description="loadError" />
        <UButton size="xs" variant="outline" icon="i-lucide-refresh-cw" @click="loadAdminOrders">重试</UButton>
      </div>

      <div class="overflow-x-auto">
        <UTable :columns="adminOrderColumns" :data="adminOrdersPaged">
          <template #username-cell="{ row }">
            <span :title="row.original.username">{{ userLabel(row.original.username) }}</span>
          </template>
          <template #createdAt-cell="{ row }">
            <span class="text-muted whitespace-nowrap">{{ formatTime(row.original.createdAt) }}</span>
          </template>
          <template #amountCents-cell="{ row }">{{ formatCents(row.original.amountCents) }}</template>
          <template #refundedCents-cell="{ row }">
            <span :class="row.original.refundedCents > 0 ? 'text-success' : 'text-muted'">
              {{ formatCents(row.original.refundedCents) }}
            </span>
          </template>
          <template #status-cell="{ row }">
            <UBadge :color="orderStatusColor(row.original.status)" variant="subtle" size="sm">
              {{ orderStatusText(row.original.status) }}
            </UBadge>
          </template>
          <template #actions-cell="{ row }">
            <UButton
              size="xs"
              variant="ghost"
              icon="i-lucide-rotate-ccw"
              :disabled="!canRefund(row.original)"
              @click="confirmRefund(row.original)"
            >退款</UButton>
          </template>
        </UTable>
      </div>
      <div v-if="orderTotal > 0" class="flex items-center justify-between gap-2 mt-3">
        <span class="text-xs text-muted">共 {{ orderTotal }} 个订单</span>
        <UPagination
          v-if="orderPages > 1"
          v-model:page="orderPage"
          :total="orderTotal"
          :items-per-page="orderPager.pageSize"
          size="sm"
        />
      </div>
      <div class="text-sm text-muted mt-2">
        退款会把订单金额<b>全额退回该用户余额</b>，并写一条 refund 流水。已全额退过款的订单按钮会禁用 —— 打印失败时系统已自动退过一次，这里是给需要人工定夺的单子（例如卡在「打印中」被转人工的）用的。
      </div>
    </UCard>

    <UModal v-model:open="showRefundModal">
      <template #content>
        <div class="p-6 space-y-4">
          <h3 class="text-lg font-semibold">确认退款</h3>
          <p v-if="pendingRefundOrder">
            将把 <strong>{{ formatCents(pendingRefundOrder.amountCents) }}</strong>
            全额退回到用户 <strong>{{ pendingRefundOrder.username }}</strong> 的余额。
          </p>
          <p class="text-xs text-muted">
            退款会写一条 refund 流水留痕。在线支付渠道的原路退款需人工在商户后台操作，因此统一退到余额。
          </p>
          <UInput v-model="refundReason" placeholder="退款原因（可选，会记入流水备注）" class="w-full" />
          <div class="flex justify-end gap-2">
            <UButton variant="ghost" @click="showRefundModal = false">取消</UButton>
            <UButton
              color="primary"
              :loading="refundingOrderId != null"
              @click="triggerRefund"
            >确认退款</UButton>
          </div>
        </div>
      </template>
    </UModal>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { getCSRF, readError, readJSON } from '../../utils/api'
import { formatCents, formatTime, orderStatusColor, orderStatusText, userLabel } from '../../utils/format'
import { usePagedRows } from '../../utils/tablePage'
import { FILTER_ALL } from '../../utils/adminFilters'
import AdminTabs from '../../components/admin/AdminTabs.vue'

const toast = useToast()
const emit = defineEmits(['logout'])

const adminOrders = ref([])
const loadError = ref('')

const orderPager = usePagedRows(adminOrders)
const { page: orderPage, pageCount: orderPages, total: orderTotal, rows: adminOrdersPaged } = orderPager

const orderFilters = ref({ username: '', status: FILTER_ALL })
const refundingOrderId = ref(null)
const refundReason = ref('')
const showRefundModal = ref(false)
const pendingRefundOrder = ref(null)

const orderStatusItems = [
  { label: '全部状态', value: FILTER_ALL },
  { label: '待支付', value: 'pending_payment' },
  { label: '已支付', value: 'paid' },
  { label: '打印中', value: 'printing' },
  { label: '已打印', value: 'printed' },
  { label: '失败', value: 'failed' },
  { label: '已退款', value: 'refunded' },
  { label: '已过期', value: 'expired' },
  { label: '已取消', value: 'cancelled' }
]

const adminOrderColumns = [
  { accessorKey: 'createdAt', header: '时间' },
  { accessorKey: 'username', header: '用户' },
  { accessorKey: 'filename', header: '文件' },
  { accessorKey: 'billedPages', header: '印数' },
  { accessorKey: 'amountCents', header: '金额' },
  { accessorKey: 'refundedCents', header: '已退' },
  { accessorKey: 'status', header: '状态' },
  { id: 'actions', header: '操作' }
]

async function loadAdminOrders() {
  loadError.value = ''
  try {
    const params = new URLSearchParams()
    if (orderFilters.value.username) params.set('username', orderFilters.value.username)
    if (orderFilters.value.status !== FILTER_ALL) params.set('status', orderFilters.value.status)
    const resp = await fetch(`/api/admin/orders?${params}`, { credentials: 'include' })
    if (!resp.ok) {
      if (resp.status === 401) {
        emit('logout')
        return
      }
      // 🚫 失败时不能只是 return —— 表格会渲染成「暂无数据」，
      // 管理员会以为全站真的没有订单。失败必须有可见的错误态。
      loadError.value = await readError(resp)
      return
    }
    adminOrders.value = await readJSON(resp)
  } catch (e) {
    loadError.value = `无法连接到服务端：${e.message || e}`
  }
}

// 「查询」= 翻回第一页 + 重查。
//
// 🚫 拆成独立函数、而不是给 loader 加个 `reset = true` 参数：
// 模板里写 `@click="loadAdminOrders"` 时 Vue 会把 MouseEvent 当第一个实参
// 传进去，带可选参数的签名会被事件对象顶成真值（AGENTS.md 里记过两次，
// 两次都是「点了没反应」这种最难查的症状）。所以公开入口一律无参。
function searchAdminOrders() {
  orderPager.resetPage()
  return loadAdminOrders()
}

// 一笔订单能否退款：必须是付过钱的、且还没退满。
function canRefund(order) {
  return Boolean(order.payMethod) && order.refundedCents < order.amountCents
}

function confirmRefund(order) {
  pendingRefundOrder.value = order
  refundReason.value = ''
  showRefundModal.value = true
}

async function triggerRefund() {
  const order = pendingRefundOrder.value
  if (!order) return
  refundingOrderId.value = order.orderId
  try {
    const resp = await fetch(`/api/admin/orders/${order.orderId}/refund`, {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': getCSRF() },
      body: JSON.stringify({ reason: refundReason.value })
    })
    if (!resp.ok) {
      const msg = await readError(resp)
      toast.add({ title: '退款失败', description: msg, color: 'error', icon: 'i-lucide-x-circle' })
      if (resp.status === 401) emit('logout')
      return
    }
    const data = await readJSON(resp)
    toast.add({
      title: '已退款',
      description: `${formatCents(data.refundedCents)} 已退回该用户余额`,
      color: 'success',
      icon: 'i-lucide-check-circle'
    })
    showRefundModal.value = false
    // 只重载本页的订单。原先还会顺带刷新用户与流水列表 —— 那两个现在是
    // 各自的子页，不挂载在这里；它们每次进页都会自己重新拉取。
    await loadAdminOrders()
  } finally {
    refundingOrderId.value = null
  }
}

onMounted(loadAdminOrders)
</script>
