<template>
  <UApp>
    <div v-if="!sessionLoaded" class="flex items-center justify-center min-h-screen bg-default">
      <UIcon name="i-lucide-loader-circle" class="w-8 h-8 animate-spin text-primary" />
    </div>
    <div v-else class="grid grid-rows-[auto_1fr_auto] min-h-screen w-full bg-default">
      <header class="flex items-center justify-between px-4 sm:px-6 py-3 border-b border-default bg-default">
        <div class="flex items-center gap-3 min-w-0">
          <h1 class="text-xl font-bold shrink-0">CUPS 打印</h1>
          <!-- 用户名只在 sm+ 显示：手机上顶栏要留给两个导航按钮，
               而「我是谁」在个人中心里有更完整的展示。 -->
          <span v-if="session" class="hidden sm:inline text-sm text-muted truncate">{{ session.username }}</span>
        </div>
        <div class="flex items-center gap-2">
          <!-- 导航只有两项：在线打印（首页）与个人中心。
               手机和电脑共用一套 —— 顶栏放不下五个按钮，而那些入口在
               个人中心里都能找到；两套导航只会各自漂移。
               登出也收进了个人中心，顶栏不再重复提供。 -->
          <div
            v-if="session && !alipayNeedBrowserReturn"
            class="flex items-center gap-0.5 p-0.5 rounded-lg bg-elevated/60 border border-default"
          >
            <UButton
              :variant="route.path === '/print' ? 'soft' : 'ghost'"
              :color="route.path === '/print' ? 'primary' : 'neutral'"
              size="xs"
              icon="i-lucide-printer"
              @click="router.push('/print')"
            >
              在线打印
            </UButton>
            <UButton
              :variant="inProfileSection ? 'soft' : 'ghost'"
              :color="inProfileSection ? 'primary' : 'neutral'"
              size="xs"
              icon="i-lucide-user-round"
              @click="router.push('/me')"
            >
              个人中心
            </UButton>
          </div>
        </div>
      </header>
      <!-- overscroll-x-none：这个 div 才是真正的滚动容器，横向滑动会从这里
           链到浏览器、被当成「后退/前进」。index.css 里也设了 html/body，
           两处都加是因为链式过冲的判定点在【被滑动的那个容器】上。 -->
      <div class="overflow-auto relative overscroll-x-none">
        <!-- 支付宝里这一页是个【死胡同】：只显示下面这段提示，不渲染任何路由内容，
             顶栏的导航也一并藏起来（见上面那个 v-if）。理由：授权是在这里完成的，
             但活要在浏览器里干 —— 那边才翻得到微信/QQ 的文件、PDF 预览也正常。
             所以不能让他在这里点进个人中心之类的页面。

             ⚠️ 不要做"读两秒自动关页"。真机试过：页面一关，用户落在支付宝首页，
             提示也没看到，完全不知道发生了什么 —— 那比不关还糟。
             现在是他自己读完、自己按返回键。 -->
        <div v-if="alipayNeedBrowserReturn" class="p-6 max-w-md mx-auto text-center space-y-4 mt-8">
          <UIcon name="i-lucide-check-circle" class="w-12 h-12 mx-auto text-success" />
          <h2 class="text-lg font-bold">登录成功</h2>
          <p class="text-sm">
            浏览器里已经登录好了，正在等你回去。
          </p>
          <p class="text-sm">
            请按手机的「<b>返回</b>」键，直到回到浏览器 ——
            那里可以继续传文件、打印。
          </p>
          <p class="text-xs text-muted">
            在这一页里不用做别的，回到浏览器就能接着用。
          </p>
        </div>
        <router-view
          v-else
          :session="session"
          :alipay-enabled="alipayEnabled"
          @login-success="onLogin"
          @logout="logout"
          @go-wallet="router.push('/wallet')"
        />
      </div>
      <!-- 提示页要做成【死胡同】：页脚里那个外链也一并藏掉（它会开新窗口，
           而在支付宝的 webview 里开新窗口同样是个岔路）。 -->
      <footer
        v-if="!alipayNeedBrowserReturn"
        class="px-6 py-3 border-t border-default bg-default text-sm text-muted flex items-center justify-center gap-3 flex-wrap"
      >
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
import { apiFetch, readError, readJSON, setUnauthorizedHandler } from './utils/api'
import { announcePaymentSettled } from './utils/paymentSignal'
import { clearPendingUpload } from './utils/pendingUpload'

const router = useRouter()
const route = useRoute()
const toast = useToast()

const session = ref(null)
const sessionLoaded = ref(false)
// 二进制版本号：首次挂载时拉一次 /api/version（公开接口，不要求登录），
// 失败时保持空字符串，footer 上的版本号节点会被 v-if 隐藏，不影响布局。
const appVersion = ref('')

// 支付宝授权登录是否可用（来自公开的 /api/public-settings，服务端凭据齐全才为真）。
// 登录页据此决定要不要显示那个按钮 —— 没配置却显示一个点了必然失败的按钮，
// 比不显示更糟；反过来它也是个不用改代码、不用重新部署前端的降级开关。
const alipayEnabled = ref(false)

// 为真时页面被"请回浏览器"的提示接管。用在这么一种情形：
// 顾客先在普通浏览器里点了「支付宝登录」，授权是在支付宝的 webview 里完成的 ——
// 那个 webview 不该接着往下用（它的文件选择器翻不到微信/QQ 的文件，
// PDF 预览也起不来），会话要交回浏览器。浏览器那边在轮询，马上就能接上。
const alipayNeedBrowserReturn = ref(false)

// 个人中心名下的页面。顶栏只有「在线打印 / 个人中心」两项，钱包、定时、
// 管理、驱动都是个人中心的二级入口 —— 停在这些页面上时高亮「个人中心」，
// 否则顶栏看起来像什么都没选中，用户会以为导航坏了。
const PROFILE_PATHS = ['/me', '/records', '/wallet', '/scheduled', '/admin', '/drivers']
// 前缀匹配，不是精确匹配 —— 子页（如 /admin/users）也要让「个人中心」保持高亮。
// 用精确匹配就得把每个子路径都塞进数组，而漏加一条的表现是「顶栏看起来像
// 导航坏了」，属于查起来很费劲的静默漂移。这里一次性解决。
const inProfileSection = computed(() =>
  PROFILE_PATHS.some((p) => route.path === p || route.path.startsWith(p + '/'))
)

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
    if (data && typeof data.alipayLogin === 'boolean') {
      alipayEnabled.value = data.alipayLogin
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
      // ⚠️ 必须先等路由的【首次导航落定】再读 route.path。
      //
      // 路由守卫里有一次异步的 session 请求，所以首次导航在 onMounted 跑
      // 到这里时还没解析完 —— 此刻 route.path 仍是初始的 '/'。直接判断必然
      // 命中下面这个 '/' 分支，把用户真正想去的那一页冲掉：
      // 实测刷新 /#/records、/#/wallet 都会被送到 /print 的落地页。
      //
      // （这条注释原先写的是「支付回跳会被冲掉」—— 那只是同一个竞态的一个
      // 后果，根因是没等 isReady。冒烟测试跑出来的：构建和静态检查全绿，
      // 只有真加载页面才看得见。）
      await router.isReady()

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
  // 暂存里是用户上传的【原始文档】，登出时必须清掉 —— 否则下一个人
  // 用这台手机登录进来，上一个人的文件还在。
  clearPendingUpload()
  router.push('/login')
}

// 会话失效的全局兜底：任何 apiFetch 收到 401 都会落到这里。
//
// 用 onLogout（只清本地）而不是 logout（还会 POST /api/logout）：
// 401 本身就说明会话已经失效，再发一次登出请求没意义，
// 而且从 handlePaymentReturn 里绕回来还会形成环。
setUnauthorizedHandler(onLogout)

// ⚠️ 视图 emit('logout') 必须接到这里，不能接 onLogout。
//
// onLogout 只清内存里的 session 并跳登录页 —— 服务端的会话 cookie 原封不动，
// 刷新一下 /api/session 又会承认它，用户「登出」完又自己登回来了。
// 只有 logout 会真的打 /api/logout 把 cookie 清掉。
//
// 早先只有顶栏那个登出按钮走 logout，视图 emit 仅用于 401 自动登出
// （那种情况会话本来就死了，不调接口也看不出来），所以这个坑一直没露头。
// 登出按钮移进个人中心后就撞上了。对 401 来说多调一次也无害：接口幂等。
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
  // 打印支付的落点。默认退回打印页；拿到批次号就去中间页收尾。
  let printTarget = { path: '/print' }

  try {
    const resp = await apiFetch(`/api/payments/${outTradeNo}/sync`, { method: 'POST' })
    if (resp.ok) {
      const data = await readJSON(resp)
      topup = data?.purpose === 'topup'
      // ⚠️ 打印支付走【中间页】而不是直接落回「在线打印」。
      // 落地页只有打印机和四个模式按钮 —— 用户付完款回来看不到刚才打的是
      // 什么，也没有「这就算完了」的收尾感，只能自己再点进个人中心找。
      // 批次号由查单接口带回来（页面重新加载过，内存里没有它）。
      if (!topup && data?.batchId) {
        printTarget = { path: '/done', query: { batch: data.batchId } }
      }
      if (data?.paid) {
        // 广播到账。此刻目的页【可能已经挂载完了】（App 的 onMounted 晚于
        // 子组件），它那次 load() 拉到的还是旧余额，靠这个信号重新拉一次。
        announcePaymentSettled()
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
    } else {
      // ⚠️ 不能只有 if (resp.ok)。401 / 500 / 反代返回 HTML 错误页都会走到
      // 这里，而用户是【刚付完款】回来的 —— 不给任何反馈等于告诉他
      // 「付了钱，但页面什么都没发生」，而且 URL 上那个单号已经被上面
      // replaceState 抹掉了，连手工排查的线索都不剩。
      //
      // 把单号写进提示里：用户截图给管理员，就能直接定位到哪一笔。
      toast.add({
        title: '支付结果查询失败',
        description: `款项可能已经到账。请稍后到「钱包」查看；若余额未变，请联系管理员并提供单号 ${outTradeNo}。`,
        color: 'warning',
        icon: 'i-lucide-alert-triangle'
      })
    }
  } catch (e) {
    // 网络抖动不该在用户刚付完款的时候弹红字吓他 —— 后台巡检每 30 秒
    // 会兜底把没结算的单子查回来。
  }

  // ⚠️ 无论上面走哪条分支都必须落到一个真实页面。
  // 少了这一句、又恰好没读出用途时，用户会停在根路径上 ——
  // 而根路径重定向到登录页，他会以为自己被登出了。
  router.replace(topup ? '/wallet' : printTarget)
}

// ─── 支付宝授权登录回跳 ────────────────────────────────────────────────

// 读回跳带回的 auth_code / state。
//
// 与支付回跳同一套双读法：先读 hash 内，再退回 hash 外 —— 支付宝不保证保留
// fragment，两种落点都出现过（见 readPayReturnOutTradeNo 上那段注释）。
function readAlipayAuthReturn() {
  const fromHash = new URLSearchParams(window.location.hash.split('?')[1] || '')
  const fromSearch = new URLSearchParams(window.location.search)
  return {
    code: fromHash.get('auth_code') || fromSearch.get('auth_code') || '',
    state: fromHash.get('state') || fromSearch.get('state') || ''
  }
}

// 在 setup 阶段【同步】读一次，理由与 payReturnOutTradeNo 完全相同：
// 未登录时路由守卫会把人送到 /login，那一步会把 hash 上的查询参数冲掉，
// 晚一步读就什么都拿不到了。
const alipayReturn = readAlipayAuthReturn()

// 拿 auth_code 去换会话。返回是否成功 —— 落哪一页交给随后的 loadSession 决定。
async function handleAlipayAuthReturn() {
  if (!alipayReturn.code) return false

  // 先清地址栏。auth_code 是【一次性的】，留在上面只会让刷新时拿一个废码
  // 再换一次，换回一个"授权码已失效"的红字 —— 用户完全看不懂。
  // 用 replaceState 而不是 router.replace：参数可能落在 hash 外面。
  window.history.replaceState(null, '', window.location.pathname + window.location.hash.split('?')[0])

  try {
    const resp = await apiFetch('/api/auth/alipay/exchange', {
      method: 'POST',
      body: JSON.stringify({ code: alipayReturn.code, state: alipayReturn.state })
    })
    if (!resp.ok) {
      // 服务端给的是指向根因的中文（授权码过期 / 应用不匹配 / openid 配置没启用…），
      // 原样透出，不要吞成一句"登录失败"。
      toast.add({
        title: '支付宝登录失败',
        description: await readError(resp),
        color: 'error',
        icon: 'i-lucide-x-circle'
      })
      return false
    }
    const data = await readJSON(resp)
    if (data && data.needBrowserReturn) {
      // 只把这一页变成"死胡同"，【不】自动关页 ——
      // 关页会让用户落在支付宝首页、提示也看不到。让他自己读完再按返回。
      alipayNeedBrowserReturn.value = true
    }
    return true
  } catch (e) {
    toast.add({
      title: '支付宝登录失败',
      description: `无法连接到服务端：${e.message}`,
      color: 'error',
      icon: 'i-lucide-x-circle'
    })
    return false
  }
}

onMounted(async () => {
  detectOS()
  // 先处理回跳再加载会话：回跳的查询参数要趁早读到手，
  // 后面任何一次路由跳转都可能把它冲掉。
  handlePaymentReturn()
  // 支付宝回跳也一样，而且必须【等它换完会话】再 loadSession ——
  // 否则那一次 loadSession 看到的是"还没登录"，会先把人推到 /login，
  // 紧接着兑换成功又跳一次，界面闪两下。
  await handleAlipayAuthReturn()
  loadSession()
  loadVersion()
  loadCustomCSS()
})
</script>
