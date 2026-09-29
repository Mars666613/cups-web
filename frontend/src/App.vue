<template>
  <UApp>
    <div v-if="!sessionLoaded" class="flex items-center justify-center min-h-screen bg-default">
      <UIcon name="i-lucide-loader-circle" class="w-8 h-8 animate-spin text-primary" />
    </div>
    <div v-else class="grid grid-rows-[auto_1fr_auto] min-h-screen w-full bg-default">
      <header class="flex items-center justify-between px-4 sm:px-6 py-3 border-b border-default bg-default">
        <div class="flex items-center gap-3 min-w-0">
          <h1 class="text-xl font-bold shrink-0">CUPS 打印</h1>
          <span v-if="session" class="text-sm text-muted truncate">{{ session.username }}</span>
        </div>
        <div class="flex items-center gap-2">
          <!-- 桌面端（sm+）：分段按钮 + 文字，一目了然 -->
          <div class="hidden sm:flex items-center gap-2">
            <!-- 导航分段容器：与主 CTA 视觉区分。
                 「打印 / 定时」对所有登录用户开放；「管理 / 驱动」仅管理员可见。 -->
            <div
              v-if="session"
              class="flex items-center gap-0.5 p-0.5 rounded-lg bg-elevated/60 border border-default"
            >
              <UButton
                :variant="route.path === '/print' ? 'soft' : 'ghost'"
                :color="route.path === '/print' ? 'primary' : 'neutral'"
                size="xs"
                icon="i-lucide-file-text"
                @click="router.push('/print')"
              >
                打印
              </UButton>
              <UButton
                :variant="route.path === '/scheduled' ? 'soft' : 'ghost'"
                :color="route.path === '/scheduled' ? 'primary' : 'neutral'"
                size="xs"
                icon="i-lucide-calendar-clock"
                @click="router.push('/scheduled')"
              >
                定时
              </UButton>
              <UButton
                :variant="route.path === '/wallet' ? 'soft' : 'ghost'"
                :color="route.path === '/wallet' ? 'primary' : 'neutral'"
                size="xs"
                icon="i-lucide-wallet"
                @click="router.push('/wallet')"
              >
                钱包
              </UButton>
              <UButton
                v-if="isAdmin"
                :variant="route.path === '/admin' ? 'soft' : 'ghost'"
                :color="route.path === '/admin' ? 'primary' : 'neutral'"
                size="xs"
                icon="i-lucide-settings"
                @click="router.push('/admin')"
              >
                管理
              </UButton>
              <UButton
                v-if="isAdmin"
                :variant="route.path === '/drivers' ? 'soft' : 'ghost'"
                :color="route.path === '/drivers' ? 'primary' : 'neutral'"
                size="xs"
                icon="i-lucide-puzzle"
                @click="router.push('/drivers')"
              >
                驱动
              </UButton>
            </div>
            <UButton
              v-if="session"
              variant="ghost"
              color="neutral"
              size="xs"
              icon="i-lucide-log-out"
              @click="logout"
            >
              登出
            </UButton>
          </div>
          <!-- 移动端（<sm）：折叠为汉堡菜单，图标+文字，易点易读 -->
          <UDropdownMenu
            v-if="session"
            :items="menuItems"
            :content="{ align: 'end' }"
            class="sm:hidden"
          >
            <UButton variant="ghost" color="neutral" size="sm" icon="i-lucide-menu" square />
          </UDropdownMenu>
        </div>
      </header>
      <div class="overflow-auto relative">
        <router-view
          :session="session"
          @login-success="onLogin"
          @logout="onLogout"
          @go-wallet="router.push('/wallet')"
        />
      </div>
      <footer class="px-6 py-3 border-t border-default bg-default text-sm text-muted flex items-center justify-center gap-3 flex-wrap">
        <span>
          Powered by
          <a href="https://github.com/hanxi/cups-web" target="_blank" class="text-primary hover:underline">cups-web</a>
        </span>
        <span v-if="appVersion" class="text-default/40">·</span>
        <!-- 版本号：二进制构建期由 -ldflags 注入到 main.Version，经 /api/version 返回。
             用户二进制覆盖升级后，无需登录即可在 footer 上看到当前运行的版本（Issue #26）。 -->
        <span v-if="appVersion" class="font-mono text-xs" :title="`cups-web ${appVersion}`">
          {{ appVersion }}
        </span>
      </footer>
    </div>
  </UApp>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { clearSessionCache, updateSessionCache } from './router'
import { apiFetch, readJSON } from './utils/api'

const router = useRouter()
const route = useRoute()
const toast = useToast()

const session = ref(null)
const sessionLoaded = ref(false)
// 二进制版本号：首次挂载时拉一次 /api/version（公开接口，不要求登录），
// 失败时保持空字符串，footer 上的版本号节点会被 v-if 隐藏，不影响布局。
const appVersion = ref('')

const isAdmin = computed(() => session.value?.role === 'admin')

// 移动端汉堡菜单项：导航项（仅 admin）与登出分成两组，组间自动加分隔线
const menuItems = computed(() => {
  const nav = []
  if (session.value) {
    nav.push({ label: '打印', icon: 'i-lucide-file-text', onSelect: () => router.push('/print') })
    nav.push({ label: '定时', icon: 'i-lucide-calendar-clock', onSelect: () => router.push('/scheduled') })
    nav.push({ label: '钱包', icon: 'i-lucide-wallet', onSelect: () => router.push('/wallet') })
  }
  if (isAdmin.value) {
    nav.push({ label: '管理', icon: 'i-lucide-settings', onSelect: () => router.push('/admin') })
    nav.push({ label: '驱动', icon: 'i-lucide-puzzle', onSelect: () => router.push('/drivers') })
  }
  const account = [{ label: '登出', icon: 'i-lucide-log-out', onSelect: () => logout() }]
  return nav.length ? [nav, account] : [account]
})

async function loadVersion() {
  try {
    const resp = await fetch('/api/version', { credentials: 'include' })
    if (resp.ok) {
      const data = await resp.json()
      if (data && typeof data.version === 'string') {
        appVersion.value = data.version
      }
    }
  } catch (e) {
    // 版本号展示属于可降级的信息，静默失败即可
  }
}

// 自定义 CSS 注入（Issue #57）：管理员在设置里贴一段 CSS，登录前也应生效
// （登录页也是应用的一部分），因此走公开接口。用 <style id="app-custom-css">
// 作为幂等的注入点，保存后可以随时刷新样式而不用 reload 整页。
async function loadCustomCSS() {
  try {
    const resp = await fetch('/api/public-settings')
    if (!resp.ok) return
    const data = await resp.json()
    if (data && typeof data.customCss === 'string') {
      applyCustomCSS(data.customCss)
    }
  } catch (e) {
    // 自定义样式属于装饰性功能，接口挂了不该阻塞主界面
  }
}

function applyCustomCSS(css) {
  let el = document.getElementById('app-custom-css')
  if (!el) {
    el = document.createElement('style')
    el.id = 'app-custom-css'
    document.head.appendChild(el)
  }
  el.textContent = css || ''
}

async function loadSession() {
  try {
    const resp = await fetch('/api/session', { credentials: 'include' })
    if (resp.ok) {
      const data = await resp.json()
      session.value = data
      updateSessionCache(data)
      // 🚫 别在这里无条件 push('/print')。
      //
      // 那等于「任何页面刷新都会被弹回打印页」。平时只是烦人，但支付回跳时
      // 是致命的：一次 push 就把单号连同落点一起冲掉，用户付完回来看到的
      // 是一张什么都没发生的打印页，而钱已经扣了。
      //
      // 有回跳单号时一律不抢方向盘 —— 交给 handlePaymentReturn 按用途决定
      // 该停在哪一页。
      if (!payReturnOutTradeNo && (route.path === '/' || route.path === '/login')) {
        router.push('/print')
      }
    } else {
      session.value = null
      router.push('/login')
    }
  } catch (e) {
    session.value = null
  } finally {
    sessionLoaded.value = true
  }
}

function onLogin() {
  loadSession()
}

function onLogout() {
  session.value = null
  clearSessionCache()
  router.push('/login')
}

async function logout() {
  try {
    await fetch('/api/logout', { method: 'POST', credentials: 'include' })
  } catch (e) {
    // ignore errors
  }
  onLogout()
}

function detectOS() {
  if (navigator.userAgent.indexOf('Windows') !== -1) {
    document.documentElement.classList.add('is-windows')
  }
}

// ── 从支付宝回来 ──────────────────────────────────────────────────
//
// 手机网站支付付完，支付宝会把浏览器送回我们配的 return_url。
//
// ⚠️ 这一刻页面是【刚刚重新加载】的：弹窗、批次、轮询全没了，内存里一点
// 线索都没有 —— URL 上那个 out_trade_no 是唯一能凭的东西。
//
// ⚠️ 而它可能落在两个地方，实测两种都出现过：
//
//	① http://host/#/wallet?out_trade_no=CW...   ← 我们拼进 hash 的理想形态
//	② http://host/?out_trade_no=CW...           ← hash 被渠道丢掉后的形态
//
// ② 是真会发生的：支付宝不保证保留 return_url 里的 fragment。只认 ① 的
// 后果很具体 —— 页面落在根路径、路由重定向到登录页、loadSession 顺手
// 把人送到 /print，用户看到的是「打印首页」，完全不知道钱去哪了。
// 所以两边都读。
function readPayReturnOutTradeNo() {
  const fromHash = new URLSearchParams(window.location.hash.split('?')[1] || '')
  const no = fromHash.get('out_trade_no')
  if (no) return no
  return new URLSearchParams(window.location.search).get('out_trade_no') || ''
}

// 在 setup 阶段【同步】读一次。
//
// 不能等 onMounted 再读：loadSession 也挂在 onMounted 上，它要靠这个值
// 决定「要不要替用户选页面」。晚一步读就会被路由跳转冲掉。
const payReturnOutTradeNo = readPayReturnOutTradeNo()

// 所以在这儿主动补一次查单。查到就是钱已到账（服务端会结算、订单开始打印），
// 查不到也不报错：用户可能只是按了返回，或者渠道还没同步完。
//
// 🚫 这一段不能省。少了它，用户付完回来看到的是一个毫无反应的页面，
//    而钱已经扣了 —— 那正是改用官方方案要解决的问题本身。
async function handlePaymentReturn() {
  const outTradeNo = payReturnOutTradeNo
  if (!outTradeNo) return

  // 先把地址栏清干净再查。单号留在上面的话，每次刷新都会再查一遍。
  // 用 replaceState 而不是 router.replace：参数可能落在 hash 外面，
  // 那是路由管不到的地方。
  window.history.replaceState(null, '', window.location.pathname + window.location.hash.split('?')[0])

  // 落点按这笔支付的【用途】决定，不按 URL 里那个 path ——
  // 渠道会把 fragment 丢掉，path 本来就不可靠；而用途是从库里读出来的，
  // 它一定对。查不到用途时退到钱包页（钱的事都记在那儿）。
  let topup = true

  try {
    const resp = await apiFetch(`/api/payments/${outTradeNo}/sync`, { method: 'POST' })
    if (resp.ok) {
      const data = await readJSON(resp)
      topup = data?.purpose === 'topup'
      if (data?.paid) {
        toast.add({
          title: topup ? '充值成功' : '支付成功',
          description: topup ? '余额已到账。' : '正在开始打印，进度可在「钱包」里查看。',
          color: 'success',
          icon: 'i-lucide-check-circle'
        })
      } else {
        toast.add({
          title: '还没收到支付结果',
          description: '如果已经付款，稍等片刻会自动到账；也可以到「钱包」里查看。',
          color: 'warning',
          icon: 'i-lucide-clock'
        })
      }
    }
  } catch (e) {
    // 网络抖动不该在用户刚付完款的时候弹红字吓他 —— 后台巡检每 30 秒
    // 会兜底把没结算的单子查回来。
  }

  // ⚠️ 无论上面走哪条分支都必须落到一个真实页面。
  // 少了这一句、又恰好没读出用途时，用户会停在根路径上 ——
  // 而根路径重定向到登录页，他会以为自己被登出了。
  router.replace(topup ? '/wallet' : '/print')
}

onMounted(() => {
  detectOS()
  // 先处理回跳再加载会话：回跳的查询参数要趁早读到手，
  // 后面任何一次路由跳转都可能把它冲掉。
  handlePaymentReturn()
  loadSession()
  loadVersion()
  loadCustomCSS()
})
</script>
