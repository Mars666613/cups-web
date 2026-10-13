<template>
  <div class="flex items-center justify-center min-h-full p-3 sm:p-4 md:p-6">
    <UCard class="w-full max-w-md shadow-lg">
      <template #header>
        <h2 class="text-xl font-bold flex items-center gap-2">
          <UIcon name="i-lucide-user" class="w-5 h-5" />
          登录
        </h2>
      </template>

      <UAlert
        v-if="error"
        icon="i-lucide-triangle-alert"
        color="error"
        variant="soft"
        :title="error"
        class="mb-6"
      />

      <!-- 支付宝为主：扫码进来的顾客看到的就这一个按钮。
           密码表单折在下面，只给管理员用。 -->
      <div v-if="alipayEnabled">
        <UButton
          color="primary"
          size="lg"
          block
          icon="i-lucide-smartphone"
          :loading="alipayLoading"
          @click="startAlipayLogin"
        >支付宝登录</UButton>
        <!-- ⚠️ 端外那句【不能】写"之后自动回到这里"。实测过：授权完成后支付宝不会
             自动退回浏览器（Android 的返回栈 + 支付宝的设计限制，三种 API 都试过）。
             写上做不到的承诺，用户会一直等一个不会发生的事。 -->
        <p class="text-xs text-muted mt-2 text-center">
          {{ inAlipay
            ? '将跳转到支付宝完成授权，无需输入任何信息'
            : '会打开支付宝，授权完成后按「返回」回到浏览器继续' }}
        </p>
      </div>

      <!-- 微信/QQ 收到的文件存在它们自己的沙箱里，系统的文件选择器看不到 ——
           这是操作系统的隔离，我们绕不过去，只能把"先存到手机"这一步讲清楚。
           （跨生态的边界：微信的文件只有微信生态拿得到，而支付宝付款只有支付宝能用，
             两者不允许交叉，所以中间的搬运只能由顾客自己做。） -->
      <div class="mt-5">
        <button
          type="button"
          class="w-full flex items-center gap-2 text-left text-sm text-muted hover:text-default transition cursor-pointer"
          @click="showHelp = !showHelp"
        >
          <UIcon name="i-lucide-file-question" class="w-4 h-4 shrink-0" />
          <span class="flex-1">要打印的文件在微信 / QQ 里？</span>
          <UIcon
            name="i-lucide-chevron-down"
            class="w-4 h-4 shrink-0 transition-transform"
            :class="showHelp ? 'rotate-180' : ''"
          />
        </button>
        <div class="overflow-hidden transition-all duration-200" :style="{ maxHeight: showHelp ? '32rem' : '0' }">
          <div class="pt-3 space-y-3 text-sm text-muted">
            <!-- 先让用户直接试一次。有些浏览器（比如手机自带的）在选择文件时会直接
                 给出「微信 / QQ」的入口，点进去就能选到别人发来的文件，不用存。
                 ⚠️ 这句【不能】写成"手机自带浏览器都能选到"：第三方浏览器未必，
                 iPhone 上没验证过。所以只说"看一眼有没有"，不替浏览器打包票。 -->
            <div>
              <div class="font-medium text-default">先直接试一次</div>
              <div>走到选文件那一步时，看一下有没有「微信 / QQ」这类入口 ——
                有就直接点进去选，不用存。</div>
            </div>
            <div>
              <div class="font-medium text-default">找不到入口就先存到手机</div>
              <div class="mt-1 space-y-1">
                <div>· <b>安卓</b>：在微信里点开文件 → 右下角「…」→ 保存到手机</div>
                <div>· <b>iPhone</b>：点开文件 → 右上角「…」→ 用其他应用打开 → 存储到「文件」</div>
                <div>· <b>QQ</b> 同理，找「保存到手机」或「用其他应用打开」</div>
              </div>
            </div>
            <p class="text-xs">
              存好之后，选文件时从「文件管理 / 最近文件」里就能看到它。
            </p>
          </div>
        </div>
      </div>

      <!-- 管理员入口：默认收起，顾客不会看到一个用不上的表单 -->
      <div class="mt-6 pt-4 border-t border-default">
        <button
          type="button"
          class="w-full flex items-center gap-2 text-left text-sm text-muted hover:text-default transition cursor-pointer"
          @click="showPassword = !showPassword"
        >
          <UIcon name="i-lucide-key-round" class="w-4 h-4 shrink-0" />
          <span class="flex-1">已有账号？管理员登录</span>
          <UIcon
            name="i-lucide-chevron-down"
            class="w-4 h-4 shrink-0 transition-transform"
            :class="showPassword ? 'rotate-180' : ''"
          />
        </button>
        <div class="overflow-hidden transition-all duration-200" :style="{ maxHeight: showPassword ? '32rem' : '0' }">
          <UForm @submit="login" :state="state" class="space-y-6 pt-5">
            <UFormField label="用户名" name="username" required>
              <UInput v-model="state.username" icon="i-lucide-user" size="lg" class="w-full" />
            </UFormField>
            <UFormField label="密码" name="password" required>
              <UInput v-model="state.password" type="password" icon="i-lucide-lock" size="lg" class="w-full" />
            </UFormField>

            <div class="mt-6">
              <UButton
                type="submit"
                color="primary"
                icon="i-lucide-log-in"
                size="lg"
                class="w-full"
                :loading="loading"
              >
                登录
              </UButton>
            </div>
          </UForm>
        </div>
      </div>
    </UCard>
  </div>
</template>

<script setup>
import { reactive, ref } from 'vue'
import { beginAlipayLogin, isInAlipay } from '../utils/alipayAuth'

// alipayEnabled 由 public-settings 决定（凭据齐全才为真）——
// 没配置时显示一个点了必然失败的按钮，比不显示更糟。
defineProps({ alipayEnabled: Boolean })

const state = reactive({
  username: '',
  password: ''
})
const error = ref('')
const loading = ref(false)

const alipayLoading = ref(false)
const showPassword = ref(false)
const showHelp = ref(false)

// 只在初始化时判一次：这个进程里 UA 不会变。
const inAlipay = isInAlipay()

const emit = defineEmits(['login-success'])

// startAlipayLogin 保持【无参】——模板里 @click 会把 MouseEvent 传进来，
// 带可选参数的签名会被事件对象顶成真值（AGENTS.md 记过两次的坑）。
//
// 离页与轮询的细节都在 utils/alipayAuth.js，这里只负责"发起 + 处理结果"。
async function startAlipayLogin() {
  error.value = ''
  alipayLoading.value = true
  try {
    // 成功时，会话已经签在【当前这个浏览器】上了（见 alipayAuth.js 里的说明：
    // 授权在支付宝里做，会话交回浏览器）。
    await beginAlipayLogin()
    emit('login-success')
  } catch (e) {
    error.value = e.message || String(e)
  } finally {
    alipayLoading.value = false
  }
}

// 后端 /api/login 的错误串是英文短语，直接展示对终端用户不友好，在此本地化。
const BACKEND_MESSAGES = {
  'invalid credentials': '用户名或密码错误',
  'missing credentials': '请输入用户名和密码',
  'too many attempts, please try again later': '登录失败次数过多，请稍后再试（默认锁定 15 分钟）',
  'login failed': '服务端处理登录时出错，请查看服务端日志',
  'session error': '会话创建失败，请查看服务端日志'
}

// 把失败响应翻译成可诊断的中文提示。
//
// 关键约定：响应体不是后端 JSON 时，绝不能兜底显示「用户名或密码错误」。这类失败说明
// 请求根本没走到 LoginHandler（被跨源防护、反向代理或网关拦掉），谎报成凭据问题会把
// 排查方向彻底带偏 —— 这正是 issue #99 里「内网能登录，反代后显示密码错误」的由来。
async function describeFailure(resp) {
  // body 只能读一次，先取文本再自行尝试解析。
  let raw = ''
  try {
    raw = await resp.text()
  } catch {
    raw = ''
  }

  let payload = null
  try {
    payload = JSON.parse(raw)
  } catch {
    payload = null
  }

  if (payload && (payload.error || payload.message)) {
    const msg = payload.error || payload.message
    return BACKEND_MESSAGES[msg] || msg
  }

  const looksLikeHTML = raw.trimStart().startsWith('<')
  const detail = looksLikeHTML
    ? '（服务端返回了 HTML 页面，通常来自反向代理或网关的错误页）'
    : raw.trim().slice(0, 200)

  if (resp.status === 403) {
    return `请求被拒绝（403）。若你通过反向代理访问，请确认它转发了真实域名（nginx：proxy_set_header Host $host; proxy_set_header X-Forwarded-Host $host;）。${detail}`
  }
  if (resp.status === 404 || resp.status === 405) {
    return `接口不可达（${resp.status}）：/api/login 没有到达 cups-web，请检查反向代理是否把 /api 转发到了正确的后端。${detail}`
  }
  if (resp.status >= 500) {
    return `服务端错误（${resp.status}），请查看服务端日志。${detail}`
  }
  return `登录失败（HTTP ${resp.status}）${detail ? '：' + detail : ''}`
}

async function login() {
  error.value = ''
  loading.value = true
  try {
    const resp = await fetch('/api/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: state.username, password: state.password }),
      credentials: 'include'
    })
    if (!resp.ok) {
      error.value = await describeFailure(resp)
      return
    }
    emit('login-success')
  } catch (e) {
    // fetch 本身抛异常＝请求没能完成（网络不可达、TLS 失败、被浏览器策略阻断）。
    error.value = `无法连接到服务端：${e.message}`
  } finally {
    loading.value = false
  }
}
</script>
