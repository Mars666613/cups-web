// 校验前端计价镜像与后端实现完全一致。
//
//   npm run check:pricing      （在 frontend/ 目录下）
//
// 读的是前后端共用的用例文件 testdata/pricing_cases.json，
// 后端那边是 internal/pricing/shared_test.go。两边任何一边改错，
// 对应的检查就会失败 —— 这样才不会出现「前端显示 ¥0.80、后端扣 ¥1.20」。
//
// 刻意不引入测试框架：这是个一次性的对拍脚本，加 vitest 进来
// 会为了几十行逻辑拖进一整套依赖。

import { readFileSync } from 'node:fs'
import { resolve, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  parsePageRanges,
  normalizePageRange,
  countBilledPages,
  computeQuote
} from '../src/utils/pricing.js'

const scriptDir = dirname(fileURLToPath(import.meta.url))
const casesPath = resolve(scriptDir, '../../testdata/pricing_cases.json')

let passed = 0
const failures = []

function fail(group, name, detail) {
  failures.push(`  [${group}] ${name}\n      ${detail}`)
}

function check(group, name, fn) {
  try {
    fn()
    passed++
  } catch (err) {
    fail(group, name, err.message)
  }
}

function assertEqual(actual, expected, what) {
  const a = JSON.stringify(actual)
  const e = JSON.stringify(expected)
  if (a !== e) {
    throw new Error(`${what}: 实际 ${a}，期望 ${e}`)
  }
}

// wantErr 为空 = 期望成功；否则期望抛出的错误 name 与它一致。
function expectErr(expectedName, fn, what) {
  let result
  let thrown = null
  try {
    result = fn()
  } catch (err) {
    thrown = err
  }

  if (expectedName) {
    if (!thrown) {
      throw new Error(`${what}: 期望抛 ${expectedName}，实际成功返回 ${JSON.stringify(result)}`)
    }
    if (thrown.name !== expectedName) {
      throw new Error(`${what}: 期望抛 ${expectedName}，实际抛 ${thrown.name}(${thrown.message})`)
    }
    return null
  }

  if (thrown) {
    throw new Error(`${what}: 意外抛错 ${thrown.name}(${thrown.message})`)
  }
  return result
}

function main() {
  let cases
  try {
    cases = JSON.parse(readFileSync(casesPath, 'utf8'))
  } catch (err) {
    console.error(`读不到用例文件 ${casesPath}: ${err.message}`)
    process.exit(1)
  }

  // ── 解析 ──
  for (const c of cases.parsePageRanges || []) {
    check('ParsePageRanges', c.name, () => {
      const got = expectErr(c.wantErr, () => parsePageRanges(c.in), 'ParsePageRanges')
      if (c.wantErr) return
      assertEqual(got, c.want, '结果')
    })
  }

  // ── 归一化 ──
  for (const c of cases.normalizePageRange || []) {
    check('NormalizePageRange', c.name, () => {
      const got = expectErr(c.wantErr, () => normalizePageRange(c.in), 'NormalizePageRange')
      if (c.wantErr) return
      assertEqual(got, c.want, '结果')
    })
  }

  // ── 计费印数 ──
  for (const c of cases.countBilledPages || []) {
    check('CountBilledPages', c.name, () => {
      const got = expectErr(
        c.wantErr,
        () => countBilledPages(c.pages, c.pageRange, c.pageSet),
        'CountBilledPages'
      )
      if (c.wantErr) return
      assertEqual(got, c.want, '页数')
    })
  }

  // ── 报价 ──
  for (const c of cases.compute || []) {
    check('Compute', c.name, () => {
      const rules = c.noRules ? [] : cases.defaultRules || []
      const got = expectErr(
        c.wantErr,
        () =>
          computeQuote({
            pages: c.pages,
            copies: c.copies,
            isColor: c.isColor,
            paperSize: c.paperSize,
            paperType: c.paperType,
            pageRange: c.pageRange,
            pageSet: c.pageSet,
            rules,
            fallbackCents: c.fallbackCents
          }),
        'Compute'
      )
      if (c.wantErr) return

      assertEqual(got.billedPages, c.wantBilled, 'BilledPages')
      assertEqual(got.copies, c.wantCopies, 'Copies')
      assertEqual(got.unitCents, c.wantUnit, 'UnitCents')
      assertEqual(got.amountCents, c.wantAmount, 'AmountCents')
      assertEqual(got.matched, c.wantMatched, 'Matched')
    })
  }

  const total = passed + failures.length
  if (failures.length > 0) {
    console.error(`\n计价对拍失败：${failures.length}/${total} 个用例不通过\n`)
    console.error(failures.join('\n'))
    console.error('\n前端 frontend/src/utils/pricing.js 与后端 internal/pricing/pricing.go 已经不一致。')
    console.error('两者必须逐条对齐 —— 否则用户看到的价格和实际扣款会对不上。\n')
    process.exit(1)
  }
  console.log(`计价对拍通过：${passed}/${total} 个用例与 Go 侧实现一致`)
}

main()
