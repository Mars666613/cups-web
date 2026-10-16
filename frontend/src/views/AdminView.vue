<template>
  <div class="p-3 sm:p-4 md:p-6 max-w-3xl mx-auto space-y-4">
    <h1 class="text-lg font-bold flex items-center gap-2">
      <UIcon name="i-lucide-settings" class="w-5 h-5 text-primary" />
      系统管理
    </h1>

    <!-- 设备异常横幅：进管理端第一眼要能看见「现在有台机器出问题了」。
         它补的是通知够不着的那一段 —— 聊天群里的告警消息会被刷下去，
         而横幅只要打开这个页面就还在。 -->
    <UButton
      v-if="openAlerts > 0"
      block
      color="error"
      variant="soft"
      icon="i-lucide-bell-ring"
      to="/admin/alerts"
      class="justify-start text-left"
    >
      有 {{ openAlerts }} 条设备异常未恢复（缺纸 / 卡纸 / 耗材 / 不可达），点这里查看
    </UButton>

    <!-- 落地菜单：每个板块各自是一个子页，这里只做入口。
         以前六块全堆在这一页，进页面就并行拉四个【全站】列表
         （用户 / 打印记录 / 订单 / 流水，后端都无上限），数据一多就卡；
         拆成子路由后每个页面只加载自己那一份，代码也按需分包。 -->
    <UCard :ui="{ body: 'p-0' }">
      <div class="divide-y divide-default">
        <button
          v-for="e in ADMIN_SECTIONS"
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
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
// 入口清单与子页标签栏共用同一份定义（见 utils/adminSections.js）——
// 各写一份的话，新加一个子页只会出现在其中一处。
import { ADMIN_SECTIONS } from '../utils/adminSections'
import { readJSON } from '../utils/api'

const router = useRouter()

// 未恢复的设备异常条数。拉不到就是 0 —— 这是唯一一个「失败时静默」的接口：
// 一个读不到的数字不该把整个管理首页变成错误页，而它本来也只是个提醒。
const openAlerts = ref(0)

onMounted(async () => {
  try {
    const resp = await fetch('/api/admin/device-events?status=open&page=1', { credentials: 'include' })
    if (!resp.ok) return
    const data = await readJSON(resp)
    openAlerts.value = Number(data?.openCount || 0)
  } catch {
    // 忽略：横幅只是锦上添花，失败不打扰任何人。
  }
})
</script>
