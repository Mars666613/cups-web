<template>
  <div class="p-3 sm:p-4 md:p-6 max-w-5xl mx-auto space-y-4">
    <div class="flex items-center justify-between gap-2 flex-wrap">
      <h1 class="text-lg font-bold flex items-center gap-2">
        <UIcon name="i-lucide-calendar-clock" class="w-5 h-5 text-primary" />
        定时打印
      </h1>
      <div class="flex items-center gap-2">
        <UButton
          variant="ghost"
          size="xs"
          icon="i-lucide-refresh-cw"
          :loading="loading"
          @click="loadRecords"
        >刷新</UButton>
        <UButton
          size="sm"
          color="primary"
          icon="i-lucide-plus"
          @click="openCreate"
        >新建定时任务</UButton>
      </div>
    </div>

    <UAlert
      color="neutral"
      variant="soft"
      icon="i-lucide-info"
      title="使用说明"
      description="任务按服务器本地时区触发。宕机期间错过的执行点会被标记为跳过，不会补跑。"
    />

    <div v-if="loading" class="flex items-center justify-center py-10 text-muted gap-2">
      <UIcon name="i-lucide-loader-circle" class="w-5 h-5 animate-spin" />
      加载中…
    </div>
    <div v-else-if="!records.length" class="text-center py-16 text-muted">
      <UIcon name="i-lucide-calendar-off" class="w-10 h-10 mx-auto mb-2 opacity-40" />
      <div>还没有定时任务</div>
    </div>
    <div v-else class="space-y-3">
      <UCard v-for="rec in records" :key="rec.id" class="overflow-hidden">
        <div class="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-3">
          <div class="min-w-0 space-y-1">
            <div class="flex items-center gap-2 flex-wrap">
              <span class="font-medium truncate">{{ rec.name || rec.filename }}</span>
              <UBadge
                :color="rec.enabled ? 'success' : 'neutral'"
                :variant="rec.enabled ? 'subtle' : 'outline'"
                size="sm"
              >{{ rec.enabled ? '启用' : '已停用' }}</UBadge>
              <UBadge color="primary" variant="outline" size="sm">
                {{ scheduleLabel(rec) }}
              </UBadge>
              <UBadge
                v-if="rec.lastStatus"
                :color="lastStatusColor(rec.lastStatus)"
                variant="soft"
                size="sm"
              >{{ lastStatusLabel(rec.lastStatus) }}</UBadge>
            </div>
            <div class="text-xs text-muted flex flex-wrap gap-x-4 gap-y-0.5">
              <span>文件：{{ rec.filename }}</span>
              <span>打印机：{{ shortPrinter(rec.printerUri) }}</span>
              <span>下次：{{ formatTime(rec.nextRunAt) }}</span>
              <span v-if="rec.lastRunAt">上次：{{ formatTime(rec.lastRunAt) }}</span>
            </div>
            <div v-if="rec.lastError" class="text-xs text-error truncate">
              上次错误：{{ rec.lastError }}
            </div>
          </div>
          <div class="flex items-center gap-1 flex-shrink-0">
            <UButton size="xs" variant="ghost" icon="i-lucide-play" :loading="rec._running" @click="runNow(rec)">立即</UButton>
            <UButton size="xs" variant="ghost" :icon="rec.enabled ? 'i-lucide-pause' : 'i-lucide-play-circle'" @click="toggleEnabled(rec)">
              {{ rec.enabled ? '停用' : '启用' }}
            </UButton>
            <UButton size="xs" variant="ghost" icon="i-lucide-pencil" @click="openEdit(rec)">编辑</UButton>
            <UButton size="xs" variant="ghost" color="error" icon="i-lucide-trash-2" @click="confirmDelete(rec)">删除</UButton>
          </div>
        </div>
      </UCard>
    </div>

    <!-- 新建 / 编辑抽屉 -->
    <UModal v-model:open="modalOpen" :title="editing ? '编辑定时任务' : '新建定时任务'" :ui="{ content: 'sm:max-w-2xl' }">
      <template #body>
        <div class="space-y-3">
          <UFormField label="任务名称（可选）">
            <UInput v-model="form.name" placeholder="例如：每周会议纪要" />
          </UFormField>

          <UFormField v-if="!editing" label="文件" required>
            <input
              type="file"
              class="block w-full text-sm text-default file:mr-3 file:py-1.5 file:px-3 file:rounded-md file:border-0 file:bg-primary/10 file:text-primary hover:file:bg-primary/20"
              @change="onFilePick"
            />
            <div v-if="form.file" class="text-xs text-muted mt-1 truncate">
              已选择：{{ form.file.name }}（{{ formatSize(form.file.size) }}）
            </div>
          </UFormField>
          <UFormField v-else label="文件">
            <div class="text-sm text-muted truncate">{{ form.filename }}（更换文件请重新创建任务）</div>
          </UFormField>

          <UFormField label="打印机" required>
            <USelect
              v-model="form.printerUri"
              :items="printerItems"
              value-key="value"
              label-key="label"
              placeholder="选择打印机"
              icon="i-lucide-printer"
            />
          </UFormField>

          <UFormField label="触发方式" required>
            <USelect
              v-model="form.scheduleType"
              :items="scheduleTypeItems"
              value-key="value"
              label-key="label"
            />
          </UFormField>

          <UFormField v-if="form.scheduleType === 'once'" label="执行时间" required>
            <UInput v-model="form.runAtLocal" type="datetime-local" />
          </UFormField>
          <template v-else>
            <UFormField label="每日时间" required>
              <UInput v-model="form.scheduleTime" type="time" />
            </UFormField>
            <UFormField v-if="form.scheduleType === 'weekly'" label="星期" required>
              <USelect
                v-model.number="form.scheduleWeekday"
                :items="weekdayItems"
                value-key="value"
                label-key="label"
              />
            </UFormField>
            <UFormField v-if="form.scheduleType === 'monthly'" label="日期" required>
              <UInput v-model.number="form.scheduleDay" type="number" min="1" max="31" />
              <template #hint>若某月没有该日期（如 2 月 30 日），当月自动跳过</template>
            </UFormField>
          </template>

          <UFormField label="份数">
            <UInput v-model.number="form.copies" type="number" min="1" max="99" />
          </UFormField>

          <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
            <UFormField label="纸张">
              <USelect v-model="form.paperSize" :items="paperSizeItems" value-key="value" label-key="label" />
            </UFormField>
            <UFormField label="方向">
              <USelect v-model="form.orientation" :items="orientationItems" value-key="value" label-key="label" />
            </UFormField>
            <UFormField label="双面">
              <USelect v-model="form.duplex" :items="duplexItems" value-key="value" label-key="label" />
            </UFormField>
            <UFormField label="色彩">
              <USelect v-model="form.color" :items="colorItems" value-key="value" label-key="label" />
            </UFormField>
          </div>

          <UFormField label="水印文字（可选）">
            <UInput v-model="form.watermarkText" placeholder="留空表示不加水印" />
          </UFormField>
        </div>
      </template>
      <template #footer>
        <div class="flex justify-end gap-2 w-full">
          <UButton variant="ghost" @click="modalOpen = false">取消</UButton>
          <UButton color="primary" :loading="submitting" @click="submit">
            {{ editing ? '保存' : '创建' }}
          </UButton>
        </div>
      </template>
    </UModal>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { apiFetch, readError } from '../utils/api'

const emit = defineEmits(['logout'])
const toast = useToast()

const loading = ref(false)
const records = ref([])
const printers = ref([])

const modalOpen = ref(false)
const editing = ref(null)
const submitting = ref(false)

const form = ref(emptyForm())

function emptyForm() {
  return {
    id: null,
    name: '',
    file: null,
    filename: '',
    printerUri: '',
    scheduleType: 'once',
    runAtLocal: defaultRunAtLocal(),
    scheduleTime: '09:00',
    scheduleWeekday: 1,
    scheduleDay: 1,
    copies: 1,
    duplex: 'one-sided',
    color: 'color',
    paperSize: 'A4',
    orientation: 'portrait',
    watermarkText: ''
  }
}

// 默认新任务时间：明天上午 9 点，避免用户不改时间就直接创建导致立即触发。
function defaultRunAtLocal() {
  const d = new Date()
  d.setDate(d.getDate() + 1)
  d.setHours(9, 0, 0, 0)
  const pad = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

const scheduleTypeItems = [
  { value: 'once', label: '一次性' },
  { value: 'daily', label: '每天' },
  { value: 'weekly', label: '每周' },
  { value: 'monthly', label: '每月' }
]

const weekdayItems = [
  { value: 0, label: '周日' },
  { value: 1, label: '周一' },
  { value: 2, label: '周二' },
  { value: 3, label: '周三' },
  { value: 4, label: '周四' },
  { value: 5, label: '周五' },
  { value: 6, label: '周六' }
]

const paperSizeItems = [
  { value: 'A4', label: 'A4' },
  { value: 'A3', label: 'A3' },
  { value: 'Letter', label: 'Letter' },
  { value: '5inch', label: '5 寸相纸' },
  { value: '6inch', label: '6 寸相纸' },
  { value: '7inch', label: '7 寸相纸' },
  { value: '8inch', label: '8 寸相纸' },
  { value: '10inch', label: '10 寸相纸' }
]
const orientationItems = [
  { value: 'portrait', label: '纵向' },
  { value: 'landscape', label: '横向' }
]
const duplexItems = [
  { value: 'one-sided', label: '单面' },
  { value: 'two-sided-long-edge', label: '双面（长边）' }
]
const colorItems = [
  { value: 'color', label: '彩色' },
  { value: 'monochrome', label: '黑白' }
]

const printerItems = computed(() =>
  printers.value.map((p) => ({ value: p.uri || p.deviceUri || p.name, label: p.name || p.uri }))
)

function shortPrinter(uri) {
  if (!uri) return ''
  const idx = uri.lastIndexOf('/')
  return idx >= 0 ? uri.slice(idx + 1) : uri
}

function formatTime(iso) {
  if (!iso) return '—'
  try {
    return new Date(iso).toLocaleString()
  } catch (e) {
    return iso
  }
}

function formatSize(bytes) {
  if (!bytes) return ''
  const units = ['B', 'KB', 'MB', 'GB']
  let n = bytes
  let i = 0
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++ }
  return `${n.toFixed(1)} ${units[i]}`
}

function scheduleLabel(rec) {
  switch (rec.scheduleType) {
    case 'once': return '一次性'
    case 'daily': return `每天 ${rec.scheduleTime}`
    case 'weekly': return `每${weekdayLabel(rec.scheduleWeekday)} ${rec.scheduleTime}`
    case 'monthly': return `每月 ${rec.scheduleDay} 日 ${rec.scheduleTime}`
    default: return rec.scheduleType
  }
}
function weekdayLabel(w) {
  return ['周日', '周一', '周二', '周三', '周四', '周五', '周六'][((w % 7) + 7) % 7]
}
function lastStatusLabel(s) {
  if (s === 'printed') return '上次成功'
  if (s === 'failed') return '上次失败'
  if (s === 'skipped') return '错过窗口'
  return s
}
function lastStatusColor(s) {
  if (s === 'printed') return 'success'
  if (s === 'failed') return 'error'
  if (s === 'skipped') return 'warning'
  return 'neutral'
}

async function loadRecords() {
  loading.value = true
  try {
    const resp = await apiFetch('/api/scheduled-prints', {}, () => emit('logout'))
    if (!resp.ok) throw new Error(await readError(resp))
    records.value = await resp.json()
  } catch (e) {
    toast.add({ title: '加载失败', description: e.message, color: 'error', icon: 'i-lucide-x-circle' })
  } finally {
    loading.value = false
  }
}

async function loadPrinters() {
  try {
    const resp = await apiFetch('/api/printers', {}, () => emit('logout'))
    if (!resp.ok) return
    printers.value = await resp.json()
  } catch (e) {
    // 静默失败：定时任务表单没有打印机数据时会给出必填提示
  }
}

function openCreate() {
  editing.value = null
  form.value = emptyForm()
  if (printerItems.value.length) {
    form.value.printerUri = printerItems.value[0].value
  }
  modalOpen.value = true
}

function openEdit(rec) {
  editing.value = rec
  form.value = {
    id: rec.id,
    name: rec.name || '',
    file: null,
    filename: rec.filename,
    printerUri: rec.printerUri,
    scheduleType: rec.scheduleType,
    runAtLocal: rec.runAt ? toLocalInput(rec.runAt) : defaultRunAtLocal(),
    scheduleTime: rec.scheduleTime || '09:00',
    scheduleWeekday: rec.scheduleWeekday,
    scheduleDay: rec.scheduleDay || 1,
    copies: rec.copies || 1,
    duplex: rec.isDuplex ? 'two-sided-long-edge' : 'one-sided',
    color: rec.isColor ? 'color' : 'monochrome',
    paperSize: rec.paperSize || 'A4',
    orientation: rec.orientation || 'portrait',
    watermarkText: rec.watermarkText || ''
  }
  modalOpen.value = true
}

// toLocalInput 把服务器返回的 UTC RFC3339 转成 <input type="datetime-local"> 需要的
// 本地时区 YYYY-MM-DDTHH:mm 字符串。用户在本地看到的日期时间与实际触发对齐。
function toLocalInput(iso) {
  const d = new Date(iso)
  const pad = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function onFilePick(e) {
  const files = e.target?.files
  form.value.file = files && files.length ? files[0] : null
}

async function submit() {
  const v = form.value
  if (!v.printerUri) { toast.add({ title: '请选择打印机', color: 'warning' }); return }
  if (!editing.value && !v.file) { toast.add({ title: '请选择文件', color: 'warning' }); return }
  if (v.scheduleType === 'once' && !v.runAtLocal) { toast.add({ title: '请选择执行时间', color: 'warning' }); return }

  submitting.value = true
  try {
    if (editing.value) {
      await saveEdit(v)
    } else {
      await createNew(v)
    }
    modalOpen.value = false
    await loadRecords()
  } catch (e) {
    toast.add({ title: '保存失败', description: e.message, color: 'error', icon: 'i-lucide-x-circle' })
  } finally {
    submitting.value = false
  }
}

async function createNew(v) {
  const fd = new FormData()
  fd.append('file', v.file, v.file.name)
  fd.append('name', v.name)
  fd.append('printer', v.printerUri)
  fd.append('copies', String(v.copies))
  fd.append('duplex', v.duplex === 'two-sided-long-edge' ? 'true' : 'false')
  fd.append('color', v.color === 'color' ? 'true' : 'false')
  fd.append('orientation', v.orientation)
  fd.append('paper_size', v.paperSize)
  fd.append('paper_type', 'plain')
  fd.append('media_source', 'auto')
  fd.append('print_scaling', 'fit')
  fd.append('page_set', 'all')
  fd.append('number_up', '1')
  fd.append('number_up_layout', 'lrtb')
  fd.append('page_border', 'none')
  if (v.watermarkText) fd.append('watermark_text', v.watermarkText)
  fd.append('schedule_type', v.scheduleType)
  if (v.scheduleType === 'once') {
    fd.append('run_at', new Date(v.runAtLocal).toISOString())
  } else {
    fd.append('schedule_time', v.scheduleTime)
    fd.append('schedule_weekday', String(v.scheduleWeekday))
    fd.append('schedule_day', String(v.scheduleDay))
  }
  const resp = await apiFetch('/api/scheduled-prints', { method: 'POST', body: fd }, () => emit('logout'))
  if (!resp.ok) throw new Error(await readError(resp))
  toast.add({ title: '已创建定时任务', color: 'success', icon: 'i-lucide-check-circle' })
}

async function saveEdit(v) {
  const payload = {
    name: v.name,
    printerUri: v.printerUri,
    isDuplex: v.duplex === 'two-sided-long-edge',
    isColor: v.color === 'color',
    copies: v.copies,
    orientation: v.orientation,
    paperSize: v.paperSize,
    paperType: 'plain',
    mediaSource: 'auto',
    printScaling: 'fit',
    pageRange: '',
    pageSet: 'all',
    mirror: false,
    watermarkText: v.watermarkText,
    numberUp: 1,
    numberUpLayout: 'lrtb',
    pageBorder: 'none',
    scheduleType: v.scheduleType,
    scheduleTime: v.scheduleTime,
    scheduleWeekday: v.scheduleWeekday,
    scheduleDay: v.scheduleDay,
    runAt: v.scheduleType === 'once' ? new Date(v.runAtLocal).toISOString() : ''
  }
  const resp = await apiFetch(`/api/scheduled-prints/${v.id}`, {
    method: 'PUT',
    body: JSON.stringify(payload)
  }, () => emit('logout'))
  if (!resp.ok) throw new Error(await readError(resp))
  toast.add({ title: '已更新', color: 'success', icon: 'i-lucide-check-circle' })
}

async function runNow(rec) {
  rec._running = true
  try {
    const resp = await apiFetch(`/api/scheduled-prints/${rec.id}/run`, { method: 'POST' }, () => emit('logout'))
    if (!resp.ok) throw new Error(await readError(resp))
    toast.add({ title: '已发送到打印机', color: 'success', icon: 'i-lucide-check-circle' })
    await loadRecords()
  } catch (e) {
    toast.add({ title: '立即执行失败', description: e.message, color: 'error', icon: 'i-lucide-x-circle' })
  } finally {
    rec._running = false
  }
}

async function toggleEnabled(rec) {
  try {
    const resp = await apiFetch(`/api/scheduled-prints/${rec.id}`, {
      method: 'PUT',
      body: JSON.stringify(buildPayloadFromRecord(rec, { enabled: !rec.enabled }))
    }, () => emit('logout'))
    if (!resp.ok) throw new Error(await readError(resp))
    await loadRecords()
  } catch (e) {
    toast.add({ title: '操作失败', description: e.message, color: 'error', icon: 'i-lucide-x-circle' })
  }
}

async function confirmDelete(rec) {
  if (!confirm(`确认删除任务「${rec.name || rec.filename}」？源文件也会一并清理，无法恢复。`)) return
  try {
    const resp = await apiFetch(`/api/scheduled-prints/${rec.id}`, { method: 'DELETE' }, () => emit('logout'))
    if (!resp.ok) throw new Error(await readError(resp))
    toast.add({ title: '已删除', color: 'success', icon: 'i-lucide-check-circle' })
    await loadRecords()
  } catch (e) {
    toast.add({ title: '删除失败', description: e.message, color: 'error', icon: 'i-lucide-x-circle' })
  }
}

function buildPayloadFromRecord(rec, overrides = {}) {
  return {
    name: rec.name || '',
    printerUri: rec.printerUri,
    enabled: rec.enabled,
    isDuplex: rec.isDuplex,
    isColor: rec.isColor,
    copies: rec.copies || 1,
    orientation: rec.orientation || 'portrait',
    paperSize: rec.paperSize || 'A4',
    paperType: rec.paperType || 'plain',
    mediaSource: rec.mediaSource || 'auto',
    printScaling: rec.printScaling || 'fit',
    pageRange: rec.pageRange || '',
    pageSet: rec.pageSet || 'all',
    mirror: !!rec.mirror,
    watermarkText: rec.watermarkText || '',
    numberUp: rec.numberUp || 1,
    numberUpLayout: rec.numberUpLayout || 'lrtb',
    pageBorder: rec.pageBorder || 'none',
    scheduleType: rec.scheduleType,
    scheduleTime: rec.scheduleTime || '09:00',
    scheduleWeekday: rec.scheduleWeekday || 0,
    scheduleDay: rec.scheduleDay || 1,
    runAt: rec.runAt || '',
    ...overrides
  }
}

onMounted(async () => {
  await Promise.all([loadRecords(), loadPrinters()])
})
</script>
