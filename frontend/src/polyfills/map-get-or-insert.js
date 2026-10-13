// Map / WeakMap 的 getOrInsert / getOrInsertComputed polyfill
//（TC39「Upsert」提案，2026 年 1 月进入 Stage 4）。
//
// 背景：pdfjs-dist 5.x 拿它们缓存每个 document 派生的东西 —— 私有字段
// `#methodPromises` 就是个 Map/WeakMap，取值走 `getOrInsertComputed`。
// 这是很新的原生方法（Chrome 139 前后才内置），旧内核浏览器上直接抛：
//
//   this[#methodPromises].getOrInsertComputed is not a function
//
// 症状与 Issue #86（`Uint8Array.prototype.toHex`）、Issue #89
// （`Promise.withResolvers`）完全同类：PDF 在【加载阶段】就炸，整份无法预览，
// 而图片走另一条渲染路径不受影响。所以「PC 上能预览、手机上不能」是它的
// 典型表现 —— 根因是浏览器版本，不是文件、不是网络。
//
// ⚠️ 必须同时在主线程（main.js）与 pdf.js worker（pdf-worker.js）中导入：
// 二者是相互独立的 JS 执行环境，主线程补上了 worker 里照样缺。
//
// 已经内置这些方法的浏览器会跳过，零副作用。

// defineGetOrInsert 给一个构造器装上两个方法。
//
// 提案里四个方法（Map/WeakMap × getOrInsert/getOrInsertComputed）语义完全一致，
// 只是挂在不同的原型上，所以共用一份实现。
function defineGetOrInsert(Ctor) {
  if (typeof Ctor.prototype.getOrInsert !== 'function') {
    Object.defineProperty(Ctor.prototype, 'getOrInsert', {
      value: function getOrInsert(key, defaultValue) {
        if (!this.has(key)) {
          this.set(key, defaultValue)
        }
        return this.get(key)
      },
      writable: true,
      configurable: true,
    })
  }

  if (typeof Ctor.prototype.getOrInsertComputed !== 'function') {
    Object.defineProperty(Ctor.prototype, 'getOrInsertComputed', {
      value: function getOrInsertComputed(key, callback) {
        if (typeof callback !== 'function') {
          throw new TypeError('getOrInsertComputed requires a callback function')
        }
        // ⚠️ callback 只在【键不存在】时调用。
        // 这正是它与 `getOrInsert(key, callback(key))` 的全部区别，也是这个
        // 方法存在的理由 —— 写错成无条件调用，会白白重建一遍派生对象，
        // pdfjs 那边表现为每页重复解析，慢但不易察觉。
        if (!this.has(key)) {
          this.set(key, callback(key))
        }
        return this.get(key)
      },
      writable: true,
      configurable: true,
    })
  }
  // WeakMap 的非法键（原始值）由底层的 has/set 自己抛 TypeError，
  // 这里不必重复校验提案里的 CanBeHeldWeakly。
}

if (typeof Map !== 'undefined') defineGetOrInsert(Map)
if (typeof WeakMap !== 'undefined') defineGetOrInsert(WeakMap)
