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
// ChannelWap / ChannelQRCode 一一对应。
export function payChannel() {
  return isMobileDevice() ? 'wap' : 'qrcode'
}
