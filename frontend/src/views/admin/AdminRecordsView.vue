<template>
  <div class="p-3 sm:p-4 md:p-6 space-y-4 md:space-y-6">
    <AdminTabs current="/admin/records" />

    <UCard>
      <template #header>
        <h2 class="text-xl font-bold flex items-center gap-2">
          <UIcon name="i-lucide-file-text" class="w-5 h-5" />
          打印记录
        </h2>
      </template>

      <div class="flex flex-wrap gap-3 items-end mb-4">
        <UInput v-model="printFilters.username" placeholder="用户名" />
        <UInput type="date" v-model="printFilters.start" />
        <UInput type="date" v-model="printFilters.end" />
        <UButton variant="outline" @click="searchPrintRecords" icon="i-lucide-search">查询</UButton>
      </div>

      <div v-if="loadError" class="flex items-start gap-2 mb-4">
        <UAlert class="flex-1" color="error" variant="soft" icon="i-lucide-alert-triangle" title="打印记录加载失败" :description="loadError" />
        <UButton size="xs" variant="outline" icon="i-lucide-refresh-cw" @click="loadPrintRecords">重试</UButton>
      </div>

      <div class="overflow-x-auto">
        <UTable :columns="printColumns" :data="printRecordsPaged">
          <template #username-cell="{ row }">
            <span :title="row.original.username">{{ userLabel(row.original.username) }}</span>
          </template>
          <!-- 时间列必须显式格式化，跟旁边的「打印订单」表一致。
               少了这个插槽，UTable 会把 createdAt 原样吐出来 ——
               界面上就是一串 `2026-10-08T02:37:57.980Z`。 -->
          <template #createdAt-cell="{ row }">
            <span class="text-muted whitespace-nowrap">{{ formatTime(row.original.createdAt) }}</span>
          </template>
          <template #download-cell="{ row }">
            <UButton size="xs" variant="ghost" icon="i-lucide-download" @click="downloadFile(row.original.id)">下载</UButton>
          </template>
        </UTable>
      </div>
      <div v-if="printTotal > 0" class="flex items-center justify-between gap-2 mt-3">
        <span class="text-xs text-muted">共 {{ printTotal }} 条记录</span>
        <UPagination
          v-if="printPages > 1"
          v-model:page="printPage"
          :total="printTotal"
          :items-per-page="printPager.pageSize"
          size="sm"
        />
      </div>
    </UCard>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { readError, readJSON } from '../../utils/api'
import { formatTime, userLabel } from '../../utils/format'
import { usePagedRows } from '../../utils/tablePage'
import AdminTabs from '../../components/admin/AdminTabs.vue'

const emit = defineEmits(['logout'])

const printFilters = ref({ username: '', start: '', end: '' })
const printRecords = ref([])
const loadError = ref('')

const printPager = usePagedRows(printRecords)
const { page: printPage, pageCount: printPages, total: printTotal, rows: printRecordsPaged } = printPager

const printColumns = [
  { accessorKey: 'createdAt', header: '时间' },
  { accessorKey: 'username', header: '用户' },
  { accessorKey: 'filename', header: '文件' },
  { accessorKey: 'pages', header: '页数' },
  { accessorKey: 'status', header: '状态' },
  { id: 'download', header: '下载' }
]

function downloadFile(id) {
  window.open(`/api/print-records/${id}/file`, '_blank')
}

async function loadPrintRecords() {
  loadError.value = ''
  try {
    const params = new URLSearchParams()
    if (printFilters.value.username) params.set('username', printFilters.value.username)
    if (printFilters.value.start) params.set('start', printFilters.value.start)
    if (printFilters.value.end) params.set('end', printFilters.value.end)
    const resp = await fetch(`/api/admin/print-records?${params.toString()}`, { credentials: 'include' })
    if (!resp.ok) {
      if (resp.status === 401) {
        emit('logout')
        return
      }
      // 🚫 失败时不能只是 return —— 表格会渲染成「暂无数据」，
      // 管理员会以为记录被清空了。失败必须有可见的错误态。
      loadError.value = await readError(resp)
      return
    }
    printRecords.value = await readJSON(resp)
  } catch (e) {
    loadError.value = `无法连接到服务端：${e.message || e}`
  }
}

// 「查询」= 翻回第一页 + 重查。
//
// 🚫 拆成独立函数、而不是给 loader 加个 `reset = true` 参数：
// 模板里写 `@click="loadPrintRecords"` 时 Vue 会把 MouseEvent 当第一个实参
// 传进去，带可选参数的签名会被事件对象顶成真值（AGENTS.md 里记过两次，
// 两次都是「点了没反应」这种最难查的症状）。所以公开入口一律无参。
function searchPrintRecords() {
  printPager.resetPage()
  return loadPrintRecords()
}

onMounted(loadPrintRecords)
</script>
