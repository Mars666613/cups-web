<template>
  <div class="p-3 sm:p-4 md:p-6 space-y-4 md:space-y-6">
    <div class="grid grid-cols-1 lg:grid-cols-2 gap-4 md:gap-6">
      <UCard>
        <template #header>
          <h2 class="text-xl font-bold flex items-center gap-2">
            <UIcon name="i-lucide-users" class="w-5 h-5" />
            用户管理
          </h2>
        </template>
        <UForm @submit="saveUser" :state="form" class="grid grid-cols-1 md:grid-cols-2 gap-3">
          <div>
            <UInput v-model="form.username" :disabled="form.protected" placeholder="登录名" :color="formErrors.username ? 'error' : undefined" />
            <p v-if="formErrors.username" class="text-xs text-error mt-1">{{ formErrors.username }}</p>
          </div>
          <div>
            <UInput type="password" v-model="form.password" :placeholder="isEditing ? '留空不修改密码' : '密码'" :color="formErrors.password ? 'error' : undefined" />
            <p v-if="formErrors.password" class="text-xs text-error mt-1">{{ formErrors.password }}</p>
          </div>
          <USelect
            v-model="form.role"
            :disabled="form.protected"
            :items="roleItems"
            value-key="value"
            label-key="label"
          />
          <UInput v-model="form.contactName" placeholder="联系人" />
          <UInput v-model="form.phone" placeholder="联系电话" />
          <div>
            <UInput v-model="form.email" placeholder="邮箱" :color="formErrors.email ? 'error' : undefined" />
            <p v-if="formErrors.email" class="text-xs text-error mt-1">{{ formErrors.email }}</p>
          </div>
          <div class="flex gap-2 md:col-span-2">
            <UButton type="submit" color="primary" :loading="savingUser" :disabled="savingUser">{{ isEditing ? '保存' : '新增用户' }}</UButton>
            <UButton type="button" variant="ghost" @click="resetForm">重置</UButton>
          </div>
        </UForm>

        <div class="overflow-x-auto mt-4">
          <UTable :columns="userColumns" :data="users">
            <template #balanceCents-cell="{ row }">
              <div class="flex items-center gap-2 whitespace-nowrap">
                <span :class="row.original.balanceCents > 0 ? 'text-success font-medium' : 'text-muted'">
                  {{ formatCents(row.original.balanceCents) }}
                </span>
                <UButton
                  size="xs"
                  variant="ghost"
                  icon="i-lucide-circle-dollar-sign"
                  @click="openBalanceModal(row.original)"
                >调整</UButton>
              </div>
            </template>
            <template #actions-cell="{ row }">
              <div class="flex gap-2">
                <UButton size="sm" variant="ghost" icon="i-lucide-pencil" @click="editUser(row.original)">编辑</UButton>
                <UButton size="sm" variant="outline" color="error" icon="i-lucide-trash-2" :disabled="row.original.username === 'admin'" @click="confirmDelete(row.original)">删除</UButton>
              </div>
            </template>
          </UTable>
        </div>
      </UCard>

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
          <UButton variant="outline" @click="loadPrintRecords" icon="i-lucide-search">查询</UButton>
        </div>
        <div class="overflow-x-auto">
          <UTable :columns="printColumns" :data="printRecords">
            <template #download-cell="{ row }">
              <UButton size="xs" variant="ghost" icon="i-lucide-download" @click="downloadFile(row.original.id)">下载</UButton>
            </template>
          </UTable>
        </div>
      </UCard>
    </div>

    <UCard>
      <template #header>
        <h2 class="text-xl font-bold flex items-center gap-2">
          <UIcon name="i-lucide-wallet" class="w-5 h-5" />
          计费设置
        </h2>
      </template>

      <div class="flex flex-wrap items-center gap-x-6 gap-y-3">
        <label class="flex items-center gap-2 cursor-pointer">
          <USwitch v-model="billing.enabled" />
          <span class="text-sm font-medium">开启计费</span>
        </label>
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
          :disabled="savingPrices"
          @click="savePrices"
        >保存计费设置</UButton>
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
        <UButton variant="outline" @click="loadAdminOrders" icon="i-lucide-search">查询</UButton>
      </div>
      <div class="overflow-x-auto">
        <UTable :columns="adminOrderColumns" :data="adminOrders">
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
              {{ orderStatusText[row.original.status] || row.original.status }}
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
      <div class="text-sm text-muted mt-2">
        退款会把订单金额<b>全额退回该用户余额</b>，并写一条 refund 流水。已全额退过款的订单按钮会禁用 —— 打印失败时系统已自动退过一次，这里是给需要人工定夺的单子（例如卡在「打印中」被转人工的）用的。
      </div>
    </UCard>

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
        <UButton variant="outline" @click="loadAdminTransactions" icon="i-lucide-search">查询</UButton>
      </div>
      <div class="overflow-x-auto">
        <UTable :columns="txColumns" :data="adminTransactions">
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
      <div class="text-sm text-muted mt-2">
        账本是审计记录，只增不改。有余额或有流水的用户因此不允许删除 —— 删用户会级联抹掉这些记录。
      </div>
    </UCard>

    <UCard>
      <template #header>
        <h2 class="text-xl font-bold flex items-center gap-2">
          <UIcon name="i-lucide-settings" class="w-5 h-5" />
          系统设置
        </h2>
      </template>
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
          <UButton color="primary" @click="saveSettings" icon="i-lucide-save" :loading="savingSettings" :disabled="savingSettings">保存设置</UButton>
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

    <UModal v-model:open="showDeleteModal">
      <template #content>
        <div class="p-6 space-y-4">
          <h3 class="text-lg font-semibold">确认删除</h3>
          <p>确定要删除用户 <strong>{{ pendingDeleteUser?.username }}</strong> 吗？</p>
          <p class="text-sm text-muted">此操作不可撤销。</p>
          <div class="flex justify-end gap-2">
            <UButton variant="ghost" @click="showDeleteModal = false">取消</UButton>
            <UButton color="error" :loading="!!deletingUserId" @click="executeDelete">确认删除</UButton>
          </div>
        </div>
      </template>
    </UModal>

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

    <UModal v-model:open="showBalanceModal">
      <template #content>
        <div class="p-6 space-y-4">
          <h3 class="text-lg font-semibold">调整余额</h3>
          <p v-if="balanceTarget" class="text-sm">
            用户 <strong>{{ balanceTarget.username }}</strong>，当前余额
            <strong>{{ formatCents(balanceTarget.balanceCents) }}</strong>
          </p>
          <div>
            <label class="block text-sm font-medium mb-1">调整金额（元）</label>
            <UInput
              type="number"
              step="0.01"
              v-model="balanceYuan"
              placeholder="正数为入账，负数为扣减，例如 10 或 -5"
              class="w-full"
            />
            <div class="text-xs text-muted mt-1">
              线下收款后在这里入账；扣减时余额不允许变成负数。
            </div>
          </div>
          <UInput v-model="balanceNote" placeholder="备注（可选，会记入流水）" class="w-full" />
          <p class="text-xs text-muted">
            每次调整都会写一条 admin_adjust 流水并记下操作人。余额与账本只能经这里变动 —— 直接改数据库会让两者对不上。
          </p>
          <div class="flex justify-end gap-2">
            <UButton variant="ghost" @click="showBalanceModal = false">取消</UButton>
            <UButton color="primary" :loading="adjustingBalance" @click="submitBalanceAdjust">确认调整</UButton>
          </div>
        </div>
      </template>
    </UModal>

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
import { ref, computed, onMounted } from 'vue'
import { getCSRF, readError, readJSON } from '../utils/api'
// 纸张尺寸 / 类型的选项与打印表单共用同一份定义（见 utils/printOptions.js）。
// 各写一份的话，管理端可能配出用户根本选不到的维度，那条价目永远不会被命中。
import { paperSizeItems, paperTypeItems } from '../utils/printOptions'
import { formatCents, formatTime } from '../utils/format'

const toast = useToast()
const emit = defineEmits(['logout'])

const users = ref([])
const form = ref({
  id: null,
  username: '',
  password: '',
  role: 'user',
  protected: false,
  contactName: '',
  phone: '',
  email: ''
})
const printFilters = ref({ username: '', start: '', end: '' })
const printRecords = ref([])
const settings = ref({ retentionDays: '', saveHistory: true, maxPagesPerJob: '0', maxUploadMB: '0', customCss: '', guestMode: false, orderTtlMinutes: '15' })
const showCleanupConfirm = ref(false)

const savingUser = ref(false)
const savingSettings = ref(false)
const cleaningUp = ref(false)
const deletingUserId = ref(null)
const pendingDeleteUser = ref(null)
const showDeleteModal = ref(false)
const formErrors = ref({})

const isEditing = computed(() => !!form.value.id)

const roleItems = [
  { label: '普通用户', value: 'user' },
  { label: '管理员', value: 'admin' }
]

const userColumns = [
  { accessorKey: 'id', header: 'ID' },
  { accessorKey: 'username', header: '登录名' },
  { accessorKey: 'role', header: '角色' },
  { accessorKey: 'balanceCents', header: '余额' },
  { accessorKey: 'contactName', header: '联系人' },
  { accessorKey: 'phone', header: '电话' },
  { accessorKey: 'email', header: '邮箱' },
  { id: 'actions', header: '操作' }
]

// ─── 余额调整（线下收款入账 / 纠正误扣）───────────────────────────────
const showBalanceModal = ref(false)
const balanceTarget = ref(null)
const balanceYuan = ref('')
const balanceNote = ref('')
const adjustingBalance = ref(false)

function openBalanceModal(user) {
  balanceTarget.value = user
  balanceYuan.value = ''
  balanceNote.value = ''
  showBalanceModal.value = true
}

async function submitBalanceAdjust() {
  const user = balanceTarget.value
  if (!user) return
  // 元 → 分。Math.round 避开浮点尾巴（0.1*100 = 10.000000000000002）。
  const amountCents = Math.round((Number(balanceYuan.value) || 0) * 100)
  if (amountCents === 0) {
    toast.add({ title: '请输入不为 0 的金额', color: 'warning', icon: 'i-lucide-alert-triangle' })
    return
  }

  adjustingBalance.value = true
  try {
    const resp = await fetch(`/api/admin/users/${user.id}/balance`, {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': getCSRF() },
      body: JSON.stringify({ amountCents, note: balanceNote.value })
    })
    if (!resp.ok) {
      const msg = await readError(resp)
      toast.add({ title: '调整失败', description: msg, color: 'error', icon: 'i-lucide-x-circle' })
      if (resp.status === 401) emit('logout')
      return
    }
    const data = await readJSON(resp)
    toast.add({
      title: '调整成功',
      description: `${user.username} 当前余额 ${formatCents(data.balanceCents)}`,
      color: 'success',
      icon: 'i-lucide-check-circle'
    })
    showBalanceModal.value = false
    await Promise.all([loadUsers(), loadAdminTransactions()])
  } finally {
    adjustingBalance.value = false
  }
}

const printColumns = [
  { accessorKey: 'createdAt', header: '时间' },
  { accessorKey: 'username', header: '用户' },
  { accessorKey: 'filename', header: '文件' },
  { accessorKey: 'pages', header: '页数' },
  { accessorKey: 'status', header: '状态' },
  { id: 'download', header: '下载' }
]

// ─── 计费设置 ─────────────────────────────────────────────────────────
// unitYuan 是 [UI 值]，存的是「元」的字符串；提交时才换算成「分」。
// 与 maxUploadMB 同一套路（后端存字节、前端用 MB 编辑）。
const billing = ref({ enabled: false, fallbackYuan: '0.00', rules: [] })
const savingPrices = ref(false)

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

// ─── 订单与流水（对账用）─────────────────────────────────────────────
// 筛选项里的「全部」必须用一个显式哨兵值。
//
// 🚫 绝不能用空字符串：Reka UI 的 SelectItem 明确禁止空串 value
// （那是留给「清空选择、显示 placeholder」的），传进去会在组件更新期
// 抛出 "A <SelectItem /> must have a value prop that is not an empty string"，
// 打崩整棵组件树 —— 表现是「点什么都没反应、一直停在当前页」。
// 实测踩过，控制台里那行错误才是唯一线索。
const FILTER_ALL = 'all'

const adminOrders = ref([])
const orderFilters = ref({ username: '', status: FILTER_ALL })
const refundingOrderId = ref(null)
const refundReason = ref('')
const showRefundModal = ref(false)
const pendingRefundOrder = ref(null)

const adminTransactions = ref([])
const txFilters = ref({ username: '', kind: FILTER_ALL })

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

const txKindItems = [
  { label: '全部类型', value: FILTER_ALL },
  { label: '充值', value: 'topup' },
  { label: '打印扣费', value: 'spend' },
  { label: '退款', value: 'refund' },
  { label: '管理员调整', value: 'admin_adjust' }
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

const txColumns = [
  { accessorKey: 'createdAt', header: '时间' },
  { accessorKey: 'username', header: '用户' },
  { accessorKey: 'kind', header: '类型' },
  { accessorKey: 'amountCents', header: '金额' },
  { accessorKey: 'balanceAfterCents', header: '余额' },
  { accessorKey: 'note', header: '备注' }
]

// 状态/类型 → 中文标签的查表。用同一个数组推导，
// 避免"加了一个状态却忘了加文案"。哨兵值不参与查表。
function toLabelMap(items) {
  return items.reduce((acc, i) => {
    if (i.value && i.value !== FILTER_ALL) acc[i.value] = i.label
    return acc
  }, {})
}
const orderStatusText = toLabelMap(orderStatusItems)
const txKindText = toLabelMap(txKindItems)

async function loadAdminOrders() {
  const params = new URLSearchParams()
  if (orderFilters.value.username) params.set('username', orderFilters.value.username)
  if (orderFilters.value.status !== FILTER_ALL) params.set('status', orderFilters.value.status)
  const resp = await fetch(`/api/admin/orders?${params}`, { credentials: 'include' })
  if (!resp.ok) {
    if (resp.status === 401) emit('logout')
    return
  }
  adminOrders.value = await readJSON(resp)
}

async function loadAdminTransactions() {
  const params = new URLSearchParams()
  if (txFilters.value.username) params.set('username', txFilters.value.username)
  if (txFilters.value.kind !== FILTER_ALL) params.set('kind', txFilters.value.kind)
  const resp = await fetch(`/api/admin/wallet-transactions?${params}`, { credentials: 'include' })
  if (!resp.ok) {
    if (resp.status === 401) emit('logout')
    return
  }
  adminTransactions.value = await readJSON(resp)
}

// 一笔订单能否退款：必须是付过钱的、且还没退满。
function canRefund(order) {
  return Boolean(order.payMethod) && order.refundedCents < order.amountCents
}

function orderStatusColor(s) {
  return {
    pending_payment: 'warning',
    paid: 'info',
    printing: 'info',
    printed: 'success',
    failed: 'error',
    refunded: 'neutral',
    expired: 'neutral',
    cancelled: 'neutral'
  }[s] || 'neutral'
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
    await Promise.all([loadAdminOrders(), loadAdminTransactions(), loadUsers()])
  } finally {
    refundingOrderId.value = null
  }
}

function validateForm() {
  formErrors.value = {}
  if (!form.value.username.trim()) {
    formErrors.value.username = '用户名不能为空'
  }
  if (!isEditing.value && !form.value.password) {
    formErrors.value.password = '新用户必须设置密码'
  }
  if (form.value.email && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(form.value.email)) {
    formErrors.value.email = '邮箱格式无效'
  }
  return Object.keys(formErrors.value).length === 0
}

function resetForm() {
  form.value = {
    id: null,
    username: '',
    password: '',
    role: 'user',
    protected: false,
    contactName: '',
    phone: '',
    email: ''
  }
  formErrors.value = {}
}

function editUser(user) {
  form.value = {
    id: user.id,
    username: user.username,
    password: '',
    role: user.role,
    protected: user.username === 'admin',
    contactName: user.contactName || '',
    phone: user.phone || '',
    email: user.email || ''
  }
  formErrors.value = {}
}

async function loadUsers() {
  const resp = await fetch('/api/admin/users', { credentials: 'include' })
  if (!resp.ok) {
    if (resp.status === 401) emit('logout')
    return
  }
  users.value = await readJSON(resp)
}

async function saveUser() {
  if (!validateForm()) return
  savingUser.value = true
  try {
    const payload = {
      username: form.value.username,
      password: form.value.password,
      role: form.value.role,
      contactName: form.value.contactName,
      phone: form.value.phone,
      email: form.value.email
    }
    const url = isEditing.value ? `/api/admin/users/${form.value.id}` : '/api/admin/users'
    const method = isEditing.value ? 'PUT' : 'POST'
    const resp = await fetch(url, {
      method,
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
    toast.add({ title: isEditing.value ? '更新成功' : '创建成功', description: `用户 ${form.value.username} 已保存`, color: 'success', icon: 'i-lucide-check-circle' })
    await loadUsers()
    resetForm()
  } finally {
    savingUser.value = false
  }
}

function confirmDelete(user) {
  pendingDeleteUser.value = user
  showDeleteModal.value = true
}

async function executeDelete() {
  const user = pendingDeleteUser.value
  if (!user) return
  deletingUserId.value = user.id
  try {
    const resp = await fetch(`/api/admin/users/${user.id}`, {
      method: 'DELETE',
      credentials: 'include',
      headers: { 'X-CSRF-Token': getCSRF() }
    })
    if (!resp.ok) {
      const msg = await readError(resp)
      toast.add({ title: '删除失败', description: msg, color: 'error', icon: 'i-lucide-x-circle' })
      if (resp.status === 401) emit('logout')
      return
    }
    toast.add({ title: '删除成功', description: `用户 ${user.username} 已删除`, color: 'success', icon: 'i-lucide-check-circle' })
    await loadUsers()
  } finally {
    deletingUserId.value = null
    showDeleteModal.value = false
    pendingDeleteUser.value = null
  }
}

function downloadFile(id) {
  window.open(`/api/print-records/${id}/file`, '_blank')
}

async function loadPrintRecords() {
  const params = new URLSearchParams()
  if (printFilters.value.username) params.set('username', printFilters.value.username)
  if (printFilters.value.start) params.set('start', printFilters.value.start)
  if (printFilters.value.end) params.set('end', printFilters.value.end)
  const resp = await fetch(`/api/admin/print-records?${params.toString()}`, { credentials: 'include' })
  if (!resp.ok) {
    if (resp.status === 401) emit('logout')
    return
  }
  printRecords.value = await readJSON(resp)
}

async function loadSettings() {
  const resp = await fetch('/api/admin/settings', { credentials: 'include' })
  if (!resp.ok) {
    if (resp.status === 401) emit('logout')
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
    await loadPrintRecords()
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

async function loadPrices() {
  const resp = await fetch('/api/admin/prices', { credentials: 'include' })
  if (!resp.ok) {
    if (resp.status === 401) emit('logout')
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
}

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
      enabled: billing.value.enabled === true,
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
    toast.add({ title: '保存成功', description: '计费设置已更新', color: 'success', icon: 'i-lucide-check-circle' })
    await loadPrices()
  } finally {
    savingPrices.value = false
  }
}

onMounted(async () => {
  await Promise.all([
    loadUsers(),
    loadPrintRecords(),
    loadSettings(),
    loadPrices(),
    loadAdminOrders(),
    loadAdminTransactions()
  ])
})
</script>
