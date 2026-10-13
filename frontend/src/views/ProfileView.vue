<template>
  <div class="p-3 sm:p-4 md:p-6 max-w-3xl mx-auto space-y-4">
    <h1 class="text-lg font-bold flex items-center gap-2">
      <UIcon name="i-lucide-user-round" class="w-5 h-5 text-primary" />
      个人中心
    </h1>

    <!-- 身份卡：这是「我是谁、我有什么权限」的唯一落点，
         顺带让手机端不必再往顶栏塞用户名（顶栏只留两个导航项）。 -->
    <UCard :ui="{ body: 'p-4 sm:p-5' }">
      <div class="flex items-center gap-3">
        <div class="w-12 h-12 rounded-full bg-primary/10 flex items-center justify-center shrink-0">
          <UIcon name="i-lucide-user-round" class="w-6 h-6 text-primary" />
        </div>
        <div class="min-w-0 flex-1">
          <div class="flex items-center gap-0.5 min-w-0">
            <!-- ⚠️ 显示【原样】的用户名，不要美化。
                 顾客要退款 / 找管理员处理订单时，能报出来的唯一凭据就是这个用户名 ——
                 把它显示成「支付宝用户」等于把对账的路堵死（管理员对着四个字查不到人）。
                 用户名做成短的（alipay:8F3K2Q）本来就是【为了能念给别人听】。
                 详见 utils/format.js 上的说明。 -->
            <span class="font-semibold truncate">{{ session?.username || '—' }}</span>
            <UButton
              v-if="session?.username"
              size="xs"
              variant="ghost"
              color="neutral"
              icon="i-lucide-copy"
              title="复制用户名"
              @click="copyUsername"
            />
          </div>
          <div class="text-xs text-muted">{{ isAdmin ? '管理员' : '普通用户' }}</div>
        </div>
      </div>
    </UCard>

    <!-- 入口列表：点整行进入，箭头在右（手机上的标准做法）。
         用 divide-y 而不是逐项加边框，分隔线不会在两端多出半截。 -->
    <UCard :ui="{ body: 'p-0' }">
      <div class="divide-y divide-default">
        <button
          v-for="e in entries"
          :key="e.to"
          type="button"
          class="w-full flex items-center gap-3 px-4 py-3.5 text-left hover:bg-elevated/50 transition cursor-pointer"
          @click="router.push(e.to)"
        >
          <UIcon :name="e.icon" class="w-5 h-5 text-primary shrink-0" />
          <div class="flex-1 min-w-0">
            <div class="text-sm font-medium">{{ e.label }}</div>
            <div class="text-xs text-muted truncate">{{ e.hint }}</div>
          </div>
          <UIcon name="i-lucide-chevron-right" class="w-4 h-4 text-muted shrink-0" />
        </button>
      </div>
    </UCard>

    <UButton
      color="neutral"
      variant="outline"
      block
      icon="i-lucide-log-out"
      @click="emit('logout')"
    >登出</UButton>
  </div>
</template>

<script setup>
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { copyText } from '../utils/clipboard'

const props = defineProps({ session: Object })
const emit = defineEmits(['logout'])
const router = useRouter()
const toast = useToast()

// copyUsername 让顾客能把用户名报给管理员（退款、查订单都要它）。
// 手机上手动选中一段字很麻烦，所以给个按钮；复制不成功时把值念出来，
// 别让他对着一个"复制失败"干瞪眼。
async function copyUsername() {
  const name = props.session?.username
  if (!name) return
  const ok = await copyText(name)
  toast.add(ok
    ? { title: '已复制用户名', description: '联系管理员时把它报过去即可', color: 'success', icon: 'i-lucide-check' }
    : { title: '复制失败', description: `请手动记下用户名：${name}`, color: 'warning', icon: 'i-lucide-alert-triangle' })
}

const isAdmin = computed(() => props.session?.role === 'admin')

// 管理类入口只对管理员出现 —— 与旧顶栏「管理 / 驱动」按钮的可见性条件一致。
const entries = computed(() => {
  const list = [
    { label: '打印记录', icon: 'i-lucide-history', to: '/records', hint: '历史任务与重新打印' },
    { label: '我的钱包', icon: 'i-lucide-wallet', to: '/wallet', hint: '余额、充值、打印订单、流水' },
    { label: '定时打印', icon: 'i-lucide-calendar-clock', to: '/scheduled', hint: '按时间自动打印' }
  ]
  if (isAdmin.value) {
    list.push(
      { label: '系统管理', icon: 'i-lucide-settings', to: '/admin', hint: '用户、价目、系统设置' },
      { label: '驱动管理', icon: 'i-lucide-puzzle', to: '/drivers', hint: '安装与管理打印机驱动' }
    )
  }
  return list
})
</script>
