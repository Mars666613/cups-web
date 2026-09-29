// 将 pdfjs-dist 自带的 cmaps 与 standard_fonts 资源拷贝到 public/pdfjs/，
// 供 <PdfCanvas> 通过 cMapUrl / standardFontDataUrl 加载，避免 CJK 字体和 UniGB-UCS2-H
// 等外部 CMap 编码下中文预览为方块或空白。
//
// 在 `bun run dev` / `bun run build` 之前由 predev / prebuild 钩子自动执行。
import { cp, mkdir, access } from 'node:fs/promises'
import { constants as fsConstants } from 'node:fs'
import { resolve, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const scriptDir = dirname(fileURLToPath(import.meta.url))
const pdfjsRoot = resolve(scriptDir, '../node_modules/pdfjs-dist')
const outRoot = resolve(scriptDir, '../public/pdfjs')

async function ensureSrcExists(path) {
  try {
    await access(path, fsConstants.R_OK)
    return true
  } catch {
    return false
  }
}

async function copyDir(name) {
  const src = resolve(pdfjsRoot, name)
  const dst = resolve(outRoot, name)
  if (!(await ensureSrcExists(src))) {
    console.warn(`[pdfjs] source not found, skipped: ${src}`)
    return
  }
  await cp(src, dst, { recursive: true, force: true })
  console.log(`[pdfjs] copied ${name} -> ${dst}`)
}

async function main() {
  await mkdir(outRoot, { recursive: true })
  await copyDir('cmaps')
  await copyDir('standard_fonts')
}

main().catch(err => {
  console.error('[pdfjs] copy failed:', err)
  process.exit(1)
})
