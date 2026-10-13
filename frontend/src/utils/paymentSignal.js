import { ref } from 'vue'

// 支付到账的广播。
//
// ── 为什么需要它 ──
//
// 手机网站支付付完，支付宝把浏览器送回 return_url，页面是【重新加载】的。
// 补查单（App.vue 的 handlePaymentReturn）发生在 App 的 onMounted 里 ——
// 而 Vue 的 onMounted 是【子组件先跑】。于是回跳到钱包页时：
//
//   1. WalletView 挂载 → load() 拉余额（此刻钱还没结算，拿到的是旧值）
//   2. App.vue 挂载 → 查单 → 到账、写账本
//   3. router.replace('/wallet') —— 已经在这一页了，是个空操作，不会重新挂载
//
// 结果是用户看着「充值成功」的提示、余额却纹丝不动，得手动点刷新。
//
// 用模块级的 ref 当广播：谁关心到账就 watch 它，各自重拉自己的数据。
// 与 router/index.js 导出 cachedSession 是同一种「模块级共享状态」写法。
export const paymentSettledTick = ref(0)

export function announcePaymentSettled() {
  paymentSettledTick.value += 1
}
