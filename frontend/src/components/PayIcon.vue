<script setup>
// 渠道图标：有官方 logo 的用 logo，没有的回落 lucide 图标。
//
// 尺寸用 tailwind 类让调用方给（按钮里、下拉项里、行内小标签里各不一样）。
// alt 一律留空：它旁边永远跟着「支付宝」这样的文字，写了读屏会把同一个
// 名字念两遍 —— 按钮那里更明显，按钮文案本身就念一遍。
import { payLogo, payIcon } from '../utils/payProvider'

defineProps({
  // 渠道标识：balance / alipay / wechat / mock
  provider: { type: String, default: '' },
  size: { type: String, default: 'w-5 h-5' },
  // 只管回落图标：品牌 logo 自带颜色，不该被染
  iconClass: { type: String, default: 'text-primary' }
})
</script>

<template>
  <img v-if="payLogo(provider)" :src="payLogo(provider)" :class="[size, 'shrink-0']" alt="" />
  <UIcon v-else :name="payIcon(provider)" :class="[size, 'shrink-0', iconClass]" />
</template>
