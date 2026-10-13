import { createRouter, createWebHashHistory } from 'vue-router'
import LoginView from '../views/LoginView.vue'
import PrintView from '../views/PrintView.vue'
import ProfileView from '../views/ProfileView.vue'

// 导航只有两项：在线打印（首页）与个人中心。
// 钱包 / 定时 / 管理 / 驱动 都收进个人中心当二级入口 ——
// 手机顶栏放不下五个按钮，而这几项的使用频率远低于打印。
const routes = [
  { path: '/', redirect: '/login' },
  { path: '/login', name: 'login', component: LoginView, meta: { requiresAuth: false } },
  { path: '/print', name: 'print', component: PrintView, meta: { requiresAuth: true } },
  { path: '/me', name: 'me', component: ProfileView, meta: { requiresAuth: true } },
  {
    // 刷卡取件的中间页：提交完提示「去哪台打印机刷工牌、什么时候之前」。
    // 与 /done 是兄弟但讲的是相反的事 —— 那边说「纸在打了」，
    // 这边说「纸还没出来，你得去刷卡」。见 PendingView.vue 的说明。
    // ?batch=<批次号> 决定展示哪一批。
    path: '/pending',
    name: 'pending',
    component: () => import('../views/PendingView.vue'),
    meta: { requiresAuth: true }
  },
  {
    // 打印/支付完成后的中间页（见 ResultView.vue 的说明）。
    // ?batch=<批次号> 决定展示哪一笔。
    path: '/done',
    name: 'done',
    component: () => import('../views/ResultView.vue'),
    meta: { requiresAuth: true }
  },
  {
    path: '/records',
    name: 'records',
    component: () => import('../views/RecordsView.vue'),
    meta: { requiresAuth: true }
  },
  // 系统管理拆成了「落地菜单 + 六个子页」。
  // 每个子页只加载自己那一份数据（以前六块同页，进页面就并行拉四个全站列表），
  // 并且全部懒加载 —— 管理端代码不再进首屏包。
  // 子页清单与标签栏共用 utils/adminSections.js 那一份定义。
  {
    path: '/admin',
    name: 'admin',
    component: () => import('../views/AdminView.vue'),
    meta: { requiresAuth: true, requiresAdmin: true }
  },
  {
    path: '/admin/users',
    name: 'admin-users',
    component: () => import('../views/admin/AdminUsersView.vue'),
    meta: { requiresAuth: true, requiresAdmin: true }
  },
  {
    path: '/admin/records',
    name: 'admin-records',
    component: () => import('../views/admin/AdminRecordsView.vue'),
    meta: { requiresAuth: true, requiresAdmin: true }
  },
  {
    path: '/admin/orders',
    name: 'admin-orders',
    component: () => import('../views/admin/AdminOrdersView.vue'),
    meta: { requiresAuth: true, requiresAdmin: true }
  },
  {
    path: '/admin/wallet',
    name: 'admin-wallet',
    component: () => import('../views/admin/AdminWalletView.vue'),
    meta: { requiresAuth: true, requiresAdmin: true }
  },
  {
    path: '/admin/billing',
    name: 'admin-billing',
    component: () => import('../views/admin/AdminBillingView.vue'),
    meta: { requiresAuth: true, requiresAdmin: true }
  },
  {
    path: '/admin/kiosk',
    name: 'admin-kiosk',
    component: () => import('../views/admin/AdminKioskView.vue'),
    meta: { requiresAuth: true, requiresAdmin: true }
  },
  {
    path: '/admin/settings',
    name: 'admin-settings',
    component: () => import('../views/admin/AdminSettingsView.vue'),
    meta: { requiresAuth: true, requiresAdmin: true }
  },
  {
    path: '/drivers',
    name: 'drivers',
    component: () => import('../views/DriversView.vue'),
    meta: { requiresAuth: true, requiresAdmin: true }
  },
  {
    path: '/scheduled',
    name: 'scheduled',
    component: () => import('../views/ScheduledView.vue'),
    meta: { requiresAuth: true }
  },
  {
    path: '/wallet',
    name: 'wallet',
    component: () => import('../views/WalletView.vue'),
    meta: { requiresAuth: true }
  }
]

const router = createRouter({
  history: createWebHashHistory(),
  routes
})

// 缓存 session 信息，避免每次路由切换都请求 API
let cachedSession = null
let sessionChecked = false

// 提供方法清除缓存（登出时调用）
export function clearSessionCache() {
  cachedSession = null
  sessionChecked = false
}

// 提供方法更新缓存（登录成功时调用）
export function updateSessionCache(session) {
  cachedSession = session
  sessionChecked = true
}

// 提供方法获取缓存的 session
export function getCachedSession() {
  return cachedSession
}

router.beforeEach(async (to, from, next) => {
  // 不需要认证的路由直接放行
  if (to.meta.requiresAuth === false) {
    next()
    return
  }

  // 检查 session
  if (!sessionChecked) {
    try {
      const resp = await fetch('/api/session', { credentials: 'include' })
      if (resp.ok) {
        cachedSession = await resp.json()
        sessionChecked = true
      } else {
        cachedSession = null
        sessionChecked = true
      }
    } catch (e) {
      cachedSession = null
      sessionChecked = true
    }
  }

  // 未登录，跳转到登录页
  if (!cachedSession) {
    if (to.path !== '/login') {
      next('/login')
    } else {
      next()
    }
    return
  }

  // 需要管理员权限但不是管理员
  if (to.meta.requiresAdmin && cachedSession.role !== 'admin') {
    next('/print')
    return
  }

  next()
})

export default router
