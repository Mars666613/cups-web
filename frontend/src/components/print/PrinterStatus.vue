<template>
  <UCard>
    <template #header>
      <div class="flex items-center justify-between cursor-pointer select-none" @click="expanded = !expanded">
        <div class="flex items-center gap-2 font-semibold min-w-0">
          <UIcon name="i-lucide-activity" class="w-5 h-5 shrink-0" />
          <span class="truncate">打印机状态</span>
          <!-- 折叠时显示摘要 -->
          <template v-if="!expanded && printerInfo">
            <span class="text-xs text-muted truncate">{{ printerInfo.name || '未知' }}</span>
            <!-- 折叠时优先显示「连不上」：队列空闲的「空闲」看着一切正常，
                 而打印机可能根本没开机 —— 那才是用户需要立刻知道的。 -->
            <UBadge
              v-if="deviceUnreachable"
              color="error"
              variant="subtle"
              size="xs"
              class="shrink-0"
            >离线</UBadge>
            <UBadge v-else :color="printerStateColor(printerInfo.state)" variant="subtle" size="xs" class="shrink-0">
              {{ printerStateText(printerInfo.state) }}
            </UBadge>
          </template>
        </div>
        <div class="flex items-center gap-1 shrink-0">
          <UButton variant="ghost" size="xs" icon="i-lucide-refresh-cw" @click.stop="$emit('refresh')" :loading="loading" />
          <UIcon
            :name="expanded ? 'i-lucide-chevron-down' : 'i-lucide-chevron-right'"
            class="w-4 h-4 text-muted transition-transform duration-200"
          />
        </div>
      </div>
    </template>
    <div
      ref="contentRef"
      class="overflow-hidden transition-all duration-300 ease-in-out"
      :style="{ maxHeight: expanded ? maxContentHeight : '0px', visibility: expanded ? 'visible' : 'hidden' }"
    >
    <div>
      <div v-if="!printerUri" class="text-center py-6 text-muted text-sm">
        请先选择打印机
      </div>
      <div v-else-if="loading && !printerInfo" class="text-center py-4">
        <UIcon name="i-lucide-loader-circle" class="w-5 h-5 animate-spin mx-auto text-muted" />
      </div>
      <div v-else-if="error" class="text-center py-4 text-sm text-error">
        <UIcon name="i-lucide-wifi-off" class="w-5 h-5 mx-auto mb-1" />
        {{ error }}
      </div>
      <div v-else-if="printerInfo" class="space-y-3">
        <!-- ⚠️ 设备可达性：这里才是「打印机到底在不在」。
             cupsd 报的 state 是【队列】状态（没活儿干就是 idle，它不主动探打印机），
             墨量与纸张来自缓存和 PPD —— 打印机拔了电它们照样显示。
             只有这个字段是从设备本身问出来的。 -->
        <UAlert
          v-if="deviceUnreachable"
          color="error"
          variant="soft"
          icon="i-lucide-wifi-off"
          title="打印机无法连接"
          :description="`${printerInfo.device.message || '设备无响应'}。此时无法打印。`"
        />

        <!-- 队列状态：显式注明是「队列」的，避免与设备状态混淆 -->
        <div class="flex items-center justify-between p-2 bg-elevated rounded-lg">
          <div class="flex items-center gap-2">
            <UIcon name="i-lucide-info" class="w-4 h-4 text-info" />
            <span class="text-sm font-medium">队列状态</span>
            <span class="text-[10px] text-muted">（CUPS 队列，非打印机本身）</span>
          </div>
          <UBadge :color="printerStateColor(printerInfo.state)" variant="subtle" size="xs">
            {{ printerStateText(printerInfo.state) }}
          </UBadge>
        </div>

        <!-- 设备实时状态：只有直连成功时才有，是打印机自己报的 -->
        <div
          v-if="printerInfo.device?.probed && printerInfo.device.reachable"
          class="flex items-center justify-between p-2 bg-elevated rounded-lg"
        >
          <div class="flex items-center gap-2">
            <UIcon name="i-lucide-plug-zap" class="w-4 h-4 text-success" />
            <span class="text-sm font-medium">设备状态</span>
            <span class="text-[10px] text-muted">（直连打印机）</span>
          </div>
          <UBadge :color="printerStateColor(printerInfo.device.state || 'idle')" variant="subtle" size="xs">
            {{ printerInfo.device.state ? printerStateText(printerInfo.device.state) : '在线' }}
          </UBadge>
        </div>

        <!-- 队列 -->
        <div class="flex items-center justify-between p-2 bg-elevated rounded-lg">
          <div class="flex items-center gap-2">
            <UIcon name="i-lucide-list-ordered" class="w-4 h-4 text-primary" />
            <span class="text-sm font-medium">队列任务数</span>
          </div>
          <span class="text-sm font-bold">{{ printerInfo.queuedJobs }}</span>
        </div>

        <!-- 状态持续时间：仅在后端算出可信秒数时显示。
             CUPS 重启后 printer-state-change-date-time 保留的是旧会话墙钟，
             不代表"当前状态维持了多久"，不能作为兜底数据源（issue #45）。 -->
        <div v-if="printerInfo.stateDurationSeconds > 0" class="flex items-center justify-between p-2 bg-elevated rounded-lg">
          <div class="flex items-center gap-2">
            <UIcon name="i-lucide-clock" class="w-4 h-4 text-success" />
            <span class="text-sm font-medium">状态持续</span>
          </div>
          <span class="text-sm">{{ formatDurationSeconds(printerInfo.stateDurationSeconds) }}</span>
        </div>

        <!-- 固件版本 -->
        <div v-if="printerInfo.firmwareVersion" class="flex items-center justify-between p-2 bg-elevated rounded-lg">
          <div class="flex items-center gap-2">
            <UIcon name="i-lucide-cpu" class="w-4 h-4 text-secondary" />
            <span class="text-sm font-medium">固件版本</span>
          </div>
          <span class="text-xs text-muted truncate max-w-32">{{ printerInfo.firmwareVersion }}</span>
        </div>

        <!-- 状态消息 -->
        <div v-if="printerInfo.stateMessage" class="p-2 bg-warning/10 border border-warning/20 rounded-lg">
          <p class="text-xs text-warning">{{ printerInfo.stateMessage }}</p>
        </div>

        <!-- 墨盒信息。
             ⚠️ 设备不可达时这些是 cupsd 【上次连上时缓存】的旧值，
             不是实时的。必须标出来 —— 把缓存值当实时值展示，
             用户会以为墨盒刚查过，而其实打印机可能离线好几天了。 -->
        <div v-if="printerInfo.markerNames && printerInfo.markerNames.length > 0">
          <div class="flex items-center gap-2 mb-2 flex-wrap">
            <UIcon name="i-lucide-droplets" class="w-4 h-4 text-primary" />
            <span class="text-sm font-semibold">墨盒信息</span>
            <UBadge v-if="deviceUnreachable" color="warning" variant="subtle" size="xs">
              缓存值 · 打印机离线
            </UBadge>
          </div>
          <div class="space-y-2">
            <div v-for="(name, i) in printerInfo.markerNames" :key="i" class="space-y-1">
              <div class="flex justify-between text-xs">
                <span class="text-muted">{{ name }}</span>
                <span :class="markerLevelColor(printerInfo.markerLevels?.[i])">
                  {{ printerInfo.markerLevels?.[i] ?? '?' }}%
                </span>
              </div>
              <div class="w-full bg-muted/30 rounded-full h-2">
                <div
                  class="h-2 rounded-full transition-all"
                  :class="markerBarColor(printerInfo.markerLevels?.[i])"
                  :style="{ width: Math.max(0, Math.min(100, printerInfo.markerLevels?.[i] ?? 0)) + '%' }"
                ></div>
              </div>
            </div>
          </div>
        </div>

        <!-- 纸盒信息 -->
        <div v-if="printerInfo.mediaReady && printerInfo.mediaReady.length > 0">
          <div class="flex items-center gap-2 mb-2">
            <UIcon name="i-lucide-layers" class="w-4 h-4 text-secondary" />
            <span class="text-sm font-semibold">纸盒信息</span>
          </div>
          <div class="space-y-1">
            <div v-for="(media, i) in printerInfo.mediaReady" :key="i"
              class="flex items-center gap-2 p-1.5 bg-elevated rounded-lg text-xs">
              <UIcon name="i-lucide-square" class="w-3 h-3 text-muted" />
              <span>{{ media }}</span>
            </div>
          </div>
        </div>

        <!-- 状态原因 -->
        <div v-if="printerInfo.stateReasons && printerInfo.stateReasons.filter(r => r !== 'none').length > 0">
          <div class="flex items-center gap-2 mb-1">
            <UIcon name="i-lucide-alert-triangle" class="w-4 h-4 text-warning" />
            <span class="text-sm font-semibold">警报</span>
          </div>
          <div class="space-y-1">
            <div v-for="reason in printerInfo.stateReasons.filter(r => r !== 'none')" :key="reason"
              class="text-xs text-warning bg-warning/10 px-2 py-1 rounded-lg">
              {{ reason }}
            </div>
          </div>
        </div>
      </div>
    </div>
    </div>
  </UCard>
</template>

<script setup>
import { ref, computed, watch, onMounted, nextTick } from 'vue'
import { formatDurationSeconds, printerStateColor, printerStateText, markerLevelColor, markerBarColor } from '../../utils/format'

const props = defineProps({
  printerInfo: { type: Object, default: null },
  printerUri: { type: String, default: '' },
  loading: { type: Boolean, default: false },
  error: { type: String, default: '' }
})

defineEmits(['refresh'])

// deviceUnreachable 是「直连探测确认打印机连不上」。
//
// ⚠️ 必须同时看 probed：usb:// 这类队列没法用网络探，probed=false。
// 只看 reachable 的话，USB 打印机的用户会一直看到一条假的离线警告。
const deviceUnreachable = computed(() =>
  Boolean(props.printerInfo?.device?.probed && !props.printerInfo.device.reachable)
)

const expanded = ref(window.innerWidth >= 1024) // 仅用于初始折叠态，用户可手动切换展开/折叠
const contentRef = ref(null)
const innerHeight = ref(0)

const maxContentHeight = computed(() => {
  return innerHeight.value ? `${innerHeight.value}px` : '500px'
})

function measureContent() {
  nextTick(() => {
    if (contentRef.value) {
      // Temporarily remove max-height to measure real height
      const el = contentRef.value
      const prevMaxHeight = el.style.maxHeight
      const prevTransition = el.style.transition
      el.style.transition = 'none'
      el.style.maxHeight = 'none'
      innerHeight.value = el.scrollHeight
      el.style.maxHeight = prevMaxHeight
      // Force reflow then restore transition
      el.offsetHeight
      el.style.transition = prevTransition
    }
  })
}

watch(() => props.printerInfo, measureContent, { deep: true })
watch(() => props.loading, measureContent)
watch(() => props.error, measureContent)
onMounted(measureContent)
</script>
