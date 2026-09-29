// 计费内核的前端镜像 —— 与 Go 侧 internal/pricing/pricing.go 逐行对应。
//
// 存在的理由：用户提交前要看价格。但不能让前端自己拍脑袋算，
// 否则一旦两边算法漂移，用户会看到「显示 ¥0.80、实际扣 ¥1.20」，
// 而且他无从判断哪个是对的。
//
// 所以两份实现跑【同一套用例】：
//   用例文件  testdata/pricing_cases.json
//   后端      internal/pricing/shared_test.go（go test）
//   前端      frontend/scripts/check-pricing.js（node，无需测试框架）
//
// ⚠️ 改这里的算法时必须同步改 Go 侧，否则 npm run check:pricing 会失败。
//
// 前端算出来的只是「展示价」：真正扣多少以服务端下单响应里的
// amountCents / billedPages 为准（两者在页数统计上可能有 1 页的差异，
// 见 PdfCanvas 与 countPages 的口径不同）。

/** 计价错误。name 与 Go 侧的哨兵错误变量同名，便于对照与用例匹配。 */
export class PricingError extends Error {
  constructor(name, message) {
    super(message)
    this.name = name
  }
}

const ERR_INVALID_PAGE_RANGE = 'ErrInvalidPageRange'
const ERR_NO_PAGES_SELECTED = 'ErrNoPagesSelected'
const ERR_UNSUPPORTED_COMBINATION = 'ErrUnsupportedCombination'

// 把各种全角/异体标点归一化成 ASCII，与 PrintOptions.vue 的 validatePageRange 一致。
const SEPARATORS = [
  ['－', '-'], ['—', '-'], ['–', '-'], ['―', '-'], // 全角/破折号 → 半角连字符
  ['，', ','], ['、', ','], // 全角逗号、顿号 → 半角逗号
  ['；', ';'], // 全角分号
  ['　', ' '] // 全角空格
]

// 连字符两侧的空白先粘掉，避免 "1 - 3" 被切成三段、中间那个孤立连字符被判非法。
// 对应 Go 侧的 hyphenSpaceRe。
const HYPHEN_SPACE_RE = /[ \t]*-[ \t]*/g

function normalizeSeparators(s) {
  let out = s
  for (const [from, to] of SEPARATORS) {
    out = out.split(from).join(to)
  }
  return out
}

/** 严格解析页码。返回 [[lo,hi],...]。非法写法抛 PricingError。 */
export function parsePageRanges(s) {
  let text = normalizeSeparators(String(s ?? '')).trim()
  if (text === '') return []
  text = text.replace(HYPHEN_SPACE_RE, '-')

  const fields = text.split(/[ \t,;]+/).filter(Boolean)
  return fields.map(parseRangeToken)
}

function parseRangeToken(tok) {
  const idx = tok.indexOf('-')
  if (idx >= 0) {
    const loStr = tok.slice(0, idx)
    const hiStr = tok.slice(idx + 1)
    // 不允许 "1-2-3"（hiStr 里还有 '-'）或 "-5"（loStr 为空）。
    if (loStr === '' || hiStr === '' || hiStr.includes('-')) {
      throw new PricingError(ERR_INVALID_PAGE_RANGE, `页码写法非法: ${tok}`)
    }
    const lo = parsePositiveInt(tok, loStr)
    const hi = parsePositiveInt(tok, hiStr)
    if (hi < lo) {
      // "5-3" 在 IPP 侧会被静默丢弃，结果是整个文档全打 —— 必须拦住。
      throw new PricingError(ERR_INVALID_PAGE_RANGE, `区间起点大于终点: ${tok}`)
    }
    return [lo, hi]
  }
  const n = parsePositiveInt(tok, tok)
  return [n, n]
}

function parsePositiveInt(tok, s) {
  // 必须用正则卡死：JS 的 parseInt("3abc") 会得到 3，
  // 而 Go 的 strconv.Atoi("3abc") 是报错的 —— 两边行为必须一致。
  if (!/^\d+$/.test(s)) {
    throw new PricingError(ERR_INVALID_PAGE_RANGE, `页码写法非法: ${tok}`)
  }
  const n = Number(s)
  if (n < 1) {
    throw new PricingError(ERR_INVALID_PAGE_RANGE, `页码必须 >= 1: ${tok}`)
  }
  return n
}

/** 输出去重、排序、合并相邻/重叠区间后的规范形式。空输入返回空串。 */
export function normalizePageRange(s) {
  const ranges = parsePageRanges(s)
  if (ranges.length === 0) return ''

  ranges.sort((a, b) => a[0] - b[0] || a[1] - b[1])

  const merged = [ranges[0].slice()]
  for (const r of ranges.slice(1)) {
    const last = merged[merged.length - 1]
    // 相邻（1-3 与 4-5）也合并成 1-5，两者之间没有空洞。
    if (r[0] <= last[1] + 1) {
      if (r[1] > last[1]) last[1] = r[1]
      continue
    }
    merged.push(r.slice())
  }

  return merged
    .map(([lo, hi]) => (lo === hi ? String(lo) : `${lo}-${hi}`))
    .join(' ')
}

/** 计算本次实际会着墨的页数（不含份数、不含 N-up 折算）。语义见 Go 侧同名函数的注释。 */
export function countBilledPages(pages, pageRange, pageSet) {
  let n = Number(pages)
  if (!Number.isFinite(n) || n < 1) n = 1

  const set = String(pageSet ?? '').trim().toLowerCase()
  const rangeStr = String(pageRange ?? '').trim()

  if (set === 'even-reverse') {
    if (rangeStr !== '') {
      throw new PricingError(
        ERR_UNSUPPORTED_COMBINATION,
        '偶数页倒序不能与页码范围同时使用'
      )
    }
    // N <= 1 时重排条件（pages > 1）不满足，实际原样打印整份文档。
    if (n <= 1) return n
    return Math.floor(n / 2)
  }

  let selected = selectPages(n, rangeStr)

  if (set === 'odd') selected = selected.filter((p) => p % 2 === 1)
  else if (set === 'even') selected = selected.filter((p) => p % 2 === 0)

  if (selected.length === 0) {
    throw new PricingError(ERR_NO_PAGES_SELECTED, '选择范围内没有可打印的页面')
  }
  return selected.length
}

function selectPages(n, pageRange) {
  const all = () => Array.from({ length: n }, (_, i) => i + 1)
  if (pageRange === '') return all()

  const ranges = parsePageRanges(pageRange)
  if (ranges.length === 0) return all()

  // 用集合去重：写 "1-3 2-5" 时真正的页数是 5 而不是 7。
  const seen = new Set()
  for (const [rawLo, rawHi] of ranges) {
    const lo = Math.max(rawLo, 1)
    const hi = Math.min(rawHi, n) // 越界裁剪
    for (let p = lo; p <= hi; p++) seen.add(p)
  }
  return [...seen].sort((a, b) => a - b)
}

/** 按 (是否彩色, 纸型, 纸张类型) 查单价。未命中返回 fallbackCents 与 false。 */
export function lookupUnitCents(rules, isColor, paperSize, paperType, fallbackCents) {
  let fallback = Number(fallbackCents) || 0
  if (fallback < 0) fallback = 0

  const size = normalizeDim(paperSize)
  const ptype = normalizeDim(paperType)

  for (const r of rules || []) {
    if (
      Boolean(r.isColor) === Boolean(isColor) &&
      normalizeDim(r.paperSize) === size &&
      normalizeDim(r.paperType) === ptype
    ) {
      return { unitCents: Number(r.unitCents) || 0, matched: true }
    }
  }
  return { unitCents: fallback, matched: false }
}

function normalizeDim(s) {
  return String(s ?? '').trim().toUpperCase()
}

/**
 * estimateImageDocPages 多张图片【未合并成 PDF】时的页数。
 *
 * 提交时前端会先调 /api/convert 把多图合成 PDF 再上传，后端
 * convertImagesMultiToPDF 在 per_page<=1 时一张图一页，设了「每页张数」
 * 则按行优先网格排版、向上取整。
 *
 * ⚠️ 这个预测【必须】与后端最终的 PDF 页数一致：不一致就会出现
 * 「价格条显示 1 页的钱、实际按 N 页扣款」。实测就踩过 ——
 * 多图未合并时前端拿不到文件（图存在 selectedImages 里，不是 pdfBlob），
 * 页数停在默认值 1。
 *
 * 本函数的算术在 verify-images.sh 里对着真实转换结果验证过。
 */
export function estimateImageDocPages(imageCount, imagesPerPage) {
  const per = Math.max(1, Number(imagesPerPage) || 1)
  const n = Math.max(0, Number(imageCount) || 0)
  if (n === 0) return 1
  return Math.ceil(n / per)
}

/**
 * 下单计价的唯一入口，与 Go 侧 Compute 对应。
 * 返回 { billedPages, copies, unitCents, amountCents, matched }。
 */
export function computeQuote({
  pages,
  copies,
  isColor,
  paperSize,
  paperType,
  pageRange,
  pageSet,
  rules,
  fallbackCents
}) {
  let nCopies = Number(copies)
  if (!Number.isFinite(nCopies) || nCopies < 1) nCopies = 1

  const billedPages = countBilledPages(pages, pageRange, pageSet)
  const { unitCents, matched } = lookupUnitCents(
    rules,
    isColor,
    paperSize,
    paperType,
    fallbackCents
  )

  return {
    billedPages,
    copies: nCopies,
    unitCents,
    amountCents: unitCents * billedPages * nCopies,
    matched
  }
}
