<template>
  <div class="p-3 sm:p-4 md:p-6 space-y-4 md:space-y-6">
    <AdminTabs current="/admin/settings" />

    <UCard>
      <template #header>
        <h2 class="text-xl font-bold flex items-center gap-2">
          <UIcon name="i-lucide-settings" class="w-5 h-5" />
          系统设置
        </h2>
      </template>

      <div v-if="loadError" class="flex items-start gap-2 mb-4">
        <UAlert class="flex-1" color="error" variant="soft" icon="i-lucide-alert-triangle" title="系统设置加载失败" :description="loadError" />
        <UButton size="xs" variant="outline" icon="i-lucide-refresh-cw" @click="loadSettings">重试</UButton>
      </div>

      <div class="grid grid-cols-1 md:grid-cols-4 gap-3 items-end">
        <div>
          <label class="block text-sm font-medium mb-1">自动清理天数</label>
          <UInput type="number" step="1" v-model="settings.retentionDays" placeholder="例如 30" />
        </div>
        <div>
          <label class="block text-sm font-medium mb-1">单个任务最大页数</label>
          <UInput type="number" step="1" min="0" v-model="settings.maxPagesPerJob" placeholder="0 表示不限" />
        </div>
        <div>
          <label class="block text-sm font-medium mb-1">单个文件最大大小 (MB)</label>
          <UInput type="number" step="1" min="0" v-model="settings.maxUploadMB" placeholder="0 表示不限" />
        </div>
        <div>
          <label class="flex items-center gap-2 cursor-pointer h-9">
            <UCheckbox v-model="settings.saveHistory" />
            <span class="text-sm">保存打印历史</span>
          </label>
        </div>
        <div>
          <label class="block text-sm font-medium mb-1">订单支付有效期 (分钟)</label>
          <UInput type="number" step="1" min="1" max="1440" v-model="settings.orderTtlMinutes" placeholder="15" />
        </div>
        <div class="md:col-span-4">
          <label class="flex items-center gap-2 cursor-pointer">
            <UCheckbox v-model="settings.guestMode" />
            <span class="text-sm">开启访客模式（免登录）</span>
          </label>
          <p class="text-xs text-muted mt-1 ml-6">开启后未登录访问会自动以内置 guest 账号进入打印页；管理页仍需登录 admin。仅建议在可信内网使用。</p>
        </div>
        <div class="flex items-end gap-2 md:col-span-4">
          <UButton color="primary" @click="saveSettings" icon="i-lucide-save" :loading="savingSettings" :disabled="savingSettings || !!loadError">保存设置</UButton>
          <UButton variant="outline" @click="showCleanupConfirm = true" icon="i-lucide-trash-2" :loading="cleaningUp" :disabled="cleaningUp">立即清理</UButton>
        </div>
      </div>
      <div class="text-sm text-muted mt-2">自动清理会在设定天数后删除过期打印记录与文件。"立即清理"将删除所有打印记录和文件。关闭"保存打印历史"后，新的打印任务将不再产生记录。页数/大小上限用于阻止超规格文件占用打印机，0 表示不限制。</div>

      <div class="mt-4 pt-4 border-t border-default">
        <label class="block text-sm font-medium mb-1">自定义 CSS</label>
        <UTextarea
          v-model="settings.customCss"
          :rows="6"
          placeholder="/* 例如：调整主题色、字号、按钮渐变等；留空则不注入 */"
          class="w-full font-mono text-xs"
          :maxlength="32768"
        />
        <div class="text-xs text-muted mt-1">保存后立即生效，所有登录页与主界面都会应用（上限 32KB）。留空即恢复默认样式。</div>
      </div>
    </UCard>

    <UModal v-model:open="showCleanupConfirm">
      <template #content>
        <div class="p-6 space-y-4">
          <h3 class="text-lg font-semibold">确认清理</h3>
          <p>此操作将<strong>删除所有打印记录和相关文件</strong>，且不可撤销。</p>
          <p class="text-xs text-muted">
            未终结订单（待支付 / 已支付 / 打印中）持有的文件会被跳过，不会误删用户已付费的内容。
          </p>
          <div class="flex justify-end gap-2">
            <UButton variant="ghost" @click="showCleanupConfirm = false">取消</UButton>
            <UButton color="error" :loading="cleaningUp" @click="triggerCleanup">确认清理</UButton>
          </div>
        </div>
      </template>
    </UModal>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { getCSRF, readError, readJSON } from '../../utils/api'
import AdminTabs from '../../components/admin/AdminTabs.vue'

const toast = useToast()
const emit = defineEmits(['logout'])

const loadError = ref('')
const showCleanupConfirm = ref(false)
const savingSettings = ref(false)
const cleaningUp = ref(false)

const settings = ref({ retentionDays: '', saveHistory: true, maxPagesPerJob: '0', maxUploadMB: '0', customCss: '', guestMode: false, orderTtlMinutes: '15' })

async function loadSettings() {
  loadError.value = ''
  try {
    const resp = await fetch('/api/admin/settings', { credentials: 'include' })
    if (!resp.ok) {
      if (resp.status === 401) {
        emit('logout')
        return
      }
      // 🚫 失败时不能只是 return：表单会停留在初值，管理员照着"默认的"界面
      // 点保存，就把真实的系统设置覆盖掉了。所以既要报错，也要禁用保存按钮。
      loadError.value = await readError(resp)
      return
    }
    const data = await readJSON(resp)
    settings.value.retentionDays = String(data.retentionDays || 0)
    settings.value.saveHistory = data.saveHistory !== false
    settings.value.maxPagesPerJob = String(data.maxPagesPerJob || 0)
    // 后端以字节存储，前端 UI 用 MB 展示（1 MB = 1024*1024 B）。
    const bytes = Number(data.maxUploadBytes || 0)
    settings.value.maxUploadMB = String(bytes > 0 ? Math.round(bytes / (1024 * 1024)) : 0)
    settings.value.customCss = typeof data.customCss === 'string' ? data.customCss : ''
    settings.value.guestMode = data.guestMode === true
    settings.value.orderTtlMinutes = String(data.orderTtlMinutes || 15)
  } catch (e) {
    loadError.value = `无法连接到服务端：${e.message || e}`
  }
}

async function triggerCleanup() {
  showCleanupConfirm.value = false
  cleaningUp.value = true
  try {
    const resp = await fetch('/api/admin/cleanup', {
      method: 'POST',
      credentials: 'include',
      headers: { 'X-CSRF-Token': getCSRF() }
    })
    if (!resp.ok) {
      const msg = await readError(resp)
      toast.add({ title: '清理失败', description: msg, color: 'error', icon: 'i-lucide-x-circle' })
      if (resp.status === 401) emit('logout')
      return
    }
    const data = await readJSON(resp)
    const count = data.deleted || 0
    toast.add({
      title: '清理完成',
      description: count > 0 ? `已删除 ${count} 条打印记录及相关文件` : '没有需要清理的记录',
      color: 'success',
      icon: 'i-lucide-check-circle'
    })
    // 原先这里还会重载打印记录列表 —— 那已经是「打印记录」子页了，
    // 不挂载在这里；它每次进页都会自己重新拉取。
  } finally {
    cleaningUp.value = false
  }
}

async function saveSettings() {
  savingSettings.value = true
  try {
    const maxPages = Math.max(0, parseInt(settings.value.maxPagesPerJob || '0', 10) || 0)
    const maxMB = Math.max(0, parseInt(settings.value.maxUploadMB || '0', 10) || 0)
    const payload = {
      retentionDays: parseInt(settings.value.retentionDays || '0', 10),
      saveHistory: settings.value.saveHistory,
      maxPagesPerJob: maxPages,
      maxUploadBytes: maxMB * 1024 * 1024,
      customCss: settings.value.customCss || '',
      guestMode: settings.value.guestMode === true,
      orderTtlMinutes: Math.max(1, parseInt(settings.value.orderTtlMinutes || '15', 10) || 15)
    }
    const resp = await fetch('/api/admin/settings', {
      method: 'PUT',
      credentials: 'include',
      headers: {
        'Content-Type': 'application/json',
        'X-CSRF-Token': getCSRF()
      },
      body: JSON.stringify(payload)
    })
    if (!resp.ok) {
      const msg = await readError(resp)
      toast.add({ title: '保存失败', description: msg, color: 'error', icon: 'i-lucide-x-circle' })
      if (resp.status === 401) emit('logout')
      return
    }
    toast.add({ title: '保存成功', description: '系统设置已更新', color: 'success', icon: 'i-lucide-check-circle' })
    await loadSettings()
    // 立即把新的自定义 CSS 应用到当前页面，避免管理员必须手动 reload（Issue #57）。
    const el = document.getElementById('app-custom-css')
    if (el || settings.value.customCss) {
      const style = el || Object.assign(document.createElement('style'), { id: 'app-custom-css' })
      if (!el) document.head.appendChild(style)
      style.textContent = settings.value.customCss || ''
    }
  } finally {
    savingSettings.value = false
  }
}

onMounted(loadSettings)
</script>
