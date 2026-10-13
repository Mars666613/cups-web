<template>
  <div class="p-3 sm:p-4 md:p-6 space-y-4 md:space-y-6">
    <AdminTabs current="/admin/users" />

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

      <div v-if="loadError" class="flex items-start gap-2 mt-4">
        <UAlert class="flex-1" color="error" variant="soft" icon="i-lucide-alert-triangle" title="用户列表加载失败" :description="loadError" />
        <UButton size="xs" variant="outline" icon="i-lucide-refresh-cw" @click="loadUsers">重试</UButton>
      </div>

      <!-- 按用户名查人。顾客报过来的就是完整的 `alipay:8F3K2Q`，粘进去；后端做的是
           子串匹配，所以只打一半也能查到。placeholder 里写清楚，免得管理员以为
           必须一字不差。 -->
      <div class="flex flex-wrap gap-3 items-end mt-4 mb-4">
        <UInput v-model="userFilters.username" placeholder="用户名（支持部分匹配）" />
        <UButton variant="outline" @click="searchUsers" icon="i-lucide-search">查询</UButton>
      </div>

      <div class="overflow-x-auto">
        <UTable :columns="userColumns" :data="usersPaged">
          <!-- 支付宝账号的用户名是 alipay:<128 字符 open_id>，直接铺出来认不出谁是谁。
               渲染成「支付宝顾客 074a1C…」，完整值留在 title 上可复制。 -->
          <template #username-cell="{ row }">
            <div class="flex items-center gap-2 min-w-0">
              <span class="truncate" :title="row.original.username">{{ userLabel(row.original.username) }}</span>
              <UBadge v-if="row.original.isAlipay" color="primary" variant="subtle" size="sm">支付宝</UBadge>
            </div>
          </template>
          <!-- 工牌号是读卡器吐出来的任意字符串，可能很长 —— 截断显示，
               完整值留在 title 上。绑定/解绑都在这一格。 -->
          <template #cardNo-cell="{ row }">
            <div class="flex items-center gap-2 min-w-0">
              <span v-if="row.original.cardNo" class="truncate font-mono" :title="row.original.cardNo">
                {{ userLabel(row.original.cardNo) }}
              </span>
              <span v-else class="text-muted text-xs">未绑定</span>
              <UButton
                size="xs"
                variant="ghost"
                icon="i-lucide-scan-line"
                @click="openCardModal(row.original)"
              >{{ row.original.cardNo ? '重绑' : '绑卡' }}</UButton>
            </div>
          </template>
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
      <!-- 带筛选却一条都没有时要说一句。否则表格空着、下面"共 N 个"那行也消失了，
           管理员分不清是"没这个人"还是"查询没生效"。 -->
      <div v-if="userFilters.username && usersTotal === 0 && !loadError" class="text-sm text-muted text-center py-4">
        没有匹配「{{ userFilters.username }}」的用户
      </div>

      <div v-if="usersTotal > 0" class="flex items-center justify-between gap-2 mt-3">
        <span class="text-xs text-muted">共 {{ usersTotal }} 个用户</span>
        <UPagination
          v-if="usersPages > 1"
          v-model:page="usersPage"
          :total="usersTotal"
          :items-per-page="usersPager.pageSize"
          size="sm"
        />
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

    <!-- 绑卡。
         ⚠️ 这里的卡号是读卡器的 **UID**（十六进制），不是卡面上印的号 ——
         两者通常不一样，这是正常的，不是读错了。
         读卡器（ACR122U 这类 PC/SC 设备）挂在打印机旁那台终端上，浏览器
         访问不到它，所以主路径是：在终端上跑 `kiosk.py --read`，把打出来的
         UID 抄/粘到下面这个框里。
         如果这台电脑上另插了键盘模拟的读卡器，直接刷也能进这个框
         （见 utils/cardReader.js 的兜底监听）。 -->
    <UModal v-model:open="showCardModal" :ui="{ content: 'max-w-md' }">
      <template #content>
        <div class="p-6 space-y-4">
          <h3 class="text-lg font-semibold">绑定工牌</h3>
          <p v-if="cardTarget" class="text-sm">
            用户 <strong>{{ cardTarget.username }}</strong>
            <span v-if="cardTarget.cardNo" class="text-muted">（已有工牌，这次是换卡）</span>
          </p>

          <!-- ── 方式一：点一下，去终端刷卡（推荐）──────────────────
               读卡器在打印机旁边那台终端上，浏览器够不着它 ——
               所以这里开一个服务端的「绑定窗口」，你在窗口期内刷的那张卡
               会被服务端直接绑给上面这个用户。 -->
          <div
            v-if="cardBindMode !== 'bound'"
            class="rounded-lg border-2 border-dashed p-5 text-center"
            :class="cardBindMode === 'waiting' ? 'border-primary bg-primary/5' : 'border-default'"
          >
            <template v-if="cardBindMode === 'waiting'">
              <UIcon name="i-lucide-scan-line" class="w-8 h-8 mx-auto mb-2 text-primary animate-pulse" />
              <p class="font-medium">正在等待刷卡…</p>
              <p class="text-sm text-muted mt-1">
                请到打印机的取件终端上刷这张工牌
              </p>
              <p class="text-xs text-muted mt-2">还剩 {{ bindRemain }} 秒</p>
              <UButton class="mt-3" variant="ghost" size="sm" @click="cancelCardBind">取消等待</UButton>
            </template>
            <template v-else>
              <UIcon name="i-lucide-hand-pointer" class="w-8 h-8 mx-auto mb-2 text-primary" />
              <UButton color="primary" size="lg" @click="startCardBind">开始等待刷卡</UButton>
              <p class="text-sm text-muted mt-3">
                点完之后到打印机旁的终端上刷工牌，卡号会自动填进来。
              </p>
              <p class="text-xs text-muted mt-1">窗口 2 分钟内有效，期间只有一个绑定窗口。</p>
            </template>

            <p v-if="bindError" class="text-sm text-warning mt-3">{{ bindError }}</p>
            <p v-if="cardBindMode === 'closed'" class="text-sm text-muted mt-3">
              窗口已经结束（超时或已取消）。可以再点一次。
            </p>
          </div>

          <!-- 绑定成功 -->
          <div v-else class="rounded-lg border-2 border-success bg-success/5 p-5 text-center">
            <UIcon name="i-lucide-check-circle" class="w-8 h-8 mx-auto mb-2 text-success" />
            <p class="font-medium">绑定成功</p>
            <p class="font-mono text-sm mt-1 break-all">{{ cardText }}</p>
            <p class="text-xs text-muted mt-2">
              如果这不该是刚才那张卡，点「解绑」重来。
            </p>
          </div>

          <!-- ── 方式二：手抄 UID ──────────────────────────────────
               批量发卡时更快 —— 不用为了绑一个人来回走一趟。 -->
          <div v-if="cardBindMode !== 'bound'" class="space-y-2">
            <div class="flex items-center gap-2 text-xs text-muted">
              <span class="h-px flex-1 bg-default" />
              或者手动填写
              <span class="h-px flex-1 bg-default" />
            </div>
            <UInput
              v-model="cardText"
              placeholder="粘贴 UID，或在本机读卡器上刷卡"
              class="w-full"
              autocomplete="off"
              spellcheck="false"
              @keydown.enter="submitCard"
            />
            <p class="text-xs text-muted">
              在终端上执行 <code class="font-mono">kiosk.py --read</code> 刷卡，
              把打出来的 UID 抄过来。
              大小写、以及中间可能带的冒号或连字符，服务器会自动统一 ——
              照原样贴进来就行。
            </p>
          </div>

          <p v-if="cardError" class="text-sm text-error">{{ cardError }}</p>

          <div class="flex justify-end gap-2">
            <UButton variant="ghost" @click="closeCardModal">
              {{ cardBindMode === 'bound' ? '关闭' : '取消' }}
            </UButton>
            <UButton
              v-if="cardTarget?.cardNo"
              variant="outline"
              color="error"
              :loading="cardBusy"
              @click="unbindCard"
            >解绑</UButton>
            <UButton
              v-if="cardBindMode !== 'bound'"
              color="primary"
              :disabled="!cardText.trim() || cardBindMode === 'waiting'"
              :loading="cardBusy"
              @click="submitCard"
            >
              确认绑定
            </UButton>
          </div>
        </div>
      </template>
    </UModal>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { apiFetch, getCSRF, readError, readJSON } from '../../utils/api'
import { formatCents, userLabel } from '../../utils/format'
import { useCardReader } from '../../utils/cardReader'
import { usePagedRows } from '../../utils/tablePage'
import AdminTabs from '../../components/admin/AdminTabs.vue'

const toast = useToast()
const emit = defineEmits(['logout'])

const users = ref([])
const loadError = ref('')
// 用户列表的筛选条件。只有用户名一项 —— 这个页面最常见的用法就是
// "顾客报了个用户名，把他找出来"。
const userFilters = ref({ username: '' })

// 解构成顶层 ref 而不是用 `pager.rows`：模板只自动解包顶层绑定，
// 挂在对象上就得写 `.rows.value`，迟早漏一个。
const usersPager = usePagedRows(users)
const { page: usersPage, pageCount: usersPages, total: usersTotal, rows: usersPaged } = usersPager

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
const savingUser = ref(false)
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
  { accessorKey: 'cardNo', header: '工牌' },
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
    await loadUsers()
  } finally {
    adjustingBalance.value = false
  }
}

// ─── 工牌绑定（刷卡取件）─────────────────────────────────────────────
//
// 卡号的来源与归一规则见 utils/cardReader.js 顶部的注释。
// 这里只负责开关捕获、把值提交给后端。
const showCardModal = ref(false)
const cardTarget = ref(null)
const cardError = ref('')
const cardBusy = ref(false)
const {
  text: cardText,
  start: startCardReading,
  stop: stopCardReading,
  reset: resetCardReading
} = useCardReader({ onSubmit: submitCard })

// ── 「点一下 → 去终端刷卡」的绑定窗口 ──
//
// 卡片与刷卡的物理位置天生是两个地方（按钮在浏览器里，读卡器在打印机旁），
// 所以中间必须由服务端开一个短命窗口把两边连起来。
// 这里负责开窗口、轮询结果、以及**在关框时把窗口关掉** ——
// 忘了最后那件事的话，那 2 分钟内谁都开不了新的绑定窗口。
const cardBindMode = ref('idle') // idle / waiting / bound / closed
const bindSessionId = ref(null)
const bindExpiresAt = ref('')
const bindRemain = ref(0)
const bindError = ref('')
let bindPoll = null
let bindClock = null

function stopBindTimers() {
  if (bindPoll) { clearInterval(bindPoll); bindPoll = null }
  if (bindClock) { clearInterval(bindClock); bindClock = null }
}

async function startCardBind() {
  const user = cardTarget.value
  if (!user) return
  cardError.value = ''
  bindError.value = ''
  try {
    const resp = await apiFetch(`/api/admin/users/${user.id}/card-bind`, { method: 'POST' })
    if (!resp.ok) {
      cardError.value = await readError(resp)
      return
    }
    const data = await readJSON(resp)
    bindSessionId.value = data.id
    bindExpiresAt.value = data.expiresAt
    cardBindMode.value = 'waiting'
    bindRemain.value = Math.max(0, Math.round((new Date(data.expiresAt).getTime() - Date.now()) / 1000))
    stopBindTimers()
    bindPoll = setInterval(pollCardBind, 1500)
    bindClock = setInterval(() => {
      bindRemain.value = Math.max(0, bindRemain.value - 1)
      if (bindRemain.value <= 0) expireCardBind()
    }, 1000)
  } catch (e) {
    cardError.value = `无法连接到服务端：${e.message || e}`
  }
}

async function pollCardBind() {
  if (!bindSessionId.value) return
  try {
    const resp = await apiFetch(`/api/admin/card-bind-sessions/${bindSessionId.value}`)
    if (!resp.ok) return // 网络抖动，下一轮再说
    const data = await readJSON(resp)

    if (data.status === 'bound') {
      stopBindTimers()
      cardBindMode.value = 'bound'
      cardText.value = data.cardNo || ''
      toast.add({
        title: '工牌绑定成功',
        description: `${data.username} 的工牌已绑定（${data.cardNo}）`,
        color: 'success',
        icon: 'i-lucide-check-circle'
      })
      await loadUsers()
      return
    }
    if (data.status === 'closed') {
      stopBindTimers()
      cardBindMode.value = 'closed'
      return
    }
    // waiting：把服务端记下的失败原因带上来（典型是「这张卡已绑给别人」）。
    // 窗口不会因此关闭，换一张卡接着刷就行。
    bindError.value = data.error || ''
  } catch {
    // 忽略，下一轮再试
  }
}

function expireCardBind() {
  stopBindTimers()
  cardBindMode.value = 'closed'
}

async function cancelCardBind() {
  const id = bindSessionId.value
  stopBindTimers()
  cardBindMode.value = 'idle'
  bindSessionId.value = null
  bindError.value = ''
  if (!id) return
  try {
    await apiFetch(`/api/admin/card-bind-sessions/${id}`, { method: 'DELETE' })
  } catch {
    // 取消失败没关系：窗口 2 分钟后自己过期。
  }
}

function openCardModal(user) {
  cardTarget.value = user
  cardError.value = ''
  resetCardReading()
  cardBindMode.value = 'idle'
  bindSessionId.value = null
  bindError.value = ''
  showCardModal.value = true
  // 打开即开启兜底监听：焦点万一不在输入框里（管理员点了别处），
  // 读卡器敲出来的字符也能被捡回来填进框里。
  startCardReading()
}

function closeCardModal() {
  // ⚠️ 关框时必须把绑定窗口也关掉 —— 它是【全局单飞】的，
  // 留着会让接下来 2 分钟内谁都开不了新窗口，而现象是
  //「点开始等待刷卡，报已经有窗口开着」，却找不到在哪儿关。
  cancelCardBind()
  // ⚠️ 必须先摘监听再关框。反过来的话，模态框消失后监听还在，
  // 页面上其它输入会被静默吞掉 —— 表现是「搜索框打不进字」。
  stopCardReading()
  showCardModal.value = false
  cardTarget.value = null
  resetCardReading()
}

async function submitCard() {
  const user = cardTarget.value
  const cardNo = cardText.value.trim()
  if (!user || !cardNo) return

  cardBusy.value = true
  cardError.value = ''
  try {
    const resp = await apiFetch(`/api/admin/users/${user.id}/card`, {
      method: 'POST',
      body: JSON.stringify({ cardNo })
    })
    if (!resp.ok) {
      // 409 的文案是后端拼好的「这张卡已经绑定给「XXX」了」——直接显示。
      // 换一句笼统的「绑定失败」，管理员会以为是系统故障而反复重试，
      // 而真实情况通常是卡发重了、或换卡后老卡没解绑。
      cardError.value = await readError(resp)
      return
    }
    toast.add({
      title: '绑定成功',
      description: `${user.username} 的工牌已绑定`,
      color: 'success',
      icon: 'i-lucide-check-circle'
    })
    closeCardModal()
    await loadUsers()
  } catch (e) {
    cardError.value = `无法连接到服务端：${e.message || e}`
  } finally {
    cardBusy.value = false
  }
}

async function unbindCard() {
  const user = cardTarget.value
  if (!user) return
  cardBusy.value = true
  cardError.value = ''
  try {
    const resp = await apiFetch(`/api/admin/users/${user.id}/card`, { method: 'DELETE' })
    if (!resp.ok) {
      cardError.value = await readError(resp)
      return
    }
    toast.add({
      title: '已解绑',
      description: `${user.username} 的工牌已解绑，这张卡现在可以绑给别人`,
      color: 'success',
      icon: 'i-lucide-check-circle'
    })
    closeCardModal()
    await loadUsers()
  } catch (e) {
    cardError.value = `无法连接到服务端：${e.message || e}`
  } finally {
    cardBusy.value = false
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
  loadError.value = ''
  try {
    const params = new URLSearchParams()
    const name = userFilters.value.username.trim()
    if (name) params.set('username', name)
    const resp = await fetch(`/api/admin/users?${params}`, { credentials: 'include' })
    if (!resp.ok) {
      if (resp.status === 401) {
        emit('logout')
        return
      }
      // 🚫 失败时不能只是 return —— 表格会渲染成「暂无数据」，
      // 管理员会以为用户真的没了。失败必须有可见的错误态。
      loadError.value = await readError(resp)
      return
    }
    users.value = await readJSON(resp)
  } catch (e) {
    loadError.value = `无法连接到服务端：${e.message || e}`
  }
}

// 「查询」= 翻回第一页 + 重查。
//
// 🚫 归零【只】放在这里，不要塞进 loadUsers：保存用户、删除用户、调额之后
// 也会重跑 loadUsers，那时管理员在第 3 页操作完被弹回第 1 页很难受。
// 🚫 也保持【无参】：模板里 @click="searchUsers" 会把 MouseEvent 传进来，
// 带可选参数的签名会被事件对象顶成真值（AGENTS.md 记过两次的坑）。
function searchUsers() {
  usersPager.resetPage()
  return loadUsers()
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

onMounted(loadUsers)

// 离开这一页时若还开着绑定窗口，把轮询停掉。
// 窗口本身留着自己过期（2 分钟）—— 不在这里发取消请求，
// 因为组件卸载时发出去的请求经常被浏览器直接掐掉，反而留下一个
// 半死不活的窗口：既没人轮询它，又占着全局单飞的名额。
onUnmounted(stopBindTimers)
</script>
