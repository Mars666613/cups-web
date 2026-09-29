// 前端静态检查：把「构建能过、但一进浏览器就炸」的坑挡在提交之前。
//
//   npm run check:frontend
//
// 为什么需要它：Vite 构建只做语法与模板编译，组件在运行期的约束
// （比如 Reka UI 对 SelectItem 的取值限制）完全检查不到。实测踩过 ——
// 一个空字符串 value 打崩了整棵组件树，界面上点什么都没反应，
// 而构建、类型、lint 全绿。

import { readFileSync, readdirSync, statSync } from 'node:fs'
import { resolve, dirname, join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

const scriptDir = dirname(fileURLToPath(import.meta.url))
const srcDir = resolve(scriptDir, '../src')

function walk(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) walk(p, out)
    else if (/\.(vue|js)$/.test(name)) out.push(p)
  }
  return out
}

const failures = []

// ── 检查 1：SelectItem 的 value 不能是空字符串 ──
//
// Reka UI 把空串保留给「清空选择 / 显示 placeholder」，传进去会抛
// "A <SelectItem /> must have a value prop that is not an empty string"，
// 而且是在组件更新期抛 —— 整棵组件树崩掉，表现是「点什么都没反应」。
//
// 需要「全部」这类无筛选选项时，用一个显式哨兵值（如 'all'），
// 在发请求前翻译掉。
const EMPTY_SELECT_VALUE = /\bvalue:\s*(''|"")\s*[,\n}]/

for (const file of walk(srcDir)) {
  const text = readFileSync(file, 'utf8')
  // 只看作为 Select 选项列表的数组字面量：形如 `value: '',` 且同文件里
  // 用到了 USelect 或 items=
  if (!/USelect|:items=/.test(text)) continue

  const lines = text.split('\n')
  lines.forEach((line, i) => {
    if (EMPTY_SELECT_VALUE.test(line)) {
      failures.push(
        `${relative(process.cwd(), file)}:${i + 1}\n` +
          `      ${line.trim()}\n` +
          `      SelectItem 的 value 不能是空字符串（Reka UI 保留给「清空选择」）。\n` +
          `      需要「全部」选项时用显式哨兵值，例如 value: 'all'，发请求前翻译掉。`
      )
    }
  })
}

// ── 检查 2：带默认参数的函数不能被裸绑定到模板事件上 ──
//
// Vue 在 @click="fn" 这种【不带括号】的绑定里会把事件对象作为第一个实参
// 传进去。若函数签名是 fn(silent = false)，那个 MouseEvent 就是真值 ——
// 默认值被顶掉，手动点击会悄无声息地退化成「静默/自动」分支：
// 不弹错误、不打日志，只是行为不对。
//
// 实测踩过两次（syncTopup 与 startTopup 各一次），两次都是「看起来正常、
// 但失败时用户什么都看不到」。
//
// 只检查【带默认值】的参数：那种签名明确表示「这个参数是可选的」，
// 而裸绑定一定会把它填上。没有默认值的参数不在此列 —— @change="onSelect"
// 这类本来就指望拿到值的绑定是合法的。
//
// 修法：模板里写 @click="fn()"（带括号），或者把静默行为拆成独立的
// 内部函数（推荐，见 WalletView 的 runTopup / runSyncTopup）。
const FN_WITH_DEFAULT_PARAM = [
  /(?:async\s+)?function\s+([A-Za-z_$][\w$]*)\s*\(([^)]*)\)/g,
  /const\s+([A-Za-z_$][\w$]*)\s*=\s*(?:async\s*)?\(([^)]*)\)\s*=>/g
]

for (const file of walk(srcDir)) {
  if (!file.endsWith('.vue')) continue
  const text = readFileSync(file, 'utf8')

  const templateEnd = text.indexOf('</template>')
  if (templateEnd < 0) continue
  const template = text.slice(0, templateEnd)
  const script = text.slice(templateEnd)

  const risky = new Map()
  for (const re of FN_WITH_DEFAULT_PARAM) {
    re.lastIndex = 0
    let m
    while ((m = re.exec(script)) !== null) {
      // 参数表里出现 `=` 即为默认值（排除 `=>` 箭头与比较运算的误判：
      // 参数表本身不会含这两者）。
      if (m[2].includes('=')) risky.set(m[1], m[2].trim())
    }
  }
  if (risky.size === 0) continue

  for (const [name] of risky) {
    // 只匹配【恰好是函数名】的绑定：@click="fn"，不含括号、不含别的表达式。
    const bare = new RegExp(`@[\\w:.-]+="\\s*${name}\\s*"`, 'g')
    let m
    while ((m = bare.exec(template)) !== null) {
      const line = template.slice(0, m.index).split('\n').length
      failures.push(
        `${relative(process.cwd(), file)}:${line}\n` +
          `      ${m[0]}\n` +
          `      ${name}(${risky.get(name)}) 带默认参数，被裸绑定到事件上时\n` +
          `      Vue 会传入事件对象，把默认值顶掉，静默走进另一条分支。\n` +
          `      改成 @xxx="${name}()"，或把可选行为拆成独立函数。`
      )
    }
  }
}

if (failures.length > 0) {
  console.error(`\n前端检查失败：${failures.length} 处\n`)
  console.error(failures.join('\n\n'))
  console.error('')
  process.exit(1)
}
console.log('前端静态检查通过')
