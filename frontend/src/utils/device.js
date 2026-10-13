// 设备判断。
//
// 这个程序主要给手机用（手机访问部署好的网址 → 传文件 → 打印），但电脑上
// 也得能用。两种设备付款的动作完全不同，服务端猜不出来，只能由前端告诉它：
//
//   手机 → 手机网站支付：跳收银台，收银台自动唤起支付宝 App
//   电脑 → 当面付二维码：电脑上不一定装了支付宝，只能让手机来扫
//
// ⚠️ 它只用来决定【走哪条路】，两条路各自都留着退路 ——
// 判断错了不会把人堵死。想「精确识别设备」是做不到的，别在这上面加码。
export function isMobileDevice() {
  if (typeof navigator === 'undefined') return false
  const ua = navigator.userAgent || ''
  if (/Android|iPhone|iPad|iPod|Windows Phone|Mobile/i.test(ua)) return true
  // UA 可能被伪装；触屏 + 窄屏再兜一层。
  const coarse = typeof window !== 'undefined' && window.matchMedia
    ? window.matchMedia('(pointer: coarse)').matches
    : false
  return coarse && typeof window !== 'undefined' && window.innerWidth < 768
}

// payChannel 返回该发给服务端的收款方式，取值与 internal/payment 的
// ChannelWap / ChannelPage 一一对应。
//
//  手机 → 'wap'   手机网站支付（跳收银台，自动唤起 App）
//  电脑 → 'page'  电脑网站支付（跳收银台，页面上有二维码可扫）
//
// ⚠️ 两条都是「跳收银台」—— 用户离开我们的页面去支付宝，付完由支付宝送回
// return_url。服务端据 channel 决定调 wap.pay 还是 page.pay。
//
// 后端还留着 'qrcode'（当面付 precreate，二维码直接渲染在我们页面上、不跳转，
// 体验更连贯），但当面付是线下收银场景的产品，网页应用一般签不下来 ——
// 官方给网页应用的就是上面两个。哪天签到了，把这里改回 'qrcode' 即可。
export function payChannel() {
  return isMobileDevice() ? 'wap' : 'page'
}

// isAlipayClient 判断当前是不是在支付宝内置浏览器里。
//
// ⚠️ 这个判断对「支付宝授权登录」是【功能性】的，不是锦上添花：
// 官方原文——「如果网站支付宝登录在手机端 H5 页面进行授权，必须要在支付宝
// 客户端内去打开 H5 页面，否则会提示请在支付宝客户端内打开授权的错误」。
// 真机实测：在系统浏览器里直接开授权链接，得到的是一个【死路】页面
// （「抱歉，出错了 / 请在支付宝客户端打开链接」），上面连个可点的按钮都没有。
//
// 所以「不在支付宝里」时不能去开授权链接，只能把用户连页面一起送进支付宝。
export function isAlipayClient() {
  if (typeof navigator === 'undefined') return false
  return /AlipayClient/i.test(navigator.userAgent || '')
}
