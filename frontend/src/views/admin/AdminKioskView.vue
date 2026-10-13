<template>
  <div class="p-3 sm:p-4 md:p-6 space-y-4 md:space-y-6">
    <AdminTabs current="/admin/kiosk" />

    <!-- ── 总开关 ──────────────────────────────────────────────────
         放在这一页而不是「系统设置」里：开关与终端是同一件事的两半，
         从同一个入口改才不会出现「开了取件但一台终端都没配」的中间态
         （那样员工提交的任务谁也取不出来）。 -->
    <UCard>
      <template #header>
        <h2 class="text-xl font-bold flex items-center gap-2">
          <UIcon name="i-lucide-scan-line" class="w-5 h-5" />
          刷卡取件
        </h2>
      </template>

      <div class="space-y-3">
        <div class="flex items-start gap-3">
          <USwitch v-model="pullEnabled" :loading="savingToggle" @update:model-value="togglePull" />
          <div class="flex-1">
            <p class="font-medium">启用刷卡取件</p>
            <p class="text-sm text-muted">
              员工在自己电脑上提交任务后，文件先暂存在服务器上；走到打印机旁刷工牌，
              任务才真正送去打印。
            </p>
          </div>
        </div>

        <UAlert
          color="warning"
          variant="soft"
          icon="i-lucide-info"
          title="与计费、访客模式互斥"
          description="这三个开关描述的都是「谁是这个部署的打印入口」，两两不能同时开。开启取件前请先关掉计费和访客模式。"
        />

        <div v-if="pullEnabled" class="grid grid-cols-1 sm:grid-cols-2 gap-3 pt-2">
          <div>
            <label class="block text-sm font-medium mb-1">取件时限（小时）</label>
            <UInput v-model="pullTtlHours" type="number" min="1" max="720" class="w-full" />
            <p class="text-xs text-muted mt-1">
              员工超过这个时间没来取，任务作废、文件回收。默认 72 小时。
            </p>
          </div>
          <div class="flex items-end">
            <UButton color="primary" :loading="savingTtl" @click="saveTtl">保存时限</UButton>
          </div>
        </div>
      </div>
    </UCard>

    <!-- ── 终端列表 ──────────────────────────────────────────────── -->
    <UCard>
      <template #header>
        <div class="flex items-center justify-between gap-2">
          <h3 class="text-lg font-semibold flex items-center gap-2">
            <UIcon name="i-lucide-monitor" class="w-5 h-5" />
            取件终端
          </h3>
          <UButton color="primary" icon="i-lucide-plus" @click="openCreate">新增终端</UButton>
        </div>
      </template>

      <div v-if="loadError" class="flex items-start gap-2">
        <UAlert class="flex-1" color="error" variant="soft" icon="i-lucide-alert-triangle" title="读取终端列表失败" :description="loadError" />
        <UButton size="xs" variant="outline" icon="i-lucide-refresh-cw" @click="load">重试</UButton>
      </div>

      <div v-else-if="terminals.length === 0" class="text-center py-8 text-muted text-sm">
        <p>还没有配置取件终端。</p>
        <p class="mt-1">每台要支持刷卡的打印机旁边，都需要一台装读卡器的小机器。</p>
      </div>

      <div v-else class="overflow-x-auto">
        <UTable :columns="columns" :data="terminals">
          <template #name-cell="{ row }">
            <div class="min-w-0">
              <div class="flex items-center gap-2">
                <span class="font-medium truncate">{{ row.original.name }}</span>
                <UBadge :color="row.original.enabled ? 'success' : 'neutral'" variant="subtle" size="sm">
                  {{ row.original.enabled ? '启用' : '停用' }}
                </UBadge>
              </div>
              <p v-if="row.original.printerUri" class="text-xs text-muted font-mono truncate" :title="row.original.printerUri">
                {{ row.original.printerUri }}
              </p>
              <p v-else class="text-xs text-muted">仅用于绑卡，不负责取件</p>
            </div>
          </template>
          <!-- 从来没上报过的终端大概率是「还没配」或「配置错了」——
               这是无屏幕方案里唯一能看出「现场那台活没活」的地方。 -->
          <template #lastSeenAt-cell="{ row }">
            <span v-if="row.original.lastSeenAt" class="text-sm">
              {{ formatTime(row.original.lastSeenAt) }}
            </span>
            <span v-else class="text-xs text-warning">从未上线</span>
          </template>
          <template #token-cell="{ row }">
            <div class="flex items-center gap-2">
              <code class="text-xs truncate max-w-[10rem]" :title="row.original.token">{{ row.original.token }}</code>
              <UButton size="xs" variant="ghost" icon="i-lucide-copy" @click="copyToken(row.original.token)" />
            </div>
          </template>
          <template #actions-cell="{ row }">
            <div class="flex gap-2">
              <UButton size="sm" variant="ghost" icon="i-lucide-pencil" @click="openEdit(row.original)">编辑</UButton>
              <UButton size="sm" variant="ghost" icon="i-lucide-key-round" @click="showToken(row.original)">配置指引</UButton>
              <UButton size="sm" variant="outline" color="error" icon="i-lucide-trash-2" @click="confirmDelete(row.original)">删除</UButton>
            </div>
          </template>
        </UTable>
      </div>
    </UCard>

    <!-- ── 新增 / 编辑 ──────────────────────────────────────────── -->
    <UModal v-model:open="showForm" :ui="{ content: 'max-w-md' }">
      <template #content>
        <div class="p-6 space-y-4">
          <h3 class="text-lg font-semibold">{{ form.token ? '编辑终端' : '新增取件终端' }}</h3>

          <div>
            <label class="block text-sm font-medium mb-1">名称</label>
            <UInput v-model="form.name" placeholder="例如：3楼东侧" class="w-full" />
            <p class="text-xs text-muted mt-1">给你自己认的。现场排障时全靠它定位是哪台。</p>
          </div>

          <!-- 「仅用于绑卡」：绑卡只需要读一次卡的 UID，而 UID 是芯片固化的 ——
               读卡器插在哪台机器上读出来都是同一个值。所以完全可以把读卡器
               插在管理员自己的电脑上，配一台只绑卡的终端，绑卡时不用走去打印机。
               它就是 printer_uri = ''。 -->
          <label class="flex items-start gap-3 rounded-lg border border-default p-3 cursor-pointer">
            <UCheckbox v-model="form.bindOnly" class="mt-0.5" />
            <div class="flex-1">
              <p class="text-sm font-medium">仅用于绑卡（不负责取件）</p>
              <p class="text-xs text-muted mt-0.5">
                适合插在管理员电脑上的读卡器 —— 绑卡时不用走到打印机那边。
                刷卡后会提示「请先在管理界面点开始等待刷卡」。
              </p>
            </div>
          </label>

          <div v-if="!form.bindOnly">
            <label class="block text-sm font-medium mb-1">对应打印机</label>
            <USelect
              v-model="form.printerUri"
              :items="printerItems"
              value-key="value"
              label-key="label"
              placeholder="选择打印机"
              class="w-full"
              :loading="loadingPrinters"
            />
            <p class="text-xs text-muted mt-1">
              员工在【这台】打印机上提交的任务，只能在这台终端刷卡取出来。
            </p>
          </div>

          <div v-if="form.token" class="flex items-center gap-3">
            <USwitch v-model="form.enabled" />
            <span class="text-sm">启用</span>
          </div>

          <p v-if="formError" class="text-sm text-error">{{ formError }}</p>

          <div class="flex justify-end gap-2">
            <UButton variant="ghost" @click="showForm = false">取消</UButton>
            <UButton color="primary" :loading="saving" @click="submitForm">
              {{ form.token ? '保存' : '创建' }}
            </UButton>
          </div>
        </div>
      </template>
    </UModal>

    <!-- ── 配置指引 ──────────────────────────────────────────────── -->
    <UModal v-model:open="showGuide" :ui="{ content: 'max-w-lg' }">
      <template #content>
        <div class="p-6 space-y-4">
          <h3 class="text-lg font-semibold">把终端接到服务器</h3>
          <p class="text-sm">
            在「{{ guideTerminal?.name }}」那台树莓派上创建
            <code class="text-xs">/etc/cups-kiosk.conf</code>：
          </p>
          <pre class="bg-elevated rounded p-3 text-xs overflow-x-auto">{{ kioskConfig }}</pre>
          <div class="flex gap-2">
            <UButton variant="outline" icon="i-lucide-copy" @click="copyToken(guideTerminal?.token)">
              复制配置
            </UButton>
          </div>
          <p class="text-xs text-muted">
            token 是这台终端的凭据 —— 拿到它就能冒充这台终端。配置文件的权限设成
            600，不要贴进工单或聊天记录里。
          </p>
          <div class="flex justify-end">
            <UButton variant="ghost" @click="showGuide = false">关闭</UButton>
          </div>
        </div>
      </template>
    </UModal>

    <UModal v-model:open="showDeleteModal" :ui="{ content: 'max-w-md' }">
      <template #content>
        <div class="p-6 space-y-4">
          <h3 class="text-lg font-semibold">确认删除</h3>
          <p>确定要删除终端 <strong>{{ pendingDelete?.name }}</strong> 吗？</p>
          <p class="text-sm text-muted">
            删除后那台机器上的配置立刻失效，员工在那台打印机上刷卡将没有任何反应。
            如果只是暂时不用，改成「停用」更好。
          </p>
          <div class="flex justify-end gap-2">
            <UButton variant="ghost" @click="showDeleteModal = false">取消</UButton>
            <UButton color="error" :loading="deleting" @click="executeDelete">确认删除</UButton>
          </div>
        </div>
      </template>
    </UModal>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { apiFetch, readError, readJSON } from '../../utils/api'
import { formatTime, printerLabel } from '../../utils/format'
import { copyText } from '../../utils/clipboard'
import AdminTabs from '../../components/admin/AdminTabs.vue'

const toast = useToast()

const terminals = ref([])
const loadError = ref('')
const printers = ref([])
const loadingPrinters = ref(false)

const pullEnabled = ref(false)
const pullTtlHours = ref(72)
const savingToggle = ref(false)
const savingTtl = ref(false)

const showForm = ref(false)
const saving = ref(false)
const formError = ref('')
const form = ref({ token: '', name: '', printerUri: '', enabled: true })

const showGuide = ref(false)
const guideTerminal = ref(null)

const showDeleteModal = ref(false)
const pendingDelete = ref(null)
const deleting = ref(false)

const columns = [
  { accessorKey: 'name', header: '终端' },
  { accessorKey: 'lastSeenAt', header: '最近上线' },
  { accessorKey: 'token', header: 'Token' },
  { id: 'actions', header: '操作' }
]

// 用 printerLabel 而不是自己拼 —— 同型号多台队列时靠 CUPS 描述区分，
// 而那套「描述与队列名相同时不重复显示」的规则只该有一份（issue #101）。
const printerItems = computed(() =>
  printers.value.map((p) => ({ label: printerLabel(p), value: p.uri }))
)

// 终端配置文件的内容。与 scripts/kiosk/ 里的模板保持一致 ——
// 两边不一致的话，管理员照着这页配出来的终端会连不上，而报错只有
// 一串「终端未注册」，完全指不到根因。
const kioskConfig = computed(() => {
  const t = guideTerminal.value
  if (!t) return ''
  return [
    `server = ${window.location.origin}`,
    `token  = ${t.token}`,
    `name   = ${t.name}`
  ].join('\n')
})

function describe(e) {
  return `无法连接到服务端：${e.message || e}`
}

async function load() {
  loadError.value = ''
  try {
    const resp = await apiFetch('/api/admin/kiosk-terminals')
    if (!resp.ok) {
      loadError.value = await readError(resp)
      return
    }
    terminals.value = await readJSON(resp)
  } catch (e) {
    loadError.value = describe(e)
  }
}

async function loadPrinters() {
  loadingPrinters.value = true
  try {
    const resp = await apiFetch('/api/printers')
    if (resp.ok) printers.value = await readJSON(resp)
  } catch {
    // 打印机列表拿不到不阻断本页 —— 已配好的终端照样能看到、能改。
    // 只有「新增」时会因为没有可选项而卡住，那时再报。
  } finally {
    loadingPrinters.value = false
  }
}

async function loadSettings() {
  try {
    const resp = await apiFetch('/api/admin/settings')
    if (!resp.ok) return
    const data = await readJSON(resp)
    pullEnabled.value = !!data.pullModeEnabled
    pullTtlHours.value = data.pullTtlHours || 72
  } catch {
    // 读不到就保持默认（关闭）。开关是【开】才有风险的那一侧：
    // 误显示成开，管理员可能以为已经生效了。
  }
}

async function togglePull(value) {
  savingToggle.value = true
  try {
    const resp = await apiFetch('/api/admin/settings', {
      method: 'PUT',
      body: JSON.stringify({ pullModeEnabled: value })
    })
    if (!resp.ok) {
      // ⚠️ 失败必须把开关拨回去。否则界面显示「已开启」而服务端没接受，
      // 管理员会以为员工提交的任务已经开始暂存了。
      pullEnabled.value = !value
      toast.add({ title: '设置失败', description: await readError(resp), color: 'error', icon: 'i-lucide-x-circle' })
      return
    }
    toast.add({
      title: value ? '已启用刷卡取件' : '已关闭刷卡取件',
      description: value ? '员工提交的任务现在会先暂存' : '打印入口已恢复为直接打印',
      color: 'success',
      icon: 'i-lucide-check-circle'
    })
  } catch (e) {
    pullEnabled.value = !value
    toast.add({ title: '设置失败', description: describe(e), color: 'error', icon: 'i-lucide-x-circle' })
  } finally {
    savingToggle.value = false
  }
}

async function saveTtl() {
  savingTtl.value = true
  try {
    const resp = await apiFetch('/api/admin/settings', {
      method: 'PUT',
      body: JSON.stringify({ pullTtlHours: Number(pullTtlHours.value) || 0 })
    })
    if (!resp.ok) {
      toast.add({ title: '保存失败', description: await readError(resp), color: 'error', icon: 'i-lucide-x-circle' })
      return
    }
    toast.add({ title: '已保存', color: 'success', icon: 'i-lucide-check-circle' })
  } catch (e) {
    toast.add({ title: '保存失败', description: describe(e), color: 'error', icon: 'i-lucide-x-circle' })
  } finally {
    savingTtl.value = false
  }
}

function openCreate() {
  form.value = { token: '', name: '', printerUri: '', enabled: true, bindOnly: false }
  formError.value = ''
  showForm.value = true
  if (printers.value.length === 0) loadPrinters()
}

function openEdit(t) {
  form.value = {
    token: t.token,
    name: t.name,
    printerUri: t.printerUri,
    enabled: t.enabled,
    // printerUri 为空 = 仅用于绑卡的终端。勾选框要跟着它，否则一打开编辑
    // 就会显示成「选了打印机但没选」（而那个下拉是空的）。
    bindOnly: !t.printerUri
  }
  formError.value = ''
  showForm.value = true
  if (printers.value.length === 0) loadPrinters()
}

async function submitForm() {
  const f = form.value
  if (!f.bindOnly && !f.printerUri) {
    formError.value = '请选择这台终端对应的打印机，或勾选「仅用于绑卡」'
    return
  }
  saving.value = true
  formError.value = ''
  try {
    const isEdit = !!f.token
    // 勾了「仅用于绑卡」就把 printerUri 传空串 —— 服务端据此把它当成
    // 绑卡专用终端（刷卡时若没有等待中的绑定窗口会明确提示）。
    const printerUri = f.bindOnly ? '' : f.printerUri
    const resp = await apiFetch(
      isEdit ? `/api/admin/kiosk-terminals/${encodeURIComponent(f.token)}` : '/api/admin/kiosk-terminals',
      {
        method: isEdit ? 'PUT' : 'POST',
        body: JSON.stringify({ name: f.name, printerUri, enabled: f.enabled })
      }
    )
    if (!resp.ok) {
      formError.value = await readError(resp)
      return
    }
    const created = await readJSON(resp)
    showForm.value = false
    await load()
    if (!isEdit && created?.token) {
      // 新建后直接把配置指引弹出来 —— token 是要填进那台树莓派的，
      // 让管理员自己去列表里找一遍纯属多余。
      guideTerminal.value = created
      showGuide.value = true
    }
  } catch (e) {
    formError.value = describe(e)
  } finally {
    saving.value = false
  }
}

function showToken(t) {
  guideTerminal.value = t
  showGuide.value = true
}

function confirmDelete(t) {
  pendingDelete.value = t
  showDeleteModal.value = true
}

async function executeDelete() {
  const t = pendingDelete.value
  if (!t) return
  deleting.value = true
  try {
    const resp = await apiFetch(`/api/admin/kiosk-terminals/${encodeURIComponent(t.token)}`, { method: 'DELETE' })
    if (!resp.ok) {
      toast.add({ title: '删除失败', description: await readError(resp), color: 'error', icon: 'i-lucide-x-circle' })
      return
    }
    toast.add({ title: '已删除', description: `终端「${t.name}」已删除`, color: 'success', icon: 'i-lucide-check-circle' })
    await load()
  } catch (e) {
    toast.add({ title: '删除失败', description: describe(e), color: 'error', icon: 'i-lucide-x-circle' })
  } finally {
    deleting.value = false
    showDeleteModal.value = false
    pendingDelete.value = null
  }
}

async function copyToken(token) {
  if (!token) return
  const ok = await copyText(token)
  toast.add({
    title: ok ? '已复制' : '复制失败',
    description: ok ? 'token 已复制到剪贴板' : '请手动选中复制',
    color: ok ? 'success' : 'error',
    icon: ok ? 'i-lucide-check-circle' : 'i-lucide-x-circle'
  })
}

onMounted(async () => {
  await Promise.all([load(), loadSettings(), loadPrinters()])
})
</script>
