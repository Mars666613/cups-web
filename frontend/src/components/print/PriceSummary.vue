<template>
  <UCard :ui="{ body: 'p-3 sm:p-4' }">
    <div class="flex flex-wrap items-baseline gap-x-3 gap-y-1">
      <UIcon name="i-lucide-receipt" class="w-4 h-4 text-primary self-center" />
      <span class="text-sm font-medium">本次费用</span>

      <template v-if="error">
        <span class="text-sm text-warning">{{ error }}</span>
      </template>

      <template v-else>
        <span class="text-lg font-bold text-primary">{{ formatCents(quote.amountCents) }}</span>
        <span class="text-xs text-muted">
          {{ formatCents(quote.unitCents) }}/页 × {{ quote.billedPages }} 页<template v-if="quote.copies > 1"> × {{ quote.copies }} 份</template>
        </span>
      </template>

      <div class="ml-auto flex items-center gap-2 text-sm">
        <span class="text-muted">余额</span>
        <span :class="insufficient ? 'text-error font-medium' : 'text-default'">
          {{ formatCents(balanceCents) }}
        </span>
        <UButton
          v-if="insufficient"
          size="xs"
          color="primary"
          variant="soft"
          icon="i-lucide-plus"
          @click="emit('go-wallet')"
        >去充值</UButton>
      </div>
    </div>

    <div v-if="!error && !quote.matched && quote.unitCents === 0" class="text-xs text-warning mt-2">
      该纸张组合尚未配置价目，将按 0 元计费。请联系管理员配置。
    </div>
    <div v-else-if="!error && !quote.matched" class="text-xs text-muted mt-2">
      该组合未单独定价，正按兜底单价 {{ formatCents(quote.unitCents) }}/页 计算。
    </div>
    <div v-if="!error && pagesEstimated" class="text-xs text-muted mt-1">
      页数为预估值，最终金额以提交后服务端确认为准。
    </div>
    <div v-if="!error && quote.billedPages !== docPages && !rangeNarrows" class="text-xs text-muted mt-1">
      按实际打印的 {{ quote.billedPages }} 页计费（共 {{ docPages }} 页）。
    </div>
  </UCard>
</template>

<script setup>
import { computed } from 'vue'
import { computeQuote } from '../../utils/pricing'
import { formatCents } from '../../utils/format'

const props = defineProps({
  docPages: { type: Number, default: 1 },
  pageRange: { type: String, default: '' },
  pageSet: { type: String, default: 'all' },
  isColor: { type: Boolean, default: true },
  paperSize: { type: String, default: 'A4' },
  paperType: { type: String, default: 'plain' },
  copies: { type: Number, default: 1 },
  // { enabled, currency, fallbackUnitCents, rules: [...] }
  prices: { type: Object, default: () => ({ rules: [], fallbackUnitCents: 0 }) },
  balanceCents: { type: Number, default: 0 },
  pagesEstimated: { type: Boolean, default: false }
})

const emit = defineEmits(['go-wallet'])

// 页码非法时 computeQuote 会抛错。PrintOptions 那边已经会禁用提交按钮，
// 这里只是把价格显示成一句提示，不重复报错。
const result = computed(() => {
  try {
    return {
      quote: computeQuote({
        pages: props.docPages,
        copies: props.copies,
        isColor: props.isColor,
        paperSize: props.paperSize,
        paperType: props.paperType,
        pageRange: props.pageRange,
        pageSet: props.pageSet,
        rules: props.prices?.rules || [],
        fallbackCents: props.prices?.fallbackUnitCents || 0
      }),
      error: ''
    }
  } catch (err) {
    return {
      quote: { billedPages: 0, copies: props.copies, unitCents: 0, amountCents: 0, matched: true },
      error: err?.message || '无法计算价格'
    }
  }
})

const quote = computed(() => result.value.quote)
const error = computed(() => result.value.error)

const insufficient = computed(() => !error.value && props.balanceCents < quote.value.amountCents)

// 用户填了页码范围/奇偶页时，印数少于总页数是预期行为，不需要额外解释。
const rangeNarrows = computed(() =>
  Boolean(props.pageRange?.trim()) || (props.pageSet && props.pageSet !== 'all')
)
</script>
