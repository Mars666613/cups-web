<template>
  <!-- 子页顶部的横向切换条。
       手机上六个标签放不下，所以允许横向滚动（shrink-0 防止被压扁换行）。 -->
  <div class="flex gap-1 overflow-x-auto pb-1">
    <UButton
      v-for="s in ADMIN_SECTIONS"
      :key="s.to"
      :to="s.to"
      :icon="s.icon"
      :variant="current === s.to ? 'soft' : 'ghost'"
      :color="current === s.to ? 'primary' : 'neutral'"
      size="xs"
      class="shrink-0"
    >{{ s.label }}</UButton>
  </div>
</template>

<script setup>
import { ADMIN_SECTIONS } from '../../utils/adminSections'

// 传当前子路由的 path。用 prop 而不是读 route：调用方一眼能看出自己是谁，
// 而且这个组件不依赖路由实例，将来在别处复用不必再配一次。
defineProps({ current: String })
</script>
