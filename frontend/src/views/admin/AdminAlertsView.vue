<template>
  <div class="p-3 sm:p-4 md:p-6 space-y-4 md:space-y-6">
    <AdminTabs current="/admin/alerts" />

    <!-- ── 巡检与通知 ── -->
    <UCard>
      <template #header>
        <h2 class="text-xl font-bold flex items-center gap-2">
          <UIcon name="i-lucide-bell-ring" class="w-5 h-5" />
          设备异常告警
        </h2>
      </template>

      <div v-if="loadError" class="flex items-start gap-2 mb-4">
        <UAlert class="flex-1" color="error" variant="soft" icon="i-lucide-alert-triangle" title="配置加载失败" :description="loadError" />
        <UButton size="xs" variant="outline" icon="i-lucide-refresh-cw" @click="loadSettings">重试</UButton>
      </div>

      <div class="grid grid-cols-1 md:grid-cols-4 gap-3 items-end">
        <div class="md:col-span-4">
          <label class="flex items-center gap-2 cursor-pointer">
            <USwitch v-model="settings.enabled" />
            <span class="text-sm font-medium">开启设备异常巡检</span>
          </label>
          <p class="text-xs text-muted mt-1 ml-12">
            每分钟直连每台打印机问一次状态。缺纸、卡纸、门没关、耗材耗尽、设备不可达都会被记下来，
            并按下面配置的通知地址发消息。<b>关闭 = 整个功能不做</b>（不巡检、不记事件、不通知）。
          </p>
        </div>

        <div class="md:col-span-2">
          <label class="block text-sm font-medium mb-1">通知地址（webhook）</label>
          <UInput v-model="settings.webhookUrl" placeholder="https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=…" />
        </div>
        <div>
          <label class="block text-sm font-medium mb-1">通知渠道</label>
          <USelect v-model="settings.webhookKind" :items="kindItems" value-key="value" label-key="label" />
        </div>
        <div>
          <label class="block text-sm font-medium mb-1">重发间隔 (分钟)</label>
          <UInput type="number" step="1" min="0" max="1440" v-model="settings.repeatMinutes" placeholder="60" />
        </div>
        <div>
          <label class="block text-sm font-medium mb-1">耗材不足阈值 (%)</label>
          <UInput type="number" step="1" min="0" max="100" v-model="settings.supplyLowPercent" placeholder="10" />
        </div>

        <div class="flex items-end gap-2 md:col-span-4">
          <UButton color="primary" icon="i-lucide-save" :loading="saving" :disabled="saving || !!loadError" @click="saveSettings">保存</UButton>
          <UButton variant="outline" icon="i-lucide-send" :loading="testing" :disabled="testing" @click="sendTest">发送测试消息</UButton>
        </div>
      </div>

      <div class="text-sm text-muted mt-3 space-y-1">
        <p>
          <b>先点「发送测试消息」再打开开关。</b>配 webhook 就是往一个输入框里粘一串 URL，粘错了不会有任何反馈 ——
          而「开关打开、消息发不出去」比不告警更危险：你以为有人在看着。测试消息用的是上面这两个框里的当前值，不必先保存。
        </p>
        <p>
          渠道选「自动」时按地址主机名判断（企业微信 / 钉钉 / 飞书），认不出来按通用 <code>{"text":"…"}</code> 发。
          重发间隔是「同一个毛病一直没好，隔多久再提醒一次」，填 0 表示只提醒一次。
        </p>
      </div>
    </UCard>

    <!-- ── 告警记录 ── -->
    <UCard>
      <template #header>
        <h2 class="text-xl font-bold flex items-center gap-2">
          <UIcon name="i-lucide-history" class="w-5 h-5" />
          告警记录
          <UBadge v-if="openCount > 0" color="error" variant="subtle" size="sm">{{ openCount }} 条未恢复</UBadge>
        </h2>
      </template>

      <div class="flex flex-wrap gap-3 items-end mb-4">
        <USelect v-model="statusFilter" :items="statusItems" value-key="value" label-key="label" class="w-40" />
        <UButton variant="outline" icon="i-lucide-search" @click="searchEvents">查询</UButton>
        <UButton variant="ghost" icon="i-lucide-refresh-cw" :loading="loadingList" @click="loadEvents">刷新</UButton>
      </div>

      <div v-if="listError" class="flex items-start gap-2 mb-4">
        <UAlert class="flex-1" color="error" variant="soft" icon="i-lucide-alert-triangle" title="告警记录加载失败" :description="listError" />
        <UButton size="xs" variant="outline" icon="i-lucide-refresh-cw" @click="loadEvents">重试</UButton>
      </div>

      <div class="overflow-x-auto">
        <UTable :columns="columns" :data="events">
          <template #startedAt-cell="{ row }">
            <span class="text-muted whitespace-nowrap">{{ formatTime(row.original.startedAt) }}</span>
          </template>
          <template #printerName-cell="{ row }">
            <span :title="row.original.printerUri" class="whitespace-nowrap">{{ row.original.printerName }}</span>
          </template>
          <template #kind-cell="{ row }">
            <UBadge :color="severityColor(row.original.severity)" variant="subtle" size="sm">
              {{ row.original.kindLabel }}
            </UBadge>
          </template>
          <template #detail-cell="{ row }">
            <span class="text-xs text-muted break-all">{{ row.original.detail || '—' }}</span>
          </template>
          <template #status-cell="{ row }">
            <div class="flex flex-wrap gap-1">
              <UBadge v-if="!row.original.resolvedAt" color="error" variant="subtle" size="sm">未恢复</UBadge>
              <UBadge v-else color="neutral" variant="subtle" size="sm">已恢复</UBadge>
              <UBadge v-if="row.original.muted" color="warning" variant="subtle" size="sm">已静音</UBadge>
            </div>
          </template>
          <template #actions-cell="{ row }">
            <UButton
              size="xs"
              variant="ghost"
              icon="i-lucide-check"
              :disabled="!!row.original.resolvedAt"
              @click="confirmResolve(row.original)"
            >已处理</UButton>
          </template>
        </UTable>
      </div>

      <div v-if="total > 0" class="flex items-center justify-between gap-2 mt-3">
        <span class="text-xs text-muted">共 {{ total }} 条记录</span>
        <UPagination v-if="pageCount > 1" v-model:page="page" :total="total" :items-per-page="pageSize" size="sm" />
      </div>
      <div v-else-if="!loadingList && !listError" class="text-sm text-muted mt-2">还没有告警记录。</div>

      <div class="text-sm text-muted mt-2">
        「已处理」= <b>我知道这个毛病了，别再提醒我这一类</b>。它不只是把这一条关掉：毛病还在的话，下一轮巡检会重新发现它，
        所以同时会静音「这台机器的这一类告警」。静音会在该类别<b>先彻底消失一次</b>之后自动解除 —— 之后再犯就是新问题，照常通知。
      </div>
    </UCard>

    <UModal v-model:open="showResolveConfirm">
      <template #content>
        <div class="p-6 space-y-4">
          <h3 class="text-lg font-semibold">标记已处理</h3>
          <p v-if="pendingEvent">
            将对 <strong>{{ pendingEvent.printerName }}</strong> 的
            <strong>{{ pendingEvent.kindLabel }}</strong> 做两件事：
          </p>
          <ol class="text-sm list-decimal ml-5 space-y-1">
            <li>把这一条记录标记为已恢复。</li>
            <li>静音这一类告警：只要这个毛病还在，就不再记录、不再发通知。</li>
          </ol>
          <p class="text-xs text-muted">
            静音的解除条件是「该类别先彻底消失一次」。所以如果信号本身就是错的（比如纸盒里明明有纸，
            但打印机一直报缺纸），这一步就能让它安静下来，而机器真的再出别的问题时你仍然会收到消息。
          </p>
          <div class="flex justify-end gap-2">
            <UButton variant="ghost" @click="showResolveConfirm = false">取消</UButton>
            <UButton color="primary" :loading="resolving" @click="triggerResolve">确认</UButton>
          </div>
        </div>
      </template>
    </UModal>
  </div>
</template>

<script setup>
import { ref, computed, watch, onMounted } from 'vue'
import { getCSRF, readError, readJSON } from '../../utils/api'
import { formatTime } from '../../utils/format'
import { FILTER_ALL } from '../../utils/adminFilters'
import AdminTabs from '../../components/admin/AdminTabs.vue'

const toast = useToast()
const emit = defineEmits(['logout'])

// ── 配置 ──
const loadError = ref('')
const saving = ref(false)
const testing = ref(false)
const settings = ref({
  enabled: false,
  webhookUrl: '',
  webhookKind: 'auto',
  repeatMinutes: '60',
  supplyLowPercent: '10'
})

// 渠道只影响消息体格式。选「自动」时后端按主机名推断 ——
// 管理员粘 URL 时不必再去想「这是哪个渠道」。
const kindItems = [
  { label: '自动识别', value: 'auto' },
  { label: '企业微信', value: 'wechat' },
  { label: '钉钉', value: 'dingtalk' },
  { label: '飞书', value: 'feishu' },
  { label: '通用 JSON', value: 'generic' }
]

// ── 列表 ──
const events = ref([])
const total = ref(0)
const openCount = ref(0)
const page = ref(1)
const pageSize = ref(100)
const loadingList = ref(false)
const listError = ref('')
const statusFilter = ref(FILTER_ALL)

const statusItems = [
  { label: '全部', value: FILTER_ALL },
  { label: '未恢复', value: 'open' },
  { label: '已恢复', value: 'resolved' }
]

const columns = [
  { accessorKey: 'startedAt', header: '开始时间' },
  { accessorKey: 'printerName', header: '打印机' },
  { accessorKey: 'kind', header: '类别' },
  { accessorKey: 'detail', header: '详情' },
  { accessorKey: 'status', header: '状态' },
  { id: 'actions', header: '操作' }
]

const pageCount = computed(() => Math.max(1, Math.ceil(total.value / pageSize.value)))

const showResolveConfirm = ref(false)
const pendingEvent = ref(null)
const resolving = ref(false)

// severityColor 把「故障 / 提醒」映射成 UI 颜色。
// 认不出的级别按 neutral 显示而不是硬塞一个 error：多一条假红色会让人
// 对真正的红色麻木。
function severityColor(sev) {
  if (sev === 'error') return 'error'
  if (sev === 'warning') return 'warning'
  return 'neutral'
}

async function loadSettings() {
  loadError.value = ''
  try {
    const resp = await fetch('/api/admin/settings', { credentials: 'include' })
    if (!resp.ok) {
      if (resp.status === 401) {
        emit('logout')
        return
      }
      // 🚫 失败不能只 return：表单会停在初值，管理员照着「默认的」界面点保存，
      // 就把真实的告警配置覆盖掉了（比如把 webhook 地址清成空）。
      loadError.value = await readError(resp)
      return
    }
    const data = await readJSON(resp)
    settings.value.enabled = data.alertEnabled === true
    settings.value.webhookUrl = typeof data.alertWebhookUrl === 'string' ? data.alertWebhookUrl : ''
    settings.value.webhookKind = data.alertWebhookKind || 'auto'
    settings.value.repeatMinutes = String(data.alertRepeatMinutes ?? 60)
    settings.value.supplyLowPercent = String(data.alertSupplyLowPercent ?? 10)
  } catch (e) {
    loadError.value = `无法连接到服务端：${e.message || e}`
  }
}

// 请求序号：连点翻页时旧响应可能后到、把新一页盖回去。只认最后一次发出的。
let listSeq = 0

async function loadEvents() {
  const seq = ++listSeq
  loadingList.value = true
  listError.value = ''
  try {
    const params = new URLSearchParams()
    if (statusFilter.value !== FILTER_ALL) params.set('status', statusFilter.value)
    params.set('page', String(page.value))
    const resp = await fetch(`/api/admin/device-events?${params}`, { credentials: 'include' })
    if (seq !== listSeq) return
    if (!resp.ok) {
      if (resp.status === 401) {
        emit('logout')
        return
      }
      // 🚫 失败不能只 return：表格会渲染成「还没有告警记录」，
      // 而管理员会以为设备一切正常 —— 这是这个页面最不该给的错觉。
      listError.value = await readError(resp)
      return
    }
    const data = await readJSON(resp)
    if (seq !== listSeq) return
    events.value = data?.events || []
    total.value = data?.total || 0
    openCount.value = data?.openCount || 0
    if (data?.pageSize) pageSize.value = data.pageSize
    const last = Math.max(1, Math.ceil(total.value / pageSize.value))
    if (page.value > last) page.value = last
  } catch (e) {
    if (seq === listSeq) listError.value = `无法连接到服务端：${e.message || e}`
  } finally {
    if (seq === listSeq) loadingList.value = false
  }
}

// 「查询」= 翻回第一页 + 重查。已经在第一页就直接查，否则交给 watch(page)。
// 公开入口保持无参：模板里 @click 不带括号时 Vue 会把 MouseEvent 传进来。
function searchEvents() {
  if (page.value !== 1) {
    page.value = 1
    return
  }
  return loadEvents()
}

// saveSettings 只发告警这几项。
//
// 后端 settingsPayload 每个字段都是指针，没传的键不会被写 —— 所以这里
// 【不能】先 GET 再整体 PUT：那样两个管理员同时改不同设置时，后保存的那个
// 会把对方刚改的值覆盖回去。
async function saveSettings() {
  saving.value = true
  try {
    const payload = {
      alertEnabled: settings.value.enabled === true,
      alertWebhookUrl: (settings.value.webhookUrl || '').trim(),
      alertWebhookKind: settings.value.webhookKind,
      alertRepeatMinutes: clampInt(settings.value.repeatMinutes, 0, 1440, 60),
      alertSupplyLowPercent: clampInt(settings.value.supplyLowPercent, 0, 100, 10)
    }
    const resp = await fetch('/api/admin/settings', {
      method: 'PUT',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': getCSRF() },
      body: JSON.stringify(payload)
    })
    if (!resp.ok) {
      const msg = await readError(resp)
      toast.add({ title: '保存失败', description: msg, color: 'error', icon: 'i-lucide-x-circle' })
      if (resp.status === 401) emit('logout')
      return
    }
    toast.add({ title: '已保存', description: '告警配置已更新', color: 'success', icon: 'i-lucide-check-circle' })
    await loadSettings()
    await loadEvents()
  } finally {
    saving.value = false
  }
}

// sendTest 用【当前表单里的值】发一条测试消息，不必先保存 ——
// 配 webhook 最容易错的恰恰是那个 URL，能当场验才用得下去。
async function sendTest() {
  testing.value = true
  try {
    const resp = await fetch('/api/admin/device-events/test', {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': getCSRF() },
      body: JSON.stringify({
        webhookUrl: (settings.value.webhookUrl || '').trim(),
        webhookKind: settings.value.webhookKind
      })
    })
    if (!resp.ok) {
      // 渠道的原话直接展示：这一步的价值就在于「为什么没通」要当场看见。
      const msg = await readError(resp)
      toast.add({ title: '测试消息发送失败', description: msg, color: 'error', icon: 'i-lucide-x-circle' })
      if (resp.status === 401) emit('logout')
      return
    }
    toast.add({
      title: '已发送',
      description: '去那个群里看一眼有没有收到；收到再打开开关',
      color: 'success',
      icon: 'i-lucide-check-circle'
    })
  } finally {
    testing.value = false
  }
}

function confirmResolve(ev) {
  pendingEvent.value = ev
  showResolveConfirm.value = true
}

async function triggerResolve() {
  const ev = pendingEvent.value
  if (!ev) return
  resolving.value = true
  try {
    const resp = await fetch(`/api/admin/device-events/${ev.id}/resolve`, {
      method: 'POST',
      credentials: 'include',
      headers: { 'X-CSRF-Token': getCSRF() }
    })
    if (!resp.ok) {
      const msg = await readError(resp)
      toast.add({ title: '操作失败', description: msg, color: 'error', icon: 'i-lucide-x-circle' })
      if (resp.status === 401) emit('logout')
      return
    }
    const data = await readJSON(resp)
    toast.add({
      title: data?.wasOpen ? '已标记处理' : '已静音',
      description: data?.wasOpen
        ? '这一类告警已静音，等它彻底消失后自动解除'
        : '这条本来就已恢复；这一类告警已静音',
      color: 'success',
      icon: 'i-lucide-check-circle'
    })
    showResolveConfirm.value = false
    await loadEvents()
  } finally {
    resolving.value = false
  }
}

// clampInt 把输入框里的字符串夹到合法区间。
// 输入框可以是空的（用户全选删掉再点保存），parseInt 会给出 NaN ——
// 直接发 NaN 出去会变成 JSON null，后端当作「没传」而静默保留旧值。
function clampInt(raw, min, max, fallback) {
  const n = parseInt(raw, 10)
  if (!Number.isFinite(n)) return fallback
  return Math.min(max, Math.max(min, n))
}

// 翻页由 watch 驱动（源参数是立即求值的，所以这两条必须放在 setup 末尾 ——
// scripts/check-frontend.js 的检查 3 盯着这条，违了就是整页白屏）。
watch(page, () => {
  loadEvents()
})

onMounted(() => {
  loadSettings()
  loadEvents()
})
</script>
