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
import { parse, compileScript, compileTemplate } from 'vue/compiler-sfc'

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

// ── 检查 3：顶层 watch 的源不能引用后面才声明的 ref ──
//
// `watch([a, b, c], cb)` 和 `watch(ref, cb)` 的【源参数是立即求值的】。
// 源里的标识符若声明在下面几百行，setup 会踩暂时性死区：
//
//     Cannot access 'quickText' before initialization
//
// 而 setup 抛异常 = 整个页面白屏。构建、类型、本文件其它检查全是绿的 ——
// 只有浏览器控制台能看到。实测踩过：一条 saveSession 的 watch 放在文件
// 前段，源数组里却引用了中部才声明的 quickText，手机上一打开就是白屏。
//
// ⚠️ 只能查【直接引用】。间接的（顶层调用一个 hoisted 函数、函数体里再
// 引用后面的 ref）静态看不出来 —— 那种一律放到 setup 末尾。
for (const file of walk(srcDir)) {
  if (!file.endsWith('.vue')) continue
  const text = readFileSync(file, 'utf8')
  const s = text.indexOf('<script setup>')
  if (s < 0) continue
  const lines = text.slice(s + 14).split('\n')
  const baseLine = text.slice(0, s).split('\n').length // <script setup> 的绝对行号

  // 顶层声明 → 行号
  const decl = new Map()
  lines.forEach((l, i) => {
    const m = l.match(/^(?:const|let|var)\s+([A-Za-z_$][\w$]*)/)
    if (m) decl.set(m[1], baseLine + i)
  })

  lines.forEach((l, i) => {
    if (!/^watch\(/.test(l)) return // 只认第 0 列的顶层 watch
    const abs = baseLine + i
    const head = l.slice(l.indexOf('(') + 1)
    // 取第一个参数：配平括号，遇到顶层逗号即止
    let depth = 0
    let arg = ''
    for (const ch of head) {
      if ('([{'.includes(ch)) depth++
      else if (')]}'.includes(ch)) {
        if (depth === 0) break
        depth--
      } else if (ch === ',' && depth === 0) break
      arg += ch
    }
    // 源里的裸标识符（数组形式逐个取，单 ref 形式就一个）
    const names = arg
      .replace(/[[\]]/g, ' ')
      .split(',')
      .map(x => x.trim())
      .filter(x => /^[A-Za-z_$][\w$]*$/.test(x))
    for (const n of names) {
      const d = decl.get(n)
      if (d !== undefined && d > abs) {
        failures.push(
          `${relative(process.cwd(), file)}:${abs}\n` +
            `      watch(${arg.trim()}, …)\n` +
            `      源参数是【立即求值】的，但 ${n} 声明在第 ${d} 行 —— 会踩暂时性死区。\n` +
            `      setup 抛异常 = 整页白屏，而构建与其它检查都是绿的。\n` +
            `      把这条 watch 移到 ${n} 声明之后（通常就是 setup 末尾）。`
        )
      }
    }
  })
}

// ── 检查 4：模板里引用的名字必须在 <script setup> 里有定义 ──
//
// Vite 构建【完全不检查模板】：模板里写一个拼错的名字、或拆文件时漏搬一个
// ref，构建照过、其它检查也照过，只有进浏览器才报
//   "Property "xxx" was accessed during render but is not defined on instance"
// 而且只炸那一个页面。实测就是拆 AdminView 时最容易漏的东西。
//
// 判据直接来自 Vue 编译器本身，不是正则猜的：<script setup> 下能解析到的
// 标识符会编成 `$setup.x`，解析不到的才落到 `_ctx.x`。所以扫编译产物里的
// `_ctx.` 就是精确的「这个模板引用了脚本里没有的东西」。
const VUE_INSTANCE_PROPS = new Set([
  // Vue 的实例属性由运行时挂在实例代理上（$emit 声明了 emits、$slots 是插槽），
  // 模板里合法，但编译器同样把它们编成 _ctx.$xxx —— 必须显式放过。
  '$attrs', '$emit', '$slots', '$props', '$refs', '$el', '$options', '$parent',
  '$root', '$data', '$route', '$router', '$forceUpdate', '$nextTick', '$watch'
])

for (const file of walk(srcDir)) {
  if (!file.endsWith('.vue')) continue
  const { descriptor, errors } = parse(readFileSync(file, 'utf8'), { filename: file })
  // 解析不过的文件交给构建去报，不在这里抢；没有模板或没有 <script setup> 的跳过。
  if (errors.length || !descriptor.template || !descriptor.scriptSetup) continue

  let bindings
  try {
    bindings = compileScript(descriptor, { id: 'check' }).bindings
  } catch {
    continue
  }
  const { code } = compileTemplate({
    source: descriptor.template.content,
    filename: file,
    id: 'check',
    compilerOptions: { bindingMetadata: bindings }
  })

  const missing = new Set()
  for (const m of code.matchAll(/_ctx\.([A-Za-z_$][\w$]*)/g)) {
    if (!VUE_INSTANCE_PROPS.has(m[1])) missing.add(m[1])
  }
  if (missing.size) {
    failures.push(
      `${relative(process.cwd(), file)}\n` +
        `      模板引用了 <script setup> 里没有的名字：${[...missing].join(', ')}\n` +
        `      Vite 构建不检查模板，这类错误只在浏览器里炸，且只炸这一个页面。\n` +
        `      拆文件时漏搬 ref、或名字拼错，都是这个症状。`
    )
  }
}

if (failures.length > 0) {
  console.error(`\n前端检查失败：${failures.length} 处\n`)
  console.error(failures.join('\n\n'))
  console.error('')
  process.exit(1)
}
console.log('前端静态检查通过')
