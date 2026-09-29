<template>
  <div class="flex flex-col items-center gap-2">
    <!-- ⚠️ 二维码必须画在【白底】上：这个界面支持深色模式，深色底上的二维码
         对比度不足，手机经常扫不出来。白色内边距（quiet zone）同样是扫码
         成功率的一部分，不能省。 -->
    <div class="bg-white p-2 rounded-lg border border-default flex items-center justify-center">
      <img v-if="dataUrl" :src="dataUrl" :alt="alt" class="block" :style="boxStyle" />
      <div v-else-if="error" class="text-center px-2" :style="boxStyle">
        <UIcon name="i-lucide-triangle-alert" class="w-5 h-5 text-error mx-auto mb-1" />
        <div class="text-xs text-error">{{ error }}</div>
        <!-- 生成失败时把原文露出来：用户还能复制链接到支付宝里打开，
             总好过面对一个空白框无计可施。 -->
        <div class="mt-2 font-mono text-[10px] break-all text-muted select-all">{{ value }}</div>
      </div>
      <div v-else class="flex items-center justify-center" :style="boxStyle">
        <UIcon name="i-lucide-loader-circle" class="w-6 h-6 animate-spin text-muted" />
      </div>
    </div>
    <slot />
  </div>
</template>

<script setup>
import { ref, computed, watch } from 'vue'
import QRCode from 'qrcode'

const props = defineProps({
  // 要编码的内容。真实渠道下是渠道返回的 qr_code（一串 URL）。
  value: { type: String, default: '' },
  alt: { type: String, default: '支付二维码' },
  // 二维码边长（像素）。低于 160 在手机上会比较难扫，
  // 但屏宽紧张时可以调小。
  size: { type: Number, default: 200 }
})

const dataUrl = ref('')
const error = ref('')
const boxStyle = computed(() => ({ width: `${props.size}px`, height: `${props.size}px` }))

// 二维码生成是异步的。用户快速切换支付方式时会有多次生成在飞，
// 用一个自增序号丢弃过期结果 —— 否则后到的旧结果会覆盖掉新的。
let seq = 0

watch(
  () => [props.value, props.size],
  async ([v, size]) => {
    const mine = ++seq
    error.value = ''
    if (!v) {
      dataUrl.value = ''
      return
    }
    try {
      const url = await QRCode.toDataURL(v, {
        margin: 1,
        width: size,
        // M 级容错（~15%）是支付二维码的常规选择：既留出纠错余量，
        // 又不至于把码点撑得太密反而更难扫。
        errorCorrectionLevel: 'M'
      })
      if (mine !== seq) return
      dataUrl.value = url
    } catch (e) {
      if (mine !== seq) return
      dataUrl.value = ''
      error.value = '二维码生成失败'
    }
  },
  { immediate: true }
)
</script>
