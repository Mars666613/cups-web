// 系统管理各子页的【唯一事实来源】。
//
// 落地菜单（views/AdminView.vue）与子页标签栏（components/admin/AdminTabs.vue）
// 都从这里取。各写一份的话，新加一个子页只会出现在其中一处 ——
// 表现是「菜单里点得到、标签栏里切不过去」，看起来就像界面坏了。
//
// 顺序 = 标签栏的显示顺序：先数据查询类（用户 / 记录 / 订单 / 流水），
// 再配置类（计费 / 系统）。子路由本身在 router/index.js 注册。
export const ADMIN_SECTIONS = [
  { label: '用户管理', icon: 'i-lucide-users', to: '/admin/users', hint: '账号、角色、余额调整' },
  { label: '打印记录', icon: 'i-lucide-file-text', to: '/admin/records', hint: '全站打印任务与文件下载' },
  { label: '打印订单', icon: 'i-lucide-receipt', to: '/admin/orders', hint: '计费订单与手工退款' },
  { label: '余额流水', icon: 'i-lucide-scroll-text', to: '/admin/wallet', hint: '账本审计，只增不改' },
  { label: '计费设置', icon: 'i-lucide-wallet', to: '/admin/billing', hint: '计费开关、兜底单价、价目表' },
  { label: '取件终端', icon: 'i-lucide-scan-line', to: '/admin/kiosk', hint: '刷卡取件开关、终端与工牌绑定' },
  { label: '系统设置', icon: 'i-lucide-settings', to: '/admin/settings', hint: '清理、上传上限、访客模式、自定义 CSS' }
]
