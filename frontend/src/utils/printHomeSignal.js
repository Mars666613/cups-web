import { ref } from 'vue'

// 「回到在线打印首页」的广播。
//
// ── 为什么需要它 ──
//
// 顶栏那个「在线打印」按钮的语义是「回到有五个模式的首页」。但五个模式
// 全都住在 /print 这一个路由里，靠组件内部状态切换 —— 用户进了「标准打印」
// 之后，地址栏仍然是 /print。这时 router.push('/print') 是一次【重复导航】，
// Vue Router 直接忽略，页面一动不动。
//
// 用户看到的是「这个按钮坏了」，而真相是那次点击等于什么都没发生。
// 真机现象：手机和电脑都回不去首页；差别只在于「先去个人中心再回来」
// 能让手机好 —— 那是因为它真的离开了这一页，回来时组件重新挂载。
//
// 与 paymentSignal 同一种写法：模块级 ref 当广播（那个文件里记着同一个
// 「已经在这一页了，路由操作是个空操作」的坑），谁关心就 watch 它。
export const printHomeSignal = ref(0)

export function requestPrintHome() {
  printHomeSignal.value += 1
}
