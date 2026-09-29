<template>
  <div class="flex items-center justify-center h-full p-3 sm:p-4 md:p-6">
    <UCard class="w-full max-w-md shadow-lg">
      <template #header>
        <h2 class="text-xl font-bold flex items-center gap-2">
          <UIcon name="i-lucide-user" class="w-5 h-5" />
          登录
        </h2>
      </template>
      
      <!-- 错误提示 -->
      <UAlert
        v-if="error"
        icon="i-lucide-triangle-alert"
        color="error"
        variant="soft"
        :title="error"
        class="mb-6"
      />
      
      <UForm @submit="login" :state="state" class="space-y-6">
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
    </UCard>
  </div>
</template>

<script setup>
import { reactive, ref } from 'vue'

const state = reactive({
  username: '',
  password: ''
})
const error = ref('')
const loading = ref(false)

const emit = defineEmits(['login-success'])

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
