<template>
  <div class="p-3 sm:p-4 md:p-6 space-y-4 md:space-y-6">
    <AdminTabs current="/admin/billing" />

    <UCard>
      <template #header>
        <h2 class="text-xl font-bold flex items-center gap-2">
          <UIcon name="i-lucide-wallet" class="w-5 h-5" />
          计费设置
        </h2>
      </template>

      <div v-if="loadError" class="flex items-start gap-2 mb-4">
        <UAlert class="flex-1" color="error" variant="soft" icon="i-lucide-alert-triangle" title="价目加载失败" :description="loadError" />
        <UButton size="xs" variant="outline" icon="i-lucide-refresh-cw" @click="loadPrices">重试</UButton>
      </div>

      <!-- ── 总开关 ──────────────────────────────────────────────────
           独立于下面的价目表，改动即生效（与「启用刷卡取件」同一套）。
           开关与价目是两件事：管理员想临时停收/恢复收费时，不该被要求
           连价目表一起提交一次 —— 那个按钮一旦失效，开关也一起卡住。 -->
      <div class="flex items-start gap-3">
        <USwitch
          v-model="billing.enabled"
          :loading="savingToggle"
          :disabled="!!loadError || savingToggle"
          @update:model-value="toggleBilling"
        />
        <div class="flex-1">
          <p class="font-medium">开启计费</p>
          <p class="text-sm text-muted">
            开启后用户提交打印要先付费才出纸；关闭则直接送印。与「刷卡取件」「访客模式」互斥。
          </p>
        </div>
      </div>

      <!-- 拦住「开了计费但什么都算 0 元」：那等于收款功能被静默关掉，
           而管理员以为自己已经在收费了。后端也会拒这种保存，这里是提前提示。 -->
      <UAlert
        v-if="priceMisconfigured"
        class="mt-4"
        color="warning"
        variant="soft"
        icon="i-lucide-alert-triangle"
        title="价目未配置"
        description="计费已开启，但既没有价目也没有兜底单价，所有打印都会算成 0 元。请至少添加一条价目，或填一个大于 0 的兜底单价。"
      />

      <div class="flex flex-wrap items-center gap-x-6 gap-y-3 mt-5 pt-5 border-t border-default">
        <div class="flex items-center gap-2">
          <label class="text-sm">兜底单价</label>
          <UInput type="number" step="0.01" min="0" v-model="billing.fallbackYuan" class="w-28" placeholder="0.00" />
          <span class="text-sm text-muted">元/页（价目未命中时使用）</span>
        </div>
        <UButton
          class="ml-auto"
          color="primary"
          icon="i-lucide-save"
          :loading="savingPrices"
          :disabled="savingPrices || !!loadError"
          @click="savePrices"
        >保存价目</UButton>
      </div>

      <div class="overflow-x-auto mt-4">
        <UTable :columns="priceColumns" :data="billing.rules">
          <template #isColor-cell="{ row }">
            <USelect v-model="row.original.isColor" :items="priceColorItems" value-key="value" label-key="label" size="sm" class="w-24" />
          </template>
          <template #paperSize-cell="{ row }">
            <USelect v-model="row.original.paperSize" :items="paperSizeItems" value-key="value" label-key="label" size="sm" class="w-40" />
          </template>
          <template #paperType-cell="{ row }">
            <USelect v-model="row.original.paperType" :items="paperTypeItems" value-key="value" label-key="label" size="sm" class="w-36" />
          </template>
          <template #unitYuan-cell="{ row }">
            <div class="flex items-center gap-1">
              <UInput type="number" step="0.01" min="0" v-model="row.original.unitYuan" size="sm" class="w-24" />
              <span class="text-xs text-muted">元/页</span>
            </div>
          </template>
          <template #actions-cell="{ row }">
            <UButton size="xs" variant="ghost" color="error" icon="i-lucide-trash-2" @click="removePriceRule(row.original)">删除</UButton>
          </template>
        </UTable>
      </div>

      <div class="mt-3">
        <UButton variant="outline" size="sm" icon="i-lucide-plus" @click="addPriceRule">新增价目</UButton>
      </div>

      <div class="text-sm text-muted mt-3">
        按「实际印数 × 单价」计费：页面范围与奇数/偶数页只收实际打印的页数，份数另乘。纸张尺寸与类型请从下拉中选择 —— 用户端只能选这些值，配了别的维度不会被命中。开启计费前需要先关闭访客模式（访客是所有未登录用户共用的账号，余额无法追责）。
      </div>
    </UCard>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { getCSRF, readError, readJSON } from '../../utils/api'
// 纸张尺寸 / 类型的选项与打印表单共用同一份定义（见 utils/printOptions.js）。
// 各写一份的话，管理端可能配出用户根本选不到的维度，那条价目永远不会被命中。
import { paperSizeItems, paperTypeItems } from '../../utils/printOptions'
import AdminTabs from '../../components/admin/AdminTabs.vue'

const toast = useToast()
const emit = defineEmits(['logout'])

const loadError = ref('')
const savingPrices = ref(false)
const savingToggle = ref(false)

// unitYuan 是 [UI 值]，存的是「元」的字符串；提交时才换算成「分」。
const billing = ref({ enabled: false, fallbackYuan: '0.00', rules: [] })

const priceColorItems = [
  { label: '黑白', value: false },
  { label: '彩色', value: true }
]

const priceColumns = [
  { accessorKey: 'isColor', header: '颜色' },
  { accessorKey: 'paperSize', header: '纸张尺寸' },
  { accessorKey: 'paperType', header: '纸张类型' },
  { accessorKey: 'unitYuan', header: '单价' },
  { id: 'actions', header: '' }
]

const priceMisconfigured = computed(() =>
  billing.value.enabled &&
  billing.value.rules.length === 0 &&
  !(Number(billing.value.fallbackYuan) > 0)
)

function addPriceRule() {
  billing.value.rules.push({ isColor: false, paperSize: 'A4', paperType: 'plain', unitYuan: '0.20' })
}

// 按【对象引用】删除而不是按下标：UTable 的插槽给的是行数据，
// 靠下标反而要依赖表格内部的行序，容易在排序变化时删错行。
function removePriceRule(rule) {
  const i = billing.value.rules.indexOf(rule)
  if (i >= 0) billing.value.rules.splice(i, 1)
}

async function loadPrices() {
  loadError.value = ''
  try {
    const resp = await fetch('/api/admin/prices', { credentials: 'include' })
    if (!resp.ok) {
      if (resp.status === 401) {
        emit('logout')
        return
      }
      // 🚫 失败时不能只是 return：表单会停留在初值（计费关闭、价目为空），
      // 管理员照着"空的"界面点保存，就把真实的价目表覆盖掉了。
      // 所以这里既要报错，也要在出错时禁用保存按钮。
      loadError.value = await readError(resp)
      return
    }
    const data = await readJSON(resp)
    billing.value.enabled = data.enabled === true
    // 后端以「分」存储，前端用「元」编辑。
    billing.value.fallbackYuan = ((Number(data.fallbackUnitCents) || 0) / 100).toFixed(2)
    billing.value.rules = (data.rules || []).map((r) => ({
      isColor: r.isColor === true,
      paperSize: r.paperSize,
      paperType: r.paperType,
      unitYuan: ((Number(r.unitCents) || 0) / 100).toFixed(2)
    }))
  } catch (e) {
    loadError.value = `无法连接到服务端：${e.message || e}`
  }
}

// 计费开关【独立提交】：PUT /api/admin/prices 的三个字段是「省略即不改」语义
// （见 pricesPayload 的注释），所以只带 enabled 就能单独切换，不会碰价目表
// 与兜底单价。管理员的意图是「先停掉收费」时，不该被要求连价目一起提交一次。
//
// 后端拿「这次修改【之后】的状态」做跨字段校验，所以这里能收到真正有用的
// 拒绝理由（还没配价目 / 访客模式开着 / 刷卡取件开着），原样转给管理员即可。
async function toggleBilling(value) {
  savingToggle.value = true
  try {
    const resp = await fetch('/api/admin/prices', {
      method: 'PUT',
      credentials: 'include',
      headers: {
        'Content-Type': 'application/json',
        'X-CSRF-Token': getCSRF()
      },
      body: JSON.stringify({ enabled: value })
    })
    if (!resp.ok) {
      // ⚠️ 失败必须把开关拨回去。界面显示「已开启」而服务端没接受，
      // 管理员会以为用户已经开始付费了 —— 这是最不能有的错觉。
      billing.value.enabled = !value
      toast.add({ title: '设置失败', description: await readError(resp), color: 'error', icon: 'i-lucide-x-circle' })
      if (resp.status === 401) emit('logout')
      return
    }
    toast.add({
      title: value ? '已开启计费' : '已关闭计费',
      description: value ? '用户提交打印后需先付费才会出纸' : '打印入口已恢复为直接送印',
      color: 'success',
      icon: 'i-lucide-check-circle'
    })
  } catch (e) {
    billing.value.enabled = !value
    toast.add({ title: '设置失败', description: `无法连接到服务端：${e.message || e}`, color: 'error', icon: 'i-lucide-x-circle' })
  } finally {
    savingToggle.value = false
  }
}

// 保存价目表与兜底单价。【不传 enabled】—— 开关由 toggleBilling 单独负责，
// 这里带上它会让「保存价目」顺带改写开关状态，两边打架时无从解释。
async function savePrices() {
  savingPrices.value = true
  try {
    const rules = billing.value.rules.map((r) => ({
      isColor: r.isColor === true,
      paperSize: r.paperSize,
      paperType: r.paperType,
      // 元 → 分。必须先 Math.round：0.1 * 100 在 IEEE754 下是 10.000000000000002，
      // 直接取整会变成 10 或 11 取决于写法，钱的换算不能有这种模糊地带。
      unitCents: Math.round((Number(r.unitYuan) || 0) * 100)
    }))
    const payload = {
      fallbackUnitCents: Math.round((Number(billing.value.fallbackYuan) || 0) * 100),
      rules
    }
    const resp = await fetch('/api/admin/prices', {
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
    toast.add({ title: '已保存', description: '价目表已更新', color: 'success', icon: 'i-lucide-check-circle' })
    await loadPrices()
  } finally {
    savingPrices.value = false
  }
}

onMounted(loadPrices)
</script>
