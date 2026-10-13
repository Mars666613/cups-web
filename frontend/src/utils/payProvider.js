// 支付渠道的图形与名称。
//
// ⚠️ 这是【唯一】一份「渠道 → 图形」的映射。用户要求「所有涉及到支付的
// 地方都要有图标」，付一次款要经过的界面有五六个，各写各的必然漂移 ——
// 加一个渠道要改五处，漏一处的表现是「某个页面上那个渠道没图标」。
//
// ⚠️ 图形一律放 src/assets/ 本地，绝不引 CDN：这个程序部署在内网
// （典型是 192.168.x.x 的单容器），运行时取不到外网资源。引 CDN 的话
// 在开发机上看着一切正常，一上线只剩一个破图 —— 而且是最需要它出现的收款页。
import alipayLogo from '../assets/alipay.svg'
import wechatLogo from '../assets/wechat.svg'

// 有官方品牌图形的渠道。图形自带颜色，不要再去 `text-primary` 染色。
//
// ⚠️ wechat 这一项目前渲染不到任何界面上 —— 后端还没实现微信渠道
// （`buildProvider("wechat")` 返回 ErrUnavailable，`availableProviders`
// 也有意不列它，见 cmd/server/payment.go 的说明）。素材先备好，等渠道
// 真正开通时这一行自动生效，不需要再改。
export const PAY_LOGOS = {
  alipay: alipayLogo,
  wechat: wechatLogo
}

// 没有品牌图形的回落图标。
//
// 余额支付是我们自己的概念，本来就没有品牌标识，永远走这里；
// mock 是本地测试渠道，也不该占用谁的品牌。
export const PAY_ICONS = {
  balance: 'i-lucide-wallet',
  mock: 'i-lucide-flask-conical'
}

export function payLogo(id) {
  return PAY_LOGOS[id] || ''
}

export function payIcon(id) {
  return PAY_ICONS[id] || 'i-lucide-credit-card'
}

// 品牌短名，用在「去 X 付款」这种句子里。
// ⚠️ 不含「支付」二字：写成「去微信支付付款」就成了病句。
// 渠道选择列表里的名字是另一套（「微信支付」），别混用。
export function payProviderName(id) {
  return {
    alipay: '支付宝',
    wechat: '微信',
    mock: '模拟支付',
    balance: '余额'
  }[id] || ''
}
