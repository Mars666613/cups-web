import { ref, computed, watch } from 'vue'

// 表格分页。
//
// ⚠️ 为什么是【自己切数组】，而不是用 UTable 的 `pagination` 属性：
// @nuxt/ui 4.7.1 的 UTable 只注册了 getCoreRowModel / getFilteredRowModel /
// getSortedRowModel / getExpandedRowModel —— **没有 getPaginationRowModel**。
// 它那个 `pagination` 只是状态透传（配 `onPaginationChange` 用），
// 传进去【不会】真的切片，表格照常渲染全部行。
//
// 也就是说：只加 `:pagination="{ pageIndex, pageSize }"` 的写法看着很对、
// 构建也过、静态检查也过，但页面长度【一点没变】。要真分页就得自己 slice
// （或者通过 `paginationOptions` 把 getPaginationRowModel 塞进去，那要额外
// 依赖 @tanstack/vue-table，不划算）。
const DEFAULT_PAGE_SIZE = 20

export function usePagedRows(rowsRef, pageSize = DEFAULT_PAGE_SIZE) {
  const page = ref(1)
  const total = computed(() => rowsRef.value?.length ?? 0)
  const pageCount = computed(() => Math.max(1, Math.ceil(total.value / pageSize)))
  const rows = computed(() =>
    (rowsRef.value || []).slice((page.value - 1) * pageSize, page.value * pageSize)
  )

  // 数据变少时把页码夹回来。
  //
  // 少了这一步就会停在空页：在最后一页删掉唯一一条记录，total 变小、
  // pageCount 变小，而 page 还停在旧值 —— 表格一片空白，用户以为数据没了。
  watch(pageCount, (n) => {
    if (page.value > n) page.value = n
  })

  // 重新查询时归零。
  //
  // 与上面那条是【两个方向】，少任何一个都会出现空表：
  //   · 这条：结果集【换了一批】，旧页码没有意义了（在第 5 页搜出 3 条 → 空白）
  //   · 上面：结果集【变小了】，旧页码超界了
  //
  // ⚠️ 别把归零塞进 loader 里。退款 / 调额之后也会重跑 loader，那时用户
  // 在第三页操作完被弹回第一页，很难受。只在「点了查询」时归零。
  function resetPage() {
    page.value = 1
  }

  return { page, pageSize, total, pageCount, rows, resetPage }
}
