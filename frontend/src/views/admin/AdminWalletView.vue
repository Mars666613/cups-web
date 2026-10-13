<template>
  <div class="p-3 sm:p-4 md:p-6 space-y-4 md:space-y-6">
    <AdminTabs current="/admin/wallet" />

    <!-- 余额流水 -->
    <UCard>
      <template #header>
        <h2 class="text-xl font-bold flex items-center gap-2">
          <UIcon name="i-lucide-scroll-text" class="w-5 h-5" />
          余额流水
        </h2>
      </template>

      <div class="flex flex-wrap gap-3 items-end mb-4">
        <UInput v-model="txFilters.username" placeholder="用户名" />
        <USelect v-model="txFilters.kind" :items="txKindItems" value-key="value" label-key="label" class="w-36" />
        <UButton variant="outline" @click="searchAdminTransactions" icon="i-lucide-search">查询</UButton>
      </div>

      <div v-if="loadError" class="flex items-start gap-2 mb-4">
        <UAlert class="flex-1" color="error" variant="soft" icon="i-lucide-alert-triangle" title="余额流水加载失败" :description="loadError" />
        <UButton size="xs" variant="outline" icon="i-lucide-refresh-cw" @click="loadAdminTransactions">重试</UButton>
      </div>

      <div class="overflow-x-auto">
        <UTable :columns="txColumns" :data="adminTransactionsPaged">
          <template #username-cell="{ row }">
            <span :title="row.original.username">{{ userLabel(row.original.username) }}</span>
          </template>
          <template #createdAt-cell="{ row }">
            <span class="text-muted whitespace-nowrap">{{ formatTime(row.original.createdAt) }}</span>
          </template>
          <template #kind-cell="{ row }">{{ txKindText[row.original.kind] || row.original.kind }}</template>
          <template #amountCents-cell="{ row }">
            <span :class="row.original.amountCents < 0 ? 'text-error' : 'text-success'">
              {{ row.original.amountCents > 0 ? '+' : '' }}{{ formatCents(row.original.amountCents) }}
            </span>
          </template>
          <template #balanceAfterCents-cell="{ row }">
            <span class="text-muted">{{ formatCents(row.original.balanceAfterCents) }}</span>
          </template>
        </UTable>
      </div>
      <div v-if="txTotal > 0" class="flex items-center justify-between gap-2 mt-3">
        <span class="text-xs text-muted">共 {{ txTotal }} 条流水</span>
        <UPagination
          v-if="txPages > 1"
          v-model:page="txPage"
          :total="txTotal"
          :items-per-page="txPager.pageSize"
          size="sm"
        />
      </div>
      <div class="text-sm text-muted mt-2">
        账本是审计记录，只增不改。有余额或有流水的用户因此不允许删除 —— 删用户会级联抹掉这些记录。
      </div>
    </UCard>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { readError, readJSON } from '../../utils/api'
import { formatCents, formatTime, userLabel } from '../../utils/format'
import { usePagedRows } from '../../utils/tablePage'
import { FILTER_ALL, toLabelMap } from '../../utils/adminFilters'
import AdminTabs from '../../components/admin/AdminTabs.vue'

const emit = defineEmits(['logout'])

const adminTransactions = ref([])
const loadError = ref('')

const txPager = usePagedRows(adminTransactions)
const { page: txPage, pageCount: txPages, total: txTotal, rows: adminTransactionsPaged } = txPager

const txFilters = ref({ username: '', kind: FILTER_ALL })

const txKindItems = [
  { label: '全部类型', value: FILTER_ALL },
  { label: '充值', value: 'topup' },
  { label: '打印扣费', value: 'spend' },
  { label: '退款', value: 'refund' },
  { label: '管理员调整', value: 'admin_adjust' }
]

const txColumns = [
  { accessorKey: 'createdAt', header: '时间' },
  { accessorKey: 'username', header: '用户' },
  { accessorKey: 'kind', header: '类型' },
  { accessorKey: 'amountCents', header: '金额' },
  { accessorKey: 'balanceAfterCents', header: '余额' },
  { accessorKey: 'note', header: '备注' }
]

const txKindText = toLabelMap(txKindItems)

async function loadAdminTransactions() {
  loadError.value = ''
  try {
    const params = new URLSearchParams()
    if (txFilters.value.username) params.set('username', txFilters.value.username)
    if (txFilters.value.kind !== FILTER_ALL) params.set('kind', txFilters.value.kind)
    const resp = await fetch(`/api/admin/wallet-transactions?${params}`, { credentials: 'include' })
    if (!resp.ok) {
      if (resp.status === 401) {
        emit('logout')
        return
      }
      // 🚫 失败时不能只是 return —— 表格会渲染成「暂无数据」，
      // 管理员会以为账本真的没有流水。失败必须有可见的错误态。
      loadError.value = await readError(resp)
      return
    }
    adminTransactions.value = await readJSON(resp)
  } catch (e) {
    loadError.value = `无法连接到服务端：${e.message || e}`
  }
}

// 「查询」= 翻回第一页 + 重查。公开入口保持无参（理由见 AdminOrdersView 同函数上的注释）。
function searchAdminTransactions() {
  txPager.resetPage()
  return loadAdminTransactions()
}

onMounted(loadAdminTransactions)
</script>
