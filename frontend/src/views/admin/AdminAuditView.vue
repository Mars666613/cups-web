<template>
  <div class="p-3 sm:p-4 md:p-6 space-y-4 md:space-y-6">
    <AdminTabs current="/admin/audit" />

    <!-- 操作审计 -->
    <UCard>
      <template #header>
        <h2 class="text-xl font-bold flex items-center gap-2">
          <UIcon name="i-lucide-history" class="w-5 h-5" />
          操作审计
        </h2>
      </template>

      <div class="flex flex-wrap gap-3 items-end mb-4">
        <USelect
          v-model="filters.action"
          :items="actionItems"
          value-key="value"
          label-key="label"
          class="w-52"
        />
        <UButton variant="outline" icon="i-lucide-search" @click="searchAudit">查询</UButton>
        <UButton variant="ghost" icon="i-lucide-refresh-cw" :loading="loading" @click="loadAudit">刷新</UButton>
      </div>

      <div v-if="loadError" class="flex items-start gap-2 mb-4">
        <UAlert
          class="flex-1"
          color="error"
          variant="soft"
          icon="i-lucide-alert-triangle"
          title="操作审计加载失败"
          :description="loadError"
        />
        <UButton size="xs" variant="outline" icon="i-lucide-refresh-cw" @click="loadAudit">重试</UButton>
      </div>

      <div class="overflow-x-auto">
        <UTable :columns="columns" :data="entries">
          <template #createdAt-cell="{ row }">
            <span class="text-muted whitespace-nowrap">{{ formatTime(row.original.createdAt) }}</span>
          </template>
          <template #actor-cell="{ row }">
            <span class="whitespace-nowrap">{{ userLabel(row.original.actor) }}</span>
          </template>
          <template #action-cell="{ row }">
            <UBadge color="neutral" variant="subtle" size="sm">{{ row.original.actionLabel }}</UBadge>
          </template>
          <template #target-cell="{ row }">
            <span class="text-muted">{{ row.original.target || '—' }}</span>
          </template>
          <template #summary-cell="{ row }">
            <span class="whitespace-pre-wrap">{{ row.original.summary }}</span>
          </template>
          <template #actions-cell="{ row }">
            <UButton size="xs" variant="ghost" icon="i-lucide-eye" @click="showDetail(row.original)">详情</UButton>
          </template>
        </UTable>
      </div>

      <div v-if="total > 0" class="flex items-center justify-between gap-2 mt-3">
        <span class="text-xs text-muted">共 {{ total }} 条记录</span>
        <UPagination
          v-if="pageCount > 1"
          v-model:page="page"
          :total="total"
          :items-per-page="pageSize"
          size="sm"
        />
      </div>
      <div v-else-if="!loading && !loadError" class="text-sm text-muted mt-2">还没有管理操作记录。</div>

      <div class="text-sm text-muted mt-2">
        只读日志，只增不改：记录管理员对系统设置、计费价目、用户、订单退款、取件终端的每一次修改。
        <b>没有改动就不会有记录</b> —— 点一次保存但什么都没变，不会留下噪声，免得把真正的那一条淹掉。
      </div>
    </UCard>

    <UModal v-model:open="detailOpen">
      <template #content>
        <div class="p-6 space-y-4 max-w-2xl">
          <h3 class="text-lg font-semibold">{{ detail ? detail.actionLabel : '操作详情' }}</h3>
          <div v-if="detail" class="space-y-1 text-sm">
            <div><span class="text-muted">时间：</span>{{ formatTime(detail.createdAt) }}</div>
            <div><span class="text-muted">操作人：</span>{{ detail.actor }}</div>
            <div v-if="detail.target"><span class="text-muted">对象：</span>{{ detail.target }}</div>
            <div class="whitespace-pre-wrap"><span class="text-muted">变更：</span>{{ detail.summary }}</div>
          </div>
          <div v-if="detailChanges.length" class="space-y-2">
            <div
              v-for="c in detailChanges"
              :key="c.field"
              class="rounded border border-default p-2 text-sm space-y-0.5"
            >
              <div class="font-mono text-xs text-muted">{{ c.field }}</div>
              <div class="text-muted line-through break-all">{{ c.before }}</div>
              <div class="break-all">{{ c.after }}</div>
            </div>
          </div>
          <div v-else class="text-sm text-muted">该操作没有可显示的字段级前后值。</div>
          <div class="flex justify-end">
            <UButton variant="ghost" @click="detailOpen = false">关闭</UButton>
          </div>
        </div>
      </template>
    </UModal>
  </div>
</template>

<script setup>
import { ref, computed, watch, onMounted } from 'vue'
import { readError, readJSON } from '../../utils/api'
import { formatTime, userLabel } from '../../utils/format'
import { FILTER_ALL } from '../../utils/adminFilters'
import AdminTabs from '../../components/admin/AdminTabs.vue'

const emit = defineEmits(['logout'])

const entries = ref([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(100)
const actions = ref({})
const loading = ref(false)
const loadError = ref('')

const filters = ref({ action: FILTER_ALL })

const detailOpen = ref(false)
const detail = ref(null)
const detailChanges = ref([])

// 动作下拉的候选项【从服务端返回的 actions 映射生成】，不写死一份清单。
//
// 动作键随功能增加（这一轮就一次加了 11 个），写死在前端的话，新加的那一类
// 永远不会出现在下拉里 —— 而管理员打开审计页恰恰就是想「看看最近都动了些什么」。
// 值是后端 auditActionLabel 给的中文，两处靠 internal/store/audit.go 的常量对齐。
const actionItems = computed(() => [
  { label: '全部操作', value: FILTER_ALL },
  ...Object.entries(actions.value).map(([value, label]) => ({ label, value }))
])

const columns = [
  { accessorKey: 'createdAt', header: '时间' },
  { accessorKey: 'actor', header: '操作人' },
  { accessorKey: 'action', header: '操作' },
  { accessorKey: 'target', header: '对象' },
  { accessorKey: 'summary', header: '变更' },
  { id: 'actions', header: '详情' }
]

const pageCount = computed(() => Math.max(1, Math.ceil(total.value / pageSize.value)))

// 请求序号：连点翻页时旧响应可能后到，会把新一页的数据盖回去。
// 只认最后一次发出的那个请求，其余一律丢弃（也包括它的 finally）。
//
// ⚠️ 这里用序号而不是 PrintView 那种 in-flight 计数：那个页面的轮询是
// 「同一份数据反复拉」，跳过重复请求就够了；这边每次翻页要的是【不同的一页】，
// 跳过反而会让用户点不动，正确做法是让最后一次赢。
let auditSeq = 0

async function loadAudit() {
  const seq = ++auditSeq
  loading.value = true
  loadError.value = ''
  try {
    const params = new URLSearchParams()
    if (filters.value.action !== FILTER_ALL) params.set('action', filters.value.action)
    params.set('page', String(page.value))
    const resp = await fetch(`/api/admin/audit?${params}`, { credentials: 'include' })
    if (seq !== auditSeq) return
    if (!resp.ok) {
      if (resp.status === 401) {
        emit('logout')
        return
      }
      // 🚫 失败时不能只是 return —— 表格会渲染成「还没有管理操作记录」，
      // 管理员会以为真的没人动过任何东西。一个审计页给出「什么都没发生」的
      // 错觉，比它直接报错危险得多。失败必须有可见的错误态。
      loadError.value = await readError(resp)
      return
    }
    const data = await readJSON(resp)
    if (seq !== auditSeq) return
    entries.value = data?.entries || []
    total.value = data?.total || 0
    if (data?.pageSize) pageSize.value = data.pageSize
    if (data?.actions) actions.value = data.actions
    // 服务端不做页码钳制，退到空页就自己退回来（与 utils/tablePage.js 同一条理由：
    // 记录变少后停在空页，看起来就像数据没了）。
    const last = Math.max(1, Math.ceil(total.value / pageSize.value))
    if (page.value > last) page.value = last
  } catch (e) {
    if (seq === auditSeq) loadError.value = `无法连接到服务端：${e.message || e}`
  } finally {
    if (seq === auditSeq) loading.value = false
  }
}

// 「查询」= 翻回第一页 + 重查。
//
// 已经在第一页就直接查；否则改 page、由下面的 watch 去查 —— 两条路各只发
// 一次请求，不会重复加载。公开入口保持无参：模板里 @click="searchAudit"
// 不带括号时 Vue 会把 MouseEvent 当第一个实参传进来（AGENTS.md 记过两次）。
function searchAudit() {
  if (page.value !== 1) {
    page.value = 1
    return
  }
  return loadAudit()
}

// diffEntry 把 before_json / after_json 拍成「字段 → 前值 / 后值」的卡片列表。
//
// 两份 JSON 都是记录写入侧 recordAudit 生成的 {字段: 值} 映射，键一一对应，
// 所以取并集即可（新增/删除的字段只会出现在一侧）。
function diffEntry(entry) {
  const before = parseJSONMap(entry.beforeJson)
  const after = parseJSONMap(entry.afterJson)
  const fields = Array.from(new Set([...Object.keys(before), ...Object.keys(after)]))
  return fields.map((field) => ({
    field,
    before: displayValue(before[field]),
    after: displayValue(after[field])
  }))
}

const EMPTY_TEXT = '(空)'

function parseJSONMap(raw) {
  if (!raw) return {}
  try {
    const v = JSON.parse(raw)
    return v && typeof v === 'object' ? v : {}
  } catch {
    // 坏 JSON 不能把弹窗打空：退化成「没有字段级前后值」，摘要那行还在。
    return {}
  }
}

function displayValue(v) {
  if (v === undefined || v === null) return EMPTY_TEXT
  if (typeof v === 'string') return v === '' ? EMPTY_TEXT : v
  if (typeof v === 'object') return JSON.stringify(v)
  return String(v)
}

function showDetail(entry) {
  detail.value = entry
  detailChanges.value = diffEntry(entry)
  detailOpen.value = true
}

// 翻页由 watch 驱动：UPagination 改了 page，这里去拉对应的一页。
// 放在 setup 末尾 —— watch 的源参数是立即求值的，源里的标识符必须已经声明
// （scripts/check-frontend.js 的检查 3 盯着这条，违了就是整页白屏）。
watch(page, () => {
  loadAudit()
})

onMounted(loadAudit)
</script>
