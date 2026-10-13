<template>
  <div class="p-3 sm:p-4 md:p-6 max-w-7xl mx-auto">
    <!-- ── 落地页：打印机 + 四个模式 ─────────────────────────────────
         进入「在线打印」先看到这一屏。手机屏小，一上来就把上传区、十几个
         参数、预览、打印记录全堆出来，用户根本不知道该从哪儿下手 ——
         先选「要打什么」，后面的界面才有意义。 -->
    <div v-if="!entered" class="max-w-2xl mx-auto pt-2 sm:pt-6">
      <h1 class="text-xl font-bold flex items-center gap-2 mb-4">
        <UIcon name="i-lucide-printer" class="w-5 h-5 text-primary" />
        在线打印
      </h1>

      <USelect
        :model-value="printer"
        :items="printerItems"
        value-key="value"
        label-key="label"
        placeholder="选择打印机"
        icon="i-lucide-printer"
        size="xl"
        class="w-full"
        @update:model-value="onPrinterSelect"
      />

      <!-- 取件模式下没有一台打印机配了终端 —— 下拉会是空的，不给一句话
           用户只会以为是页面坏了（而真实原因是管理员还没配终端）。 -->
      <UAlert
        v-if="pull.enabled && printers.length > 0 && printerItems.length === 0"
        class="mt-3"
        color="warning"
        variant="soft"
        icon="i-lucide-triangle-alert"
        title="暂无可取件的打印机"
        description="当前没有任何打印机配好取件终端，请联系管理员。"
      />

      <!-- 连接状态：紧贴打印机名字，让用户在点模式之前就知道这台能不能打 -->
      <div class="flex items-center gap-1.5 mt-2 mb-5" :title="deviceState.title">
        <span class="w-2 h-2 rounded-full shrink-0" :class="DEVICE_TONE_CLASS[deviceState.tone]" />
        <span class="text-xs" :class="DEVICE_TEXT_CLASS[deviceState.tone]">{{ deviceState.text }}</span>
      </div>

      <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
        <button
          v-for="m in printModeItems"
          :key="m.value"
          type="button"
          class="flex items-center gap-3 p-4 rounded-xl border border-default bg-elevated/40 hover:border-primary hover:bg-primary/5 transition text-left cursor-pointer"
          @click="enterMode(m.value)"
        >
          <div class="w-11 h-11 rounded-lg bg-primary/10 flex items-center justify-center shrink-0">
            <UIcon :name="m.icon" class="w-5 h-5 text-primary" />
          </div>
          <div class="min-w-0">
            <div class="font-medium">{{ m.label }}</div>
            <div class="text-xs text-muted truncate">{{ m.hint }}</div>
          </div>
        </button>
      </div>
    </div>

    <!-- 顶部标题栏：桌面端与主体栅格对齐（2 + 3），移动端单行 flex -->
    <div v-if="entered" class="mb-3 grid grid-cols-1 lg:grid-cols-5 gap-x-4 gap-y-2">
      <!-- 左：返回 + 当前模式 + 打印机下拉（桌面 col-span-2，与左栏对齐）。
           排成一列两行：上面操作行、下面连接状态。状态若塞进操作行会被
           flex-1 的下拉挤到边上，换行位置还随屏幕宽度飘。 -->
      <div class="lg:col-span-2 min-w-0">
        <div class="flex items-center gap-2 sm:gap-3 min-w-0">
          <!-- 标题即返回：点它回落地页换一种打印方式。
               用一个按钮而不是「←」+ 标题两个元素 —— 手机上顶栏还要塞
               打印机下拉和图标按钮，多一个元素就挤不下了。 -->
          <button
            type="button"
            class="shrink-0 flex items-center gap-1 text-base sm:text-lg font-bold cursor-pointer"
            title="返回选择打印方式"
            @click="backToLanding"
          >
            <UIcon name="i-lucide-chevron-left" class="w-5 h-5 text-muted shrink-0" />
            {{ currentMode.label }}
          </button>
          <USelect
            :model-value="printer"
            :items="printerItems"
            value-key="value"
            label-key="label"
            placeholder="选择打印机"
            icon="i-lucide-printer"
            class="flex-1 min-w-0"
            @update:model-value="onPrinterSelect"
          />
          <!-- CUPS 任务是运维视角，只给管理员。普通用户看到队列里排着别人的
               任务，只会以为要自己去处理。 -->
          <UButton
            v-if="isAdmin"
            variant="ghost"
            size="xs"
            icon="i-lucide-list-todo"
            class="shrink-0 lg:hidden"
            @click="openJobsModal"
            title="CUPS 任务"
          />
          <UButton
            variant="ghost"
            size="xs"
            icon="i-lucide-refresh-cw"
            class="shrink-0 lg:hidden"
            @click="refreshAll"
            :loading="refreshing"
          />
        </div>
        <!-- 连接状态：普通用户看不到整块打印机状态卡片，但必须能判断
             「这台连上了没有」—— 否则点完打印才发现打不出来。 -->
        <div class="flex items-center gap-1.5 mt-1.5" :title="deviceState.title">
          <span class="w-2 h-2 rounded-full shrink-0" :class="DEVICE_TONE_CLASS[deviceState.tone]" />
          <span class="text-xs" :class="DEVICE_TEXT_CLASS[deviceState.tone]">{{ deviceState.text }}</span>
        </div>
      </div>
      <!-- 右：任务列表 + 刷新按钮（桌面 col-span-3，靠右，与右栏对齐；移动端隐藏） -->
      <div class="hidden lg:flex lg:col-span-3 items-center justify-end gap-2">
        <UButton
          v-if="isAdmin"
          variant="ghost"
          size="xs"
          icon="i-lucide-list-todo"
          @click="openJobsModal"
        >CUPS 任务</UButton>
        <UButton
          variant="ghost"
          size="xs"
          icon="i-lucide-refresh-cw"
          @click="refreshAll"
          :loading="refreshing"
        >刷新</UButton>
      </div>
    </div>

    <!-- 检测到页面被浏览器重载时说明一句。
         不说的话，用户只会觉得「怎么又回到起点了」—— 而真相是浏览器回收了
         标签页，跟他操作对不对没关系。 -->
    <UAlert
      v-if="reloadNotice"
      color="warning"
      variant="soft"
      icon="i-lucide-refresh-cw"
      class="mb-3"
      title="页面被浏览器重新加载了"
      :description="reloadNotice"
      close
      @update:open="reloadNotice = ''"
    />

    <!-- 主体两栏布局：左栏操作区（上传+参数），右栏预览区。
         ⚠️ 手机上两个 column 包装器用 contents 拆掉，让它们的子项直接成为
         外层栅格的项 —— 这样才排得出「上传 → 参数 → 预览 → 费用 → 按钮」。
         否则预览被关在右栏里，永远只能整体排在左栏（含费用和按钮）之后，
         用户会先看到要花多少钱、再看到打出来长什么样。
         桌面端 lg:block 把包装器还原成真实盒子，order-* 在块布局里不生效。 -->
    <div v-if="entered" class="grid grid-cols-1 lg:grid-cols-5 gap-4">
      <!-- 左栏：上传 + 打印参数 -->
      <div class="contents lg:block lg:col-span-2 lg:space-y-4">
        <div class="order-1 space-y-4">
          <!-- 1. 文件上传 — 仅标准模式 -->
          <FileUpload
            v-if="printMode === 'standard'"
            :selected-file="selectedFile"
            :display-name="fileDisplayName"
            :converting="converting"
            :converted="converted"
            :preview-url="previewUrl"
            :download-name="downloadName"
            :pdf-blob="pdfBlob"
            :can-print="canPrint"
            :can-convert="canConvert"
            :printing="printing"
            :is-multi-image="isMultiImage"
            :total-size="multiImageTotalSize"
            :gs-applying="gsApplying"
            :gs-applied="gsApplied"
            @file-selected="processFile"
            @files-selected="processMultipleImages"
            @files-batch-selected="processBatchFiles"
            @clear="clearFile"
            @convert="convertToPdf"
            @apply-gs="applyGsNormalization"
            @print="uploadAndPrint"
          />

          <!-- 发票模式上传 -->
          <UCard v-if="printMode === 'invoice'" :ui="{ body: 'p-3 sm:p-4' }">
            <div class="space-y-3">
              <div
                class="border-2 border-dashed rounded-lg p-2 sm:p-4 text-center cursor-pointer transition-colors"
                :class="invoiceDragging ? 'border-primary bg-primary/5' : 'border-muted hover:border-primary/50'"
                @dragover.prevent="invoiceDragging = true"
                @dragleave="invoiceDragging = false"
                @drop.prevent="onInvoiceDrop"
                @click="invoiceInput.click()"
              >
                <input ref="invoiceInput" type="file" class="hidden" multiple @change="onInvoiceFileChange" />
                <div class="flex items-center justify-center gap-2 text-sm text-muted py-1">
                  <UIcon name="i-lucide-receipt" class="w-5 h-5" />
                  <span>上传发票文件（支持多选，PDF / 图片 / OFD）</span>
                </div>
              </div>
              <div v-if="invoiceFiles.length > 0" class="space-y-1">
                <div v-for="(f, idx) in invoiceFiles" :key="idx" class="flex items-center gap-2 text-sm px-2 py-1 rounded hover:bg-elevated">
                  <UIcon name="i-lucide-file" class="w-4 h-4 text-muted shrink-0" />
                  <span class="flex-1 truncate">{{ f.name }}</span>
                  <span class="text-xs text-muted shrink-0">{{ formatFileSize(f.size) }}</span>
                  <UButton variant="ghost" size="xs" color="error" icon="i-lucide-x" class="shrink-0" @click="removeInvoiceFile(idx)" />
                </div>
              </div>
              <UButton
                v-if="invoiceFiles.length > 0"
                variant="outline"
                size="sm"
                icon="i-lucide-layers"
                :loading="composing"
                :disabled="composing"
                @click="composeAndPreview"
              >合并预览 ({{ invoiceFiles.length }} 个文件)</UButton>
            </div>
          </UCard>

          <!-- 身份证模式上传 -->
          <IdCardUpload
            v-if="printMode === 'id_card'"
            :front="idCardFront"
            :back="idCardBack"
            :front-preview="idCardFrontPreview"
            :back-preview="idCardBackPreview"
            @update:front="onIdCardFront"
            @update:back="onIdCardBack"
          />
          <div v-if="printMode === 'id_card'" class="flex items-center gap-3">
            <span class="text-sm text-muted shrink-0">版面</span>
            <div class="flex rounded-lg border border-muted overflow-hidden">
              <label
                v-for="p in ['A4', 'A5']"
                :key="p"
                class="px-3 py-1 cursor-pointer text-sm transition"
                :class="idCardPaper === p ? 'bg-primary text-white font-medium' : 'hover:bg-elevated'"
              >
                <input type="radio" :value="p" :checked="idCardPaper === p" class="sr-only" @change="setIdCardPaper(p)" />
                {{ p }}
              </label>
            </div>
          </div>
          <UButton
            v-if="printMode === 'id_card' && idCardFront && idCardBack"
            variant="outline"
            size="sm"
            icon="i-lucide-layers"
            :loading="composing"
            :disabled="composing"
            @click="composeAndPreview"
          >合并预览</UButton>

          <!-- 文字速印(issue #70):粘贴文字直接打,不必先建 .txt 上传 -->
          <UCard v-if="printMode === 'text'" :ui="{ body: 'p-3 sm:p-4' }">
            <div class="space-y-3">
              <UTextarea
                v-model="quickText"
                :rows="10"
                placeholder="在此粘贴要打印的文字…"
                class="w-full"
                :disabled="converting || printing"
              />
              <div class="flex items-center justify-between gap-2 text-xs text-muted">
                <span>{{ quickText.length }} 字符</span>
                <UButton
                  v-if="quickText"
                  variant="ghost"
                  size="xs"
                  icon="i-lucide-x"
                  :disabled="converting || printing"
                  @click="quickText = ''"
                >清空</UButton>
              </div>
              <UButton
                variant="outline"
                size="sm"
                icon="i-lucide-file-text"
                :loading="converting"
                :disabled="!quickText.trim() || converting || printing"
                @click="convertQuickText"
              >转换预览</UButton>
            </div>
          </UCard>

          <!-- 多图片列表（仅标准模式） -->
          <UCard v-if="printMode === 'standard' && selectedImages.length > 1">
            <template #header>
              <div class="flex items-center justify-between">
                <div class="flex items-center gap-2 font-semibold">
                  <UIcon name="i-lucide-images" class="w-5 h-5" />
                  已选图片 ({{ selectedImages.length }})
                </div>
                <UButton variant="ghost" size="xs" color="error" icon="i-lucide-trash-2" @click="clearFile">清空全部</UButton>
              </div>
            </template>
            <!-- 每页张数(issue #37):类 WinXP 打印图片向导,把多图排到同一页 -->
            <div class="flex items-center gap-2 mb-2 text-sm">
              <span class="text-muted shrink-0">每页张数</span>
              <div class="flex rounded-lg border border-muted overflow-hidden">
                <label
                  v-for="n in [1, 2, 4, 6, 9]"
                  :key="n"
                  class="px-3 py-1 cursor-pointer text-sm transition"
                  :class="imagesPerPage === n ? 'bg-primary text-white font-medium' : 'hover:bg-elevated'"
                >
                  <input type="radio" :value="n" :checked="imagesPerPage === n" class="sr-only" @change="setImagesPerPage(n)" />
                  {{ n }}
                </label>
              </div>
            </div>
            <div class="grid grid-cols-2 sm:grid-cols-3 gap-2">
              <div v-for="(img, idx) in selectedImages" :key="idx" class="relative group rounded-lg overflow-hidden border border-default">
                <img :src="imageThumbnails[idx]" class="w-full h-20 object-cover" :style="imageRotations[idx] ? `transform: rotate(${imageRotations[idx]}deg)` : ''" />
                <div class="absolute top-1 right-1 flex gap-1">
                  <UButton variant="solid" size="xs" color="primary" icon="i-lucide-rotate-ccw" title="左旋90°" @click="rotateImage(idx, -90)" />
                  <UButton variant="solid" size="xs" color="primary" icon="i-lucide-rotate-cw" title="右旋90°" @click="rotateImage(idx, 90)" />
                </div>
                <div class="absolute inset-0 bg-black/40 opacity-0 group-hover:opacity-100 transition-opacity flex items-center justify-center">
                  <UButton variant="solid" size="xs" color="error" icon="i-lucide-x" @click="removeImage(idx)" />
                </div>
                <p class="text-xs truncate px-1 py-0.5">{{ img.name }}<span v-if="imageRotations[idx]" class="text-muted"> · {{ imageRotations[idx] }}°</span></p>
              </div>
            </div>
          </UCard>

          <!-- 打印参数 -->
          <PrintOptions
            v-model:isColor="isColor"
            v-model:invert="invert"
            :invert-visible="isInvertible"
            v-model:duplex="duplex"
            v-model:copies="copies"
            v-model:paperSize="paperSize"
            v-model:paperType="paperType"
            v-model:mediaSource="mediaSource"
            :media-source-supported="printerInfo?.mediaSourceSupported || []"
            v-model:printScaling="printScaling"
            v-model:scalePercent="scalePercent"
            v-model:pageRange="pageRange"
            v-model:pageSet="pageSet"
            v-model:mirror="mirror"
            v-model:watermarkText="watermarkText"
            v-model:numberUp="numberUp"
            v-model:numberUpLayout="numberUpLayout"
            v-model:pageBorder="pageBorder"
            :printing="printing"
            @page-range-invalid="pageRangeInvalid = $event"
          />
        </div><!-- /order-1：上传 + 参数 -->

      <!-- 手机上的第三屏（排在预览之后）：先看打出来什么样，再看要花多少 -->
        <div class="order-3 space-y-4">
          <!-- 价格条：插在参数区与打印按钮之间，让用户在点按钮前就看到要花多少 -->
          <PriceSummary
            v-if="billing.enabled"
            :doc-pages="docPages"
            :page-range="pageRange"
            :page-set="pageSet"
            :is-color="isColor"
            :paper-size="paperSize"
            :paper-type="paperType"
            :copies="copies"
            :prices="billing"
            :balance-cents="wallet.balanceCents || 0"
            :pages-estimated="pagesEstimated"
            :error="billingError"
            @go-wallet="goWallet"
          />

          <!-- 开始打印按钮 -->
          <UButton
            color="primary"
            size="xl"
            :ui="{ base: 'justify-center', label: 'flex-1 text-center' }"
            class="w-full font-semibold tracking-wide shadow-lg shadow-primary/25 hover:shadow-xl hover:shadow-primary/35 transition-all ring-1 ring-primary/30"
            icon="i-lucide-send"
            :disabled="!canPrint || printing || batchPrinting"
            :loading="printing || batchPrinting"
            @click="uploadAndPrint"
          >
            {{ printButtonLabel }}
          </UButton>
        </div><!-- /order-3：费用 + 按钮 -->
      </div><!-- /左栏 -->

      <!-- 右栏：预览 + 打印记录 + 打印机状态 -->
      <div class="contents lg:block lg:col-span-3 lg:space-y-4">
      <!-- 手机上的第二屏：紧跟在上传/参数之后 -->
        <div class="order-2 space-y-4">
          <div class="lg:sticky lg:top-4 space-y-4">
            <PrintPreview
              :selected-file="selectedFile"
              :is-multi-image="isMultiImage"
              :preview-url="previewUrl"
              :preview-type="previewType"
              :text-preview="textPreview"
              :paper-size-label="paperSizeLabel"
              v-model:orientation="orientation"
              :orientation-label="orientationLabel"
              :paper-dim-text="paperDimText"
              :paper-preview-style="paperPreviewStyle"
              :watermark-text="watermarkText"
              :print-scaling="printScaling"
              :scale-percent="scalePercent"
            />
          </div>
        </div><!-- /order-2：预览 -->

        <!-- 手机上的第四屏：只给管理员看的打印机状态，排在最后 -->
        <div class="order-4 space-y-4">
          <!-- 打印机状态整块只给管理员：墨量、纸盒、队列都是运维信息。
               普通用户只需要名字旁那个连接状态灯（见顶栏的 deviceState）。
               打印记录也搬走了 —— 它属于「回头再看」，在个人中心里。 -->
          <PrinterStatus
            v-if="isAdmin"
            :printer-info="printerInfo"
            :printer-uri="printer"
            :loading="loadingPrinterInfo"
            :error="printerInfoError"
            @refresh="loadPrinterInfo"
          />
        </div><!-- /order-4：记录与状态 -->
      </div><!-- /右栏 -->
    </div>

    <!-- CUPS 任务列表弹窗（issue #60）：查看/取消 CUPS 未完成任务 -->
    <UModal v-model:open="showJobsModal" :ui="{ content: 'max-w-3xl' }">
      <template #content>
        <div class="p-4 sm:p-6 space-y-3">
          <div class="flex items-center justify-between gap-2">
            <h3 class="text-lg font-semibold flex items-center gap-2">
              <UIcon name="i-lucide-list-todo" class="w-5 h-5 text-primary" />
              CUPS 任务
            </h3>
            <div class="flex items-center gap-2">
              <UButton
                variant="ghost"
                size="xs"
                icon="i-lucide-refresh-cw"
                :loading="loadingJobs"
                @click="loadJobs(true)"
              >刷新</UButton>
              <UButton variant="ghost" size="xs" icon="i-lucide-x" @click="showJobsModal = false" />
            </div>
          </div>
          <p class="text-xs text-muted">
            列出 CUPS 队列中所有未完成的任务；每 5 秒自动刷新。可取消卡在 processing / stopped 的任务。
          </p>

          <div v-if="jobsError" class="text-sm text-error break-all">
            {{ jobsError }}
          </div>

          <div v-if="loadingJobs && !cupsJobs.length" class="py-8 text-center text-sm text-muted">
            加载中…
          </div>
          <div v-else-if="!cupsJobs.length" class="py-8 text-center text-sm text-muted">
            当前没有未完成的任务。
          </div>
          <div v-else class="overflow-x-auto -mx-2 sm:mx-0">
            <table class="w-full text-sm">
              <thead>
                <tr class="text-left text-xs text-muted border-b border-muted">
                  <th class="py-2 px-2">ID</th>
                  <th class="py-2 px-2">打印机</th>
                  <th class="py-2 px-2">文件</th>
                  <th class="py-2 px-2">用户</th>
                  <th class="py-2 px-2">状态</th>
                  <th class="py-2 px-2 text-right">操作</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="j in cupsJobs" :key="j.id" class="border-b border-muted/50">
                  <td class="py-2 px-2 font-mono">{{ j.id }}</td>
                  <td class="py-2 px-2 truncate max-w-[8rem]" :title="j.printerName">{{ j.printerName || '-' }}</td>
                  <td class="py-2 px-2 truncate max-w-[10rem]" :title="j.name">{{ j.name || '-' }}</td>
                  <td class="py-2 px-2 truncate max-w-[6rem]" :title="j.user">{{ j.user || '-' }}</td>
                  <td class="py-2 px-2">
                    <span
                      class="inline-block px-1.5 py-0.5 rounded text-xs"
                      :class="jobStateClass(j)"
                    >{{ j.stateText }}</span>
                    <div v-if="j.stateReasons?.length" class="text-[10px] text-muted mt-0.5 break-all">
                      {{ j.stateReasons.join(', ') }}
                    </div>
                    <div v-if="j.sheetsCompleted > 0" class="text-[10px] text-muted mt-0.5">
                      已打印 {{ j.sheetsCompleted }} 页
                    </div>
                  </td>
                  <td class="py-2 px-2 text-right">
                    <UButton
                      size="xs"
                      color="error"
                      variant="soft"
                      :loading="cancelingJobId === j.id"
                      @click="cancelJob(j)"
                    >取消</UButton>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </template>
    </UModal>

    <!-- 支付弹窗：计费开启时下单后弹出，负责支付与结果轮询 -->
    <PaymentModal
      v-if="billing.enabled"
      v-model:open="showPaymentModal"
      :batch="orderBatch"
      :payment-methods="wallet.paymentMethods || []"
      @paid="onPaid"
      @go-wallet="goWallet"
      @done="goPrintResult"
    />
  </div>
</template>

<script setup>
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { apiFetch, readError, readJSON } from '../utils/api'
import { isOfficeFile, isOFDFile } from '../utils/file'
import { downscaleImageIfNeeded } from '../utils/image'
import { estimateImageDocPages } from '../utils/pricing'
import { paymentSettledTick } from '../utils/paymentSignal'
import { savePendingUpload, loadPendingUpload, clearPendingUpload } from '../utils/pendingUpload'
import FileUpload from '../components/print/FileUpload.vue'
import IdCardUpload from '../components/print/IdCardUpload.vue'
import PrintPreview from '../components/print/PrintPreview.vue'
import PrintOptions from '../components/print/PrintOptions.vue'
import PrinterStatus from '../components/print/PrinterStatus.vue'
import PriceSummary from '../components/print/PriceSummary.vue'
import PaymentModal from '../components/print/PaymentModal.vue'
import { formatFileSize, printerLabel, printerDescription } from '../utils/format'

const props = defineProps({ session: Object })
const emit = defineEmits(['logout', 'go-wallet'])
const router = useRouter()

// CUPS 任务列表与打印机状态整块只给管理员看。
//
// 普通用户要回答的问题是「这台能不能打」，不是「队列里排了谁、墨盒还剩
// 百分之几」—— 那些是运维信息，摊在打印页上只会让人以为要自己处理。
// 连接状态单独做成打印机名字旁边的一个指示（见 deviceState）。
const isAdmin = computed(() => props.session?.role === 'admin')
const toast = useToast()

// ─── 打印机 ───────────────────────────────────────────────
const printer = ref('')
const printers = ref([])

// ─── 文件 ─────────────────────────────────────────────────
const selectedFile = ref(null)
const previewUrl = ref('')
const previewType = ref('')
const textPreview = ref('')
const converting = ref(false)
const converted = ref(false)
const pdfBlob = ref(null)
const downloadName = ref('')
const selectedImages = ref([])
// 多图每页张数(issue #37):1=每张一页(默认,历史行为);2/4/6/9=同一页网格排版。
// 前端把这个值透给 /api/convert 的 per_page,后端 convertImagesMultiToPDF 会算网格。
const imagesPerPage = ref(1)
const imageThumbnails = ref([])
const imageRotations = ref([])
const fileDisplayName = ref('')
// 是否对当前 PDF 应用了后端 gs 规范化（仅 PDF 上传后通过 UI 按钮显式触发）
const gsApplying = ref(false)
const gsApplied = ref(false)

// ─── 批量打印 ────────────────────────────────────────────
const batchFiles = ref([])
const batchPrinting = ref(false)
const batchProgress = ref({ current: 0, total: 0 })

// ─── 打印参数 ─────────────────────────────────────────────
const isColor = ref(true)
// 黑白反转（issue #87）：仅图片转 PDF、黑白模式下生效
const invert = ref(false)
const duplex = ref('one-sided')
const orientation = ref('portrait')
const copies = ref(1)
const paperSize = ref('A4')
const paperType = ref('plain')
const mediaSource = ref('auto')
// 默认走 IPP 的 `auto`：文档不缩放、图片自适应，最贴近 CUPS 原生行为。
// 早先默认 `fit` 会把 PDF 内容拉伸至打印机纸张（尤其上传 PDF 尺寸 ≠ 目标
// paperSize 时），字体变大、多出一页（issue #47）。用户需要撑满时仍可手选 fit。
const printScaling = ref('auto')
const scalePercent = ref(100)
const pageRange = ref('')
const pageSet = ref('all')
// 页码合法性由 PrintOptions 校验后回传（空串 = 合法）。
// 必须参与 canPrint：只显示错误而不拦截，用户仍能提交 "5-3" 这类范围，
// 而它在 IPP 侧会被静默丢弃 → 打印整个文档。
const pageRangeInvalid = ref('')
const mirror = ref(false)
const watermarkText = ref('')
const numberUp = ref(1)
const numberUpLayout = ref('lrtb')
const pageBorder = ref('none')

// ─── 打印模式 ─────────────────────────────────────────────
const printMode = ref(localStorage.getItem('print_mode') || 'standard')
// hint 只出现在落地页的大按钮上：那里光有名字不够，用户得知道每个模式
// 是干什么的（尤其「发票」和「身份证」这两个专用流程）。
const printModeItems = [
  { label: '标准打印', value: 'standard', icon: 'i-lucide-file-text', hint: '上传 PDF / 图片 / Office 文档' },
  { label: '文字速印', value: 'text', icon: 'i-lucide-clipboard-type', hint: '粘贴一段文字直接打印' },
  { label: '发票打印', value: 'invoice', icon: 'i-lucide-receipt', hint: '多张发票合并到一页' },
  { label: '身份证打印', value: 'id_card', icon: 'i-lucide-id-card', hint: '正反面复印到一张纸上' }
]

// 是否已经选好模式进入打印页。
//
// ⚠️ 刻意不复用 localStorage 里那个 print_mode 来决定进不进 ——
// 进入这个页面【永远】先看到落地页（打印机 + 四个按钮）。手机屏小，
// 一上来就是上传区 + 十几个参数 + 预览 + 记录，用户根本找不到北。
// 存下来的模式只用来让 switchMode 在「选的是同一个模式」时保留已有内容。
// ─── 浏览器重载之后不把人扔回起点 ─────────────────────────────
//
// 实测现象：手机上选完文件，偶尔整个页面被重新加载，回到四个按钮的落地页，
// 得重新选一遍文件。
//
// 重载不是我们触发的（应用代码里没有任何 location.reload），是浏览器自己干的：
// 打开文件选择器时页面被切到后台，手机内存紧张就把标签页回收了，用户回来时
// 浏览器重新加载整个页面，JS 状态全丢。文件对象也随页面一起没了 ——
// 那一步我们救不回来，但至少别让用户连「刚才选的哪种打印」都重来一遍。
//
// ⚠️ 只在【确认是浏览器重载】时才恢复。点导航进这个页面仍然看落地页 ——
// 那是刻意的入口设计（手机屏小，一上来就是上传区+十几个参数+预览，
// 用户找不到北），不能因为做了这个恢复就把它破坏掉。
const RESTORE_KEY = 'print_session'

function navType() {
  try {
    return performance.getEntriesByType('navigation')[0]?.type || 'unknown'
  } catch {
    return 'unknown'
  }
}

// 「重载」的判据。实测手机回收标签页后恢复时，浏览器上报的类型不一定是
// reload —— back_forward（从会话历史恢复）也可能。两种都算。
function isBrowserReload() {
  const t = navType()
  return t === 'reload' || t === 'back_forward'
}

// 会话里存什么：小的一律进 sessionStorage（字符串、数字，写起来同步且便宜），
// 文件走 IndexedDB（见 utils/pendingUpload.js）。
//
// 存参数不只是为了好看：用户调了双面、份数、页码再被回收，回来发现全变回默认值，
// 和文件丢了是一样烦的，而且他未必记得自己调过什么。
function saveSession() {
  try {
    sessionStorage.setItem(RESTORE_KEY, JSON.stringify({
      entered: entered.value,
      mode: printMode.value,
      quickText: quickText.value,
      opts: {
        isColor: isColor.value,
        duplex: duplex.value,
        orientation: orientation.value,
        copies: copies.value,
        paperSize: paperSize.value,
        paperType: paperType.value,
        mediaSource: mediaSource.value,
        printScaling: printScaling.value,
        scalePercent: scalePercent.value,
        pageRange: pageRange.value,
        pageSet: pageSet.value,
        mirror: mirror.value,
        watermarkText: watermarkText.value,
        numberUp: numberUp.value,
        numberUpLayout: numberUpLayout.value,
        pageBorder: pageBorder.value
      }
    }))
  } catch {
    // 隐私模式下 sessionStorage 可能直接抛异常。恢复不了不影响正常使用。
  }
}

function applySavedOptions(opts, mode) {
  if (!opts || typeof opts !== 'object') return
  // 先套模式默认值，再用用户存下来的覆盖 —— 否则恢复出来的参数
  // 会和用户当时看到的界面不一致（见 applyModeDefaults 的说明）。
  applyModeDefaults(mode)
  const setIf = (ref, key) => {
    if (opts[key] !== undefined && opts[key] !== null) ref.value = opts[key]
  }
  setIf(isColor, 'isColor')
  setIf(duplex, 'duplex')
  setIf(orientation, 'orientation')
  setIf(copies, 'copies')
  setIf(paperSize, 'paperSize')
  setIf(paperType, 'paperType')
  setIf(mediaSource, 'mediaSource')
  setIf(printScaling, 'printScaling')
  setIf(scalePercent, 'scalePercent')
  setIf(pageRange, 'pageRange')
  setIf(pageSet, 'pageSet')
  setIf(mirror, 'mirror')
  setIf(watermarkText, 'watermarkText')
  setIf(numberUp, 'numberUp')
  setIf(numberUpLayout, 'numberUpLayout')
  setIf(pageBorder, 'pageBorder')
}

function restoreAfterReload() {
  // ⚠️ 这行是【诊断入口】，别当成调试残留删掉。
  //
  // 「选完文件偶尔回到四个按钮首页」只有手机能复现，而我们离线判断不了
  // 浏览器的加载类型。出问题时让用户看一眼控制台这一行，就能区分：
  //   reload / back_forward → 浏览器把标签页回收后重新加载（本次要处理的情况）
  //   navigate              → 是别的路径进来的，得另找原因
  //   unknown               → 浏览器不支持 Navigation Timing
  console.info('[print] 页面加载类型:', navType(),
    '| 上次退出时已进入打印页:', sessionStorage.getItem(RESTORE_KEY) || '（无）')

  if (!isBrowserReload()) return false
  try {
    const saved = JSON.parse(sessionStorage.getItem(RESTORE_KEY) || 'null')
    if (!saved?.entered) return false
    // 不走 switchMode：它会先 clearFile/clearModeState，而这里本就是
    // 想恢复「同一个模式」的界面。
    const mode = printModeItems.some(m => m.value === saved.mode) ? saved.mode : printMode.value
    printMode.value = mode
    if (typeof saved.quickText === 'string') quickText.value = saved.quickText
    applySavedOptions(saved.opts, mode)
    return true
  } catch {
    return false
  }
}

const restoredFromReload = ref(false)
// 重载后的提示文案；空 = 不显示。文案在 restorePendingUpload 里按
// 「文件有没有回来」决定，见那里的说明。
const reloadNotice = ref('')
const entered = ref(false)
// ⚠️ restoreAfterReload() 的调用【必须】留在 setup 末尾（见本文件最后），
// 不能挪到这里。它要写 quickText 和一堆打印参数，而那些 ref 有些声明在
// 这行【之后】—— const 有暂时性死区，提前调用会抛
// `Cannot access 'quickText' before initialization`，整个页面白屏。
// 实测踩过：报错只在浏览器控制台，构建与静态检查都是绿的。

// ⚠️ 记会话的那条 watch 不在这里注册 —— 见文件末尾「会话与文件的持久化」。
// watch([...]) 的【源数组是立即求值的】，而它要引用的 quickText 声明在下面几百行，
// 放在这里会踩暂时性死区、整页白屏。

// ─── 待打印文件的暂存 ─────────────────────────────────────────
//
// 参数（上面那些）存 sessionStorage 就够了，文件不行 —— 二进制、几十 MB。
// 走 IndexedDB，见 utils/pendingUpload.js。

// pendingPayload 把「用户上传的原始文件」整理成可暂存的形状。
//
// ⚠️ 存【原始入参】而不是处理后的中间产物：恢复时走同一个入口函数
// （processFile / processMultipleImages / …），代码路径和用户当初点的
// 一模一样。自己去拼内部状态（previewUrl / pdfBlob / converted /
// imageThumbnails…）迟早会漏掉一个 ref，表现为「预览看着对，但某个参数
// 没生效」这种极难查的问题。
function pendingPayload() {
  if (printMode.value === 'text') {
    return quickText.value.trim() ? { mode: 'text', quickText: quickText.value } : null
  }
  if (printMode.value === 'invoice') {
    return invoiceFiles.value.length ? { mode: 'invoice', invoice: [...invoiceFiles.value] } : null
  }
  if (printMode.value === 'id_card') {
    return (idCardFront.value || idCardBack.value)
      ? { mode: 'id_card', front: idCardFront.value, back: idCardBack.value }
      : null
  }
  if (selectedImages.value.length > 1) return { mode: 'standard', multi: [...selectedImages.value] }
  if (selectedFile.value) return { mode: 'standard', file: selectedFile.value }
  if (batchFiles.value.length) return { mode: 'standard', batch: [...batchFiles.value] }
  return null
}

// syncPendingUpload 是唯一的写入口：有内容就存，没内容就清。
//
// ⚠️ 只留一个入口是刻意的。clearFile() 在 processFile() 开头也会被调用，
// 若清空与保存各写各的，两个异步操作会互相超车。走同一个函数、按调用
// 顺序发事务，最后一次调用说了算。
function syncPendingUpload() {
  const payload = pendingPayload()
  if (!payload) {
    clearPendingUpload()
    return
  }
  savePendingUpload(payload)
}

// restorePendingUpload 把暂存的文件接回去。
//
// ⚠️ 只在【确认是浏览器重载】时做。从导航进这个页面时看到落地页是刻意的
// 设计，不该被自动填上一个上一次留下的文件。
async function restorePendingUpload() {
  if (!restoredFromReload.value) return
  // 用户手快，已经自己选好文件了 —— 别覆盖他。
  if (pendingPayload()) return

  const rec = await loadPendingUpload()
  try {
    if (rec) {
      if (rec.mode === 'text') {
        if (rec.quickText) await convertQuickText()
      } else if (rec.mode === 'invoice') {
        invoiceFiles.value = rec.invoice || []
      } else if (rec.mode === 'id_card') {
        if (rec.front) onIdCardFront(rec.front)
        if (rec.back) onIdCardBack(rec.back)
      } else if (rec.multi?.length > 1) {
        processMultipleImages(rec.multi)
      } else if (rec.batch?.length) {
        processBatchFiles(rec.batch)
      } else if (rec.file) {
        processFile(rec.file)
      }
    }
  } catch (e) {
    // 恢复失败就退回「用户自己重新选」，不该因此弹错。
    console.warn('[pending] 恢复上传文件失败:', e)
  }

  // 提示文案按【文件有没有真的回来】来选 —— 两种情况用户要做的事不同：
  // 回来了就什么都不用做，没回来才需要重新选一次。说错会让人白折腾。
  reloadNotice.value = pendingPayload()
    ? '页面被浏览器重新加载了（手机上常见于内存不足）。已恢复你刚才的打印方式、参数和文件。'
    : '页面被浏览器重新加载了（手机上常见于内存不足）。已回到刚才的打印方式，请重新选择文件。'
}

const currentMode = computed(
  () => printModeItems.find(m => m.value === printMode.value) || printModeItems[0]
)

function enterMode(mode) {
  switchMode(mode)
  entered.value = true
}

function backToLanding() {
  entered.value = false
  restoredFromReload.value = false
  reloadNotice.value = ''
}
// 文字速印(issue #70):用户直接粘贴一段文字就能打,省去先建 .txt 再上传的步骤。
// 内容用 File 包装喂给现有 processFile 流程,后端已支持 fileKindText。
const quickText = ref('')
const invoiceFiles = ref([])
const invoiceDragging = ref(false)
const invoiceInput = ref(null)
const idCardFront = ref(null)
const idCardBack = ref(null)
const idCardFrontPreview = ref('')
const idCardBackPreview = ref('')
const idCardPaper = ref('A4')
const composing = ref(false)

// ─── 状态 ─────────────────────────────────────────────────
const printing = ref(false)
const refreshing = ref(false)

// ─── CUPS 任务(issue #60) ────────────────────────────────
// 打开一个 UModal 展示 CUPS 队列里所有未完成任务;每 5s 轮询一次(仅在 modal 开着时)。
// 允许用户直接取消卡在 processing/stopped 的任务 —— 这是 #60 的核心诉求。
const showJobsModal = ref(false)
const cupsJobs = ref([])
const loadingJobs = ref(false)
const jobsError = ref('')
const cancelingJobId = ref(0)
let jobsTimer = null

async function loadJobs(showLoading = false) {
  if (showLoading) loadingJobs.value = true
  jobsError.value = ''
  try {
    const resp = await apiFetch('/api/cups-jobs', {}, () => emit('logout'))
    if (resp.ok) {
      cupsJobs.value = await readJSON(resp) || []
    } else if (resp.status !== 401) {
      jobsError.value = await readError(resp)
    }
  } catch (e) {
    jobsError.value = e?.message || '加载失败'
  } finally {
    loadingJobs.value = false
  }
}

function openJobsModal() {
  showJobsModal.value = true
  loadJobs(true)
}

// 打开/关闭 modal 时启停 5s 轮询;避免关闭后仍空转拉后端。
watch(showJobsModal, (open) => {
  if (jobsTimer) { clearInterval(jobsTimer); jobsTimer = null }
  if (open) {
    jobsTimer = setInterval(() => loadJobs(false), 5000)
  }
})

async function cancelJob(job) {
  if (!job?.id || cancelingJobId.value) return
  cancelingJobId.value = job.id
  try {
    const resp = await apiFetch('/api/cups-jobs/cancel', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ printerUri: job.printerUri, jobId: job.id }),
    }, () => emit('logout'))
    if (resp.ok) {
      toast.add({ title: `已取消任务 #${job.id}`, color: 'success' })
      await loadJobs(false)
    } else if (resp.status !== 401) {
      const msg = await readError(resp)
      toast.add({ title: '取消失败', description: msg, color: 'error' })
    }
  } catch (e) {
    toast.add({ title: '取消失败', description: e.message, color: 'error' })
  } finally {
    cancelingJobId.value = 0
  }
}

function jobStateClass(j) {
  // stopped / 带 reason 的通常是"卡住"的信号,用红/黄突出。
  if (j.state === 6) return 'bg-error/10 text-error'
  if (j.stateReasons?.length) return 'bg-warning/10 text-warning'
  if (j.state === 5) return 'bg-primary/10 text-primary'
  return 'bg-muted text-muted'
}


// ─── 打印机状态 ───────────────────────────────────────────
const printerInfo = ref(null)
const loadingPrinterInfo = ref(false)
const printerInfoError = ref('')

// deviceState 是打印机名字旁边那个连接状态灯。
//
// 它替代了原来给普通用户看的整块「打印机状态」卡片：用户只需要知道
// 「连上了没有」，墨量、纸盒、队列那些是管理员的事。
//
// ⚠️ 判据直接取后端直连探测的结果（/api/printer-info 的 device.probed /
// device.reachable），不能用 cupsd 报的 printer-state —— 那是【队列】状态，
// 打印机拔了电它照样报 idle。论证见 AGENTS.md 的「打印机的真实状态必须直连探测」。
//
// ⚠️ probed 与 reachable 必须分开：
//   probed=false  → 探不了（USB 等），【不等于】连不上，显示中性态
//   只有 probed && !reachable 才是真的连不上
// 合并成一句「离线」会把 USB 打印机的用户永远吓住。
const deviceState = computed(() => {
  if (!printer.value) return { text: '未选择', tone: 'muted', title: '还没有选择打印机' }
  if (loadingPrinterInfo.value && !printerInfo.value) {
    return { text: '检测中…', tone: 'muted', title: '正在查询打印机状态' }
  }
  const d = printerInfo.value?.device
  if (printerInfoError.value) {
    return { text: '状态未知', tone: 'muted', title: printerInfoError.value }
  }
  if (!d || !d.probed) {
    return { text: '无法探测', tone: 'muted', title: '这类连接方式不支持直连探测，不代表打印机离线' }
  }
  if (d.reachable) {
    return { text: '已连接', tone: 'success', title: '直连探测确认打印机在线' }
  }
  return { text: '未连接', tone: 'error', title: '直连探测确认连不上打印机，请检查电源与网络' }
})

// 状态灯的颜色。用显式的三档而不是拼字符串，Tailwind 才扫得到类名。
const DEVICE_TONE_CLASS = {
  success: 'bg-success',
  error: 'bg-error',
  muted: 'bg-muted'
}
const DEVICE_TEXT_CLASS = {
  success: 'text-success',
  error: 'text-error',
  muted: 'text-muted'
}

// ─── 纸张尺寸映射 ─────────────────────────────────────────
const paperDimensionsMap = {
  'A5': { width: 148, height: 210 },
  'A4': { width: 210, height: 297 },
  'A3': { width: 297, height: 420 },
  'A2': { width: 420, height: 594 },
  'A1': { width: 594, height: 841 },
  '5inch': { width: 89, height: 127 },
  '6inch': { width: 102, height: 152 },
  '7inch': { width: 127, height: 178 },
  '8inch': { width: 152, height: 203 },
  '10inch': { width: 203, height: 254 },
  'Letter': { width: 216, height: 279 },
  'Legal': { width: 216, height: 356 },
}

// ─── 选项列表（供 PrintOptions 内部的 paperSizeLabel 等计算使用） ──
const orientationItems = [
  { label: '纵向', value: 'portrait' },
  { label: '横向', value: 'landscape' }
]
const paperSizeItems = [
  { label: 'A5 (148×210mm)', value: 'A5' },
  { label: 'A4 (210×297mm)', value: 'A4' },
  { label: 'A3 (297×420mm)', value: 'A3' },
  { label: 'A2 (420×594mm)', value: 'A2' },
  { label: 'A1 (594×841mm)', value: 'A1' },
  { label: '5寸 (89×127mm)', value: '5inch' },
  { label: '6寸 (102×152mm)', value: '6inch' },
  { label: '7寸 (127×178mm)', value: '7inch' },
  { label: '8寸 (152×203mm)', value: '8inch' },
  { label: '10寸 (203×254mm)', value: '10inch' },
  { label: 'Letter (8.5×11in)', value: 'Letter' },
  { label: 'Legal (8.5×14in)', value: 'Legal' }
]

// ─── 计算属性 ─────────────────────────────────────────────
const isMultiImage = computed(() => selectedImages.value.length > 1)
const multiImageTotalSize = computed(() => selectedImages.value.reduce((sum, f) => sum + f.size, 0))
// 当前是否为图片场景（单图图片或多图合一），决定「黑白反转」是否可用
const isImageKind = computed(() =>
  isMultiImage.value ||
  (selectedFile.value && selectedFile.value.type && selectedFile.value.type.startsWith('image/'))
)
// 黑白反转仅对图片转 PDF 且黑白模式有效
const isInvertible = computed(() => isImageKind.value && !isColor.value)
// 切换到非「图片+黑白」场景时重置反转，避免彩色时残留反色
watch(isInvertible, (v) => { if (!v) invert.value = false })
const canPrint = computed(() => {
  if (!printer.value) return false
  // 页码非法时禁止提交。非法范围（如 "5-3"、"0-5"）会被 IPP 侧静默丢弃，
  // 结果是 page-ranges 属性压根不发送、CUPS 打印整个文档。
  if (pageRangeInvalid.value) return false
  if (printMode.value === 'standard') {
    return !!pdfBlob.value || !!selectedFile.value || isMultiImage.value || batchFiles.value.length > 0
  }
  // 发票/身份证模式：需要已合并出 pdfBlob
  return !!pdfBlob.value
})

const printButtonLabel = computed(() => {
  if (printMode.value === 'standard') {
    return batchFiles.value.length > 0 ? `批量打印 (${batchFiles.value.length} 个文件)` : '开始打印'
  }
  return '开始打印'
})

// 打印机下拉选项：
// label 只放队列名 + CUPS 描述，URI/型号挪到 description 那行（issue #101）：
// 选中态的触发按钮只渲染 label，URI 挤在里面会把描述顶出可视区。
//
// ⚠️ 刷卡取件模式下只列【配有取件终端】的打印机（后端给的 hasKiosk）。
// 这不是锦上添花：员工要是选了没有终端的那台，任务提交后**永远取不出来**，
// 而且不会有任何报错 —— 它躺在待取件里直到超时才被清掉。后端也有一道
// 同样的校验（pullOrderCreateHandler），两边都要有。
const printerItems = computed(() => {
  const list = pull.value.enabled
    ? printers.value.filter(p => p.hasKiosk === true)
    : printers.value
  return list.map(p => ({
    label: printerLabel(p),
    description: printerDescription(p),
    value: p.uri
  }))
})
function onPrinterSelect(val) {
  printer.value = val
  onPrinterChange()
}
const canConvert = computed(() => {
  if (isMultiImage.value) return !converting.value && !converted.value
  return !!selectedFile.value && !converting.value && selectedFile.value.type !== 'application/pdf'
})

const paperSizeLabel = computed(() => {
  const item = paperSizeItems.find(i => i.value === paperSize.value)
  return item?.label || paperSize.value
})

const orientationLabel = computed(() => {
  const item = orientationItems.find(i => i.value === orientation.value)
  return item?.label || (orientation.value === 'portrait' ? '纵向' : '横向')
})

const paperDimText = computed(() => {
  const dim = paperDimensionsMap[paperSize.value]
  if (!dim) return ''
  if (orientation.value === 'landscape') {
    return `${dim.height}×${dim.width}mm`
  }
  return `${dim.width}×${dim.height}mm`
})

// 纸张预览：宽度撑满容器，高度由 aspect-ratio 根据纸张真实比例自动算出。
// 不再限制 maxWidth —— 预览区域宽度始终等于容器宽度，这样"预览区的形状"
// 就能精确同步当前纸张（纵向 / 横向 / 5寸 ... 10寸）的真实比例。
// 提交给后端的 print_scaling：自定义模式下传纯数字百分比。
// 这里再 clamp 一次——输入框在聚焦状态下可能残留区间外的中间值，
// 直接提交会被后端判为非法而退回 100%（见 resolveCustomScaling）。
const printScalingParam = computed(() => {
  if (printScaling.value !== 'custom') return printScaling.value
  const p = Number(scalePercent.value)
  if (!Number.isFinite(p)) return '100'
  return String(Math.round(Math.min(400, Math.max(10, p))))
})

const paperPreviewStyle = computed(() => {
  const dim = paperDimensionsMap[paperSize.value]
  if (!dim) return {}
  const isLandscape = orientation.value === 'landscape'
  const width = isLandscape ? dim.height : dim.width
  const height = isLandscape ? dim.width : dim.height
  return {
    aspectRatio: `${width} / ${height}`,
    width: '100%'
  }
})

// ─── 文件操作 ─────────────────────────────────────────────
function clearPreviewUrl() {
  if (previewUrl.value) {
    try { URL.revokeObjectURL(previewUrl.value) } catch (_) { /* 忽略 */ }
  }
  previewUrl.value = ''
}

function clearFile() {
  clearPreviewUrl()
  previewType.value = ''
  textPreview.value = ''
  pdfBlob.value = null
  converted.value = false
  selectedFile.value = null
  downloadName.value = ''
  // 清空多图片状态
  imageThumbnails.value.forEach(url => { try { URL.revokeObjectURL(url) } catch (_) {} })
  selectedImages.value = []
  imageThumbnails.value = []
  imageRotations.value = []
  imagesPerPage.value = 1
  fileDisplayName.value = ''
  gsApplying.value = false
  gsApplied.value = false
  batchFiles.value = []
}

function processFile(f) {
  clearFile()
  selectedFile.value = f
  fileDisplayName.value = ''
  downloadName.value = f.name.replace(/\.[^/.]+$/, '') + '.pdf'

  if (f.type === 'application/pdf') {
    // 默认不再对上传 PDF 走 /api/convert (gs 规范化)：直接用原始字节做预览和打印，
    // 上传即可秒开预览。如需修复 CJK 字体乱码等问题，用户可点击"应用 GS 规范化"
    // 显式触发 applyGsNormalization()，把当前 PDF 替换为 gs 产物。
    clearPreviewUrl()
    previewUrl.value = URL.createObjectURL(f)
    previewType.value = 'pdf'
    textPreview.value = ''
    pdfBlob.value = f
    converted.value = true
  } else if (f.type.startsWith('image/') || /\.(heic|heif)$/i.test(f.name)) {
    if (isHeicImage(f)) {
      // HEIC/HEIF 浏览器无法原生解码，先提示"正在转换"，异步用 heic2any 转成 JPEG 再预览
      previewType.value = 'text'
      textPreview.value = '正在解码 HEIC/HEIF 图片，请稍候…'
      const originalFile = f
      heicBlobToJpegBlob(originalFile)
        .then(jpegFile => {
          // 若用户在转码期间已切换/清空文件，则丢弃结果
          if (selectedFile.value !== originalFile) return
          selectedFile.value = jpegFile
          downloadName.value = jpegFile.name.replace(/\.[^/.]+$/, '') + '.pdf'
          clearPreviewUrl()
          previewUrl.value = URL.createObjectURL(jpegFile)
          previewType.value = 'image'
          textPreview.value = ''
        })
        .catch(err => {
          if (selectedFile.value !== originalFile) return
          previewType.value = 'text'
          textPreview.value = `HEIC 解码失败：${err.message || '未知错误'}`
          toast.add({ title: 'HEIC 解码失败', description: err.message, color: 'error', icon: 'i-lucide-x-circle' })
        })
    } else {
      previewUrl.value = URL.createObjectURL(f)
      previewType.value = 'image'
    }
  } else if (isOfficeFile(f)) {
    previewType.value = 'text'
    textPreview.value = 'Office 文档（无法直接预览）。点击"转换为 PDF"生成预览。'
  } else if (isOFDFile(f)) {
    previewType.value = 'text'
    textPreview.value = 'OFD文件（开放版式文档）无法直接预览。点击"转换为PDF"生成预览。'
  } else if (f.type.startsWith('text/') || /\.(txt|md|html)$/i.test(f.name)) {
    const reader = new FileReader()
    reader.onload = () => {
      textPreview.value = reader.result
      previewType.value = 'text'
    }
    reader.readAsText(f)
  } else {
    previewType.value = 'text'
    textPreview.value = '无法预览此文件类型，可直接提交打印。'
  }
}

// 判断是否为 HEIC/HEIF 格式（iPhone 默认的高效图片格式，浏览器无法原生解码）
function isHeicImage(file) {
  const type = (file.type || '').toLowerCase()
  if (type === 'image/heic' || type === 'image/heif') return true
  if (/\.(heic|heif)$/i.test(file.name)) return true
  return false
}

// 动态加载 heic2any 并将 HEIC/HEIF Blob 转成 JPEG Blob。
// 使用动态 import 是为了不把 heic2any（~800KB）打进主包，仅当用户真的上传 HEIC 时才按需加载。
async function heicBlobToJpegBlob(file) {
  let heic2any
  try {
    const mod = await import('heic2any')
    heic2any = mod.default || mod
  } catch (e) {
    throw new Error('加载 HEIC 解码器失败，请检查网络后重试')
  }
  try {
    const result = await heic2any({ blob: file, toType: 'image/jpeg', quality: 0.9 })
    // heic2any 多图时可能返回数组，这里只取第一张
    const blob = Array.isArray(result) ? result[0] : result
    // 保留原文件名但改为 .jpg 后缀，便于后续流程展示
    const jpegName = file.name.replace(/\.(heic|heif)$/i, '') + '.jpg'
    return new File([blob], jpegName, { type: 'image/jpeg', lastModified: Date.now() })
  } catch (e) {
    throw new Error(`HEIC 解码失败：${file.name}（文件可能已损坏或非标准 HEIC 格式）`)
  }
}

// 多图合一逐图旋转（issue #87）：左旋/右旋 90°，归一化到 0/90/180/270。
// 旋转后旧的合并 PDF 失效，清掉 pdfBlob 强制重新转换，避免打印到未旋转的旧 PDF。
function rotateImage(idx, delta) {
  const cur = imageRotations.value[idx] || 0
  let next = (cur + delta) % 360
  if (next < 0) next += 360
  imageRotations.value[idx] = next
  pdfBlob.value = null
  converted.value = false
}

// 切换每页张数(issue #37):使上一次的 PDF 失效,让用户再点"合并"重新预览。
function setImagesPerPage(n) {
  imagesPerPage.value = n
  pdfBlob.value = null
  converted.value = false
}

function processMultipleImages(files) {
  clearFile()
  const arr = Array.from(files)
  selectedImages.value = arr
  imageRotations.value = arr.map(() => 0)
  fileDisplayName.value = `${arr.length}张图片`
  downloadName.value = '合并图片.pdf'
  converted.value = false

  // HEIC 无法直接生成缩略图，先用占位图，再异步转码后替换
  const hasHeic = arr.some(isHeicImage)
  imageThumbnails.value = arr.map(f => isHeicImage(f) ? '' : URL.createObjectURL(f))
  // 用第一张可用的缩略图作为预览；若没有则等待转码
  const firstReadyIdx = imageThumbnails.value.findIndex(u => !!u)
  if (firstReadyIdx >= 0) {
    previewUrl.value = imageThumbnails.value[firstReadyIdx]
    previewType.value = 'image'
  } else {
    previewType.value = 'text'
    textPreview.value = '正在解码 HEIC/HEIF 图片，请稍候…'
  }

  if (hasHeic) {
    const heicBatch = arr
    arr.forEach((f, idx) => {
      if (!isHeicImage(f)) return
      heicBlobToJpegBlob(f)
        .then(jpegFile => {
          // 若用户在转码期间已切换/清空文件，则丢弃结果
          if (selectedImages.value !== heicBatch) return
          selectedImages.value[idx] = jpegFile
          imageThumbnails.value[idx] = URL.createObjectURL(jpegFile)
          // 若之前没有可用预览，此时用第一张已就绪的
          if (!previewUrl.value || previewType.value !== 'image') {
            const firstIdx = imageThumbnails.value.findIndex(u => !!u)
            if (firstIdx >= 0) {
              previewUrl.value = imageThumbnails.value[firstIdx]
              previewType.value = 'image'
              textPreview.value = ''
            }
          }
        })
        .catch(err => {
          if (selectedImages.value !== heicBatch) return
          toast.add({
            title: `HEIC 解码失败：${f.name}`,
            description: err.message,
            color: 'error',
            icon: 'i-lucide-x-circle'
          })
        })
    })
  }
}

function removeImage(idx) {
  URL.revokeObjectURL(imageThumbnails.value[idx])
  selectedImages.value.splice(idx, 1)
  imageThumbnails.value.splice(idx, 1)
  imageRotations.value.splice(idx, 1)
  if (selectedImages.value.length === 1) {
    // 切换为单图片模式
    const f = selectedImages.value[0]
    selectedImages.value = []
    imageThumbnails.value.forEach(url => { try { URL.revokeObjectURL(url) } catch (_) {} })
    imageThumbnails.value = []
    fileDisplayName.value = ''
    processFile(f)
  } else if (selectedImages.value.length === 0) {
    clearFile()
  } else {
    fileDisplayName.value = `${selectedImages.value.length}张图片`
    // 更新预览为第一张（不调用 clearPreviewUrl，因为旧 previewUrl 与 imageThumbnails 共享同一 URL）
    previewUrl.value = imageThumbnails.value[0]
    converted.value = false
    pdfBlob.value = null
  }
}

function processBatchFiles(files) {
  clearFile()
  batchFiles.value = Array.from(files)
  fileDisplayName.value = `${batchFiles.value.length} 个文件（批量打印）`
  previewType.value = 'text'
  textPreview.value = `已选择 ${batchFiles.value.length} 个文件，点击"开始打印"将逐个打印。`
}

async function uploadAndPrintBatch() {
  if (!printer.value) { toast.add({ title: '请选择打印机', color: 'warning' }); return }
  if (batchFiles.value.length === 0) return

  // 计费 / 取件模式：一次批量下单，N 个文件共享一个批次。
  // 计费时是「一笔支付覆盖全部」——不逐个下单逐个付，那样用户得扫码 N 次，
  // 批量打印的意义就没了；取件时是「一批一起暂存」。
  // ⚠️ 取件模式**必须**也走这条分支：直接打 /api/print 会被 403
  // （见 printHandler 的闸门），而且那等于绕开了取件本身。
  if (billing.value.enabled || pull.value.enabled) {
    const form = new FormData()
    form.append('printer', printer.value)
    form.append('duplex', duplex.value === 'one-sided' ? 'false' : 'true')
    form.append('color', isColor.value ? 'true' : 'false')
    form.append('copies', String(copies.value))
    form.append('orientation', orientation.value)
    form.append('paper_size', paperSize.value)
    form.append('paper_type', paperType.value)
    if (mediaSource.value && mediaSource.value !== 'auto') form.append('media_source', mediaSource.value)
    form.append('print_scaling', printScalingParam.value)
    if (pageRange.value.trim()) form.append('page_range', pageRange.value.trim())
    if (pageSet.value && pageSet.value !== 'all') form.append('page_set', pageSet.value)
    if (mirror.value) form.append('mirror', 'true')
    if (watermarkText.value.trim()) form.append('watermark_text', watermarkText.value.trim())
    if (numberUp.value > 1) {
      form.append('number_up', String(numberUp.value))
      form.append('number_up_layout', numberUpLayout.value)
      form.append('page_border', pageBorder.value)
    }
    // 原样上传：转换交给服务端（免费路径才是前端先逐个转好的）。
    for (const f of batchFiles.value) {
      form.append('files', f, f.name)
    }
    if (pull.value.enabled) await submitPullOrder(form)
    else await submitOrder(form)
    return
  }

  batchPrinting.value = true
  batchProgress.value = { current: 0, total: batchFiles.value.length }
  let successCount = 0
  let failCount = 0

  for (let i = 0; i < batchFiles.value.length; i++) {
    batchProgress.value.current = i + 1
    const file = batchFiles.value[i]

    try {
      let fileToSend = file
      // 非 PDF 文件需要先转换
      if (file.type !== 'application/pdf') {
        const fd = new FormData()
        fd.append('file', file, file.name)
        fd.append('orientation', orientation.value)
        fd.append('paper_size', paperSize.value)
        if (invert.value) fd.append('invert', 'true')
        const convertResp = await apiFetch('/api/convert', { method: 'POST', body: fd }, () => emit('logout'))
        if (!convertResp.ok) {
          throw new Error(await readError(convertResp))
        }
        const blob = await convertResp.blob()
        fileToSend = new File([blob], file.name.replace(/\.[^/.]+$/, '') + '.pdf', { type: 'application/pdf' })
      }

      const form = new FormData()
      form.append('file', fileToSend, fileToSend.name)
      form.append('printer', printer.value)
      form.append('duplex', duplex.value === 'one-sided' ? 'false' : 'true')
      form.append('color', isColor.value ? 'true' : 'false')
      form.append('copies', String(copies.value))
      form.append('orientation', orientation.value)
      form.append('paper_size', paperSize.value)
      form.append('paper_type', paperType.value)
      if (mediaSource.value && mediaSource.value !== 'auto') form.append('media_source', mediaSource.value)
      form.append('print_scaling', printScalingParam.value)
      if (pageRange.value.trim()) form.append('page_range', pageRange.value.trim())
      if (pageSet.value && pageSet.value !== 'all') form.append('page_set', pageSet.value)
      if (mirror.value) form.append('mirror', 'true')
      if (watermarkText.value.trim()) form.append('watermark_text', watermarkText.value.trim())
      if (numberUp.value > 1) {
        form.append('number_up', String(numberUp.value))
        form.append('number_up_layout', numberUpLayout.value)
        form.append('page_border', pageBorder.value)
      }

      const resp = await apiFetch('/api/print', { method: 'POST', body: form }, () => emit('logout'))
      if (!resp.ok) throw new Error(await readError(resp))
      successCount++
    } catch (e) {
      failCount++
      toast.add({ title: `打印失败：${file.name}`, description: e.message, color: 'error', icon: 'i-lucide-x-circle' })
    }
  }

  if (successCount > 0) {
    toast.add({
      title: '批量打印完成',
      description: `成功 ${successCount} 个${failCount > 0 ? `，失败 ${failCount} 个` : ''}`,
      color: failCount > 0 ? 'warning' : 'success',
      icon: failCount > 0 ? 'i-lucide-alert-triangle' : 'i-lucide-check-circle'
    })
    localStorage.setItem('last_printer', printer.value)
    // 文件已经打成纸了，暂存的那份没用了 —— 存的是用户的原始文档，不该多留。
    clearPendingUpload()
  }
  batchPrinting.value = false
  batchProgress.value = { current: 0, total: 0 }
}

// 通过后端 /api/convert 将���或多张图片合成为单个 PDF。
// - 单图时传 `file` 字段；多图时传多个 `files` 字段，由后端 convertImagesMultiToPDF 合并。
// - HEIC 已由 processFile / processMultipleImages 提前���换为 JPEG，这里无需特殊处理。
// - 上传前用 downscaleImageIfNeeded 在浏览器端预压缩：长边 >3000px 的大图缩成 JPEG，
//   避免多张原图合并时撞到反向代理的 client_max_body_size 触发 413（Issue #42）。
//   阈值与后端 imageDownscaleMaxEdge 对齐，服务端拿到时已是合理尺寸，无需再 downscale。
//   sequential 而非 Promise.all 是为了避免移动端同时持有多张大 canvas 导致 OOM。
async function convertImagesToPdfViaServer(files, orient, pSize, name, rotations, invert) {
  const list = Array.isArray(files) ? files : [files]
  const downscaled = []
  for (const f of list) {
    downscaled.push(await downscaleImageIfNeeded(f))
  }
  const fd = new FormData()
  if (downscaled.length === 1) {
    fd.append('file', downscaled[0], downscaled[0].name)
  } else {
    for (const f of downscaled) fd.append('files', f, f.name)
    if (name) fd.append('name', name)
    // 多图合一逐图旋转（issue #87）：按顺序对应每个 files 字段
    if (rotations && rotations.length) {
      fd.append('rotations', rotations.map(r => r || 0).join(','))
    }
    // 每页张数(issue #37):>1 时后端按网格排版到同一页。
    if (imagesPerPage.value && imagesPerPage.value > 1) {
      fd.append('per_page', String(imagesPerPage.value))
    }
  }

  if (orient) fd.append('orientation', orient)
  if (pSize) fd.append('paper_size', pSize)
  if (invert) fd.append('invert', 'true')
  const resp = await apiFetch('/api/convert', { method: 'POST', body: fd }, () => emit('logout'))
  if (!resp.ok) throw new Error(await readError(resp))
  return resp.blob()
}

// 通过后端 /api/convert 将文本文件转成 PDF（使用后端内嵌中文字体，避免 jsPDF 中文乱码）。
async function convertTextViaServer(file, orient, pSize) {
  const fd = new FormData()
  fd.append('file', file, file.name)
  if (orient) fd.append('orientation', orient)
  if (pSize) fd.append('paper_size', pSize)
  const resp = await apiFetch('/api/convert', { method: 'POST', body: fd }, () => emit('logout'))
  if (!resp.ok) throw new Error(await readError(resp))
  return resp.blob()
}

// 通过后端 /api/convert 将 Office / OFD 文件转成 PDF。
async function convertOfficeToPdf(file) {
  const fd = new FormData()
  fd.append('file', file, file.name)
  const resp = await apiFetch('/api/convert', { method: 'POST', body: fd }, () => emit('logout'))
  if (!resp.ok) throw new Error(await readError(resp))
  return resp.blob()
}

async function convertToPdf() {
  if (!selectedFile.value && !isMultiImage.value) return
  converting.value = true
  try {
    const f = selectedFile.value
    let blob
    if (isMultiImage.value) {
      blob = await convertImagesToPdfViaServer(
        selectedImages.value, orientation.value, paperSize.value, downloadName.value || '合并图片.pdf',
        imageRotations.value, invert.value
      )
    } else if (isOfficeFile(f) || isOFDFile(f)) {
      blob = await convertOfficeToPdf(f)
    } else if (f.type.startsWith('image/')) {
      blob = await convertImagesToPdfViaServer([f], orientation.value, paperSize.value, null, null, invert.value)
    } else {
      blob = await convertTextViaServer(f, orientation.value, paperSize.value)
    }
    pdfBlob.value = blob
    clearPreviewUrl()
    previewUrl.value = URL.createObjectURL(blob)
    previewType.value = 'pdf'
    converted.value = true
    toast.add({ title: '转换成功', color: 'success', icon: 'i-lucide-check-circle' })
  } catch (e) {
    toast.add({ title: '转换失败', description: e.message, color: 'error', icon: 'i-lucide-x-circle' })
  } finally {
    converting.value = false
  }
}

// 对当前 PDF 显式应用后端 gs 规范化。
// 后端 /api/convert?normalize=true 会把 PDF 重写为 1.4 版本并嵌入所有字体，
// 用于修复 CJK 字体外挂 CMap 导致的乱码等问题。规范化结果替换 pdfBlob 与
// previewUrl，后续打印发的就是这份字节流。
async function applyGsNormalization() {
  const f = selectedFile.value
  if (!f || f.type !== 'application/pdf') return
  if (gsApplying.value || gsApplied.value) return
  gsApplying.value = true
  try {
    const fd = new FormData()
    fd.append('file', f, f.name)
    fd.append('normalize', 'true')
    const resp = await apiFetch('/api/convert', { method: 'POST', body: fd }, () => emit('logout'))
    if (!resp.ok) throw new Error(await readError(resp))
    const blob = await resp.blob()
    // 用户在期间换/清了文件，丢弃结果
    if (selectedFile.value !== f) return
    clearPreviewUrl()
    previewUrl.value = URL.createObjectURL(blob)
    previewType.value = 'pdf'
    textPreview.value = ''
    pdfBlob.value = blob
    gsApplied.value = true
    toast.add({ title: '已应用 GS 规范化', color: 'success', icon: 'i-lucide-check-circle' })
  } catch (e) {
    toast.add({ title: '应用 GS 失败', description: e.message, color: 'error', icon: 'i-lucide-x-circle' })
  } finally {
    gsApplying.value = false
  }
}

// ─── 打印 ─────────────────────────────────────────────────
async function uploadAndPrint() {
  if (!printer.value) { toast.add({ title: '请选择打印机', color: 'warning' }); return }

  // 批量打印模式
  if (batchFiles.value.length > 0) {
    await uploadAndPrintBatch()
    return
  }

  const fileToSend = pdfBlob.value || selectedFile.value
  if (!fileToSend && !isMultiImage.value) { toast.add({ title: '请先选择文件', color: 'warning' }); return }
  // 多图片未转换时自动转换
  if (isMultiImage.value && !pdfBlob.value) {
    await convertToPdf()
    if (!pdfBlob.value) return
  }
  const actualFile = pdfBlob.value || selectedFile.value
  const filename = pdfBlob.value
    ? (downloadName.value || 'document.pdf')
    : (selectedFile.value ? selectedFile.value.name : 'document')

  const form = new FormData()
  form.append('file', actualFile, filename)
  form.append('printer', printer.value)
  form.append('duplex', duplex.value === 'one-sided' ? 'false' : 'true')
  form.append('color', isColor.value ? 'true' : 'false')
  form.append('copies', String(copies.value))
  form.append('orientation', orientation.value)
  form.append('paper_size', paperSize.value)
  form.append('paper_type', paperType.value)
  if (mediaSource.value && mediaSource.value !== 'auto') form.append('media_source', mediaSource.value)
  form.append('print_scaling', printScalingParam.value)
  if (pageRange.value.trim()) form.append('page_range', pageRange.value.trim())
  if (pageSet.value && pageSet.value !== 'all') form.append('page_set', pageSet.value)
  if (mirror.value) form.append('mirror', 'true')
  // 单图图片直接打印（未先转 PDF）时把黑白反转带给后端；已转成 PDF 的不传（反色已并入）
  if (invert.value && !pdfBlob.value && isImageKind.value) form.append('invert', 'true')
  if (watermarkText.value.trim()) form.append('watermark_text', watermarkText.value.trim())
  if (numberUp.value > 1) {
    form.append('number_up', String(numberUp.value))
    form.append('number_up_layout', numberUpLayout.value)
    form.append('page_border', pageBorder.value)
  }

  // 计费开启时改走「下单 → 支付 → 送印」，取件模式走「提交 → 暂存 → 刷卡送印」。
  // 两者都不开的部署完全走原路径，一个字节的额外请求都不产生。
  if (billing.value.enabled) {
    await submitOrder(form)
    return
  }
  if (pull.value.enabled) {
    await submitPullOrder(form)
    return
  }

  printing.value = true
  try {
    const resp = await apiFetch('/api/print', {
      method: 'POST',
      body: form
    }, () => emit('logout'))
    if (!resp.ok) {
      throw new Error(await readError(resp))
    }
    const j = await readJSON(resp)
    toast.add({
      title: '打印任务已提交',
      description: `任务ID：${j.jobId || '—'}，共 ${j.pages} 页`,
      color: 'success',
      icon: 'i-lucide-check-circle'
    })
    localStorage.setItem('last_printer', printer.value)
    // 同上：打印成功即清掉暂存。
    clearPendingUpload()
  } catch (e) {
    toast.add({ title: '打印失败', description: e.message, color: 'error', icon: 'i-lucide-x-circle' })
  } finally {
    printing.value = false
  }
}

// 打印记录与重打已移到个人中心的「打印记录」页（views/RecordsView.vue）：
// 记录属于「回头再看」，在打印页上占着一大块，手机上一次打印要滚很久才能翻过去。
// ─── 打印机状态 ───────────────────────────────────────────
async function loadPrinterInfo(silent = false) {
  if (!printer.value) return
  if (!silent) loadingPrinterInfo.value = true
  printerInfoError.value = ''
  try {
    const resp = await apiFetch(
      `/api/printer-info?uri=${encodeURIComponent(printer.value)}`,
      {},
      () => emit('logout')
    )
    if (resp.ok) {
      printerInfo.value = await readJSON(resp)
      // 若当前所选纸盒不在这台打印机的可用列表里，回退到「自动」
      const trays = printerInfo.value?.mediaSourceSupported || []
      if (mediaSource.value !== 'auto' && !trays.includes(mediaSource.value)) {
        mediaSource.value = 'auto'
      }
    } else if (resp.status !== 401) {
      printerInfoError.value = await readError(resp)
    }
  } catch (e) {
    // ⚠️ 这里是【请求服务端接口】失败（网络断了、请求被中断），
    // 不是「打印机连不上」。原先写「无法连接到打印机」会把排查方向从
    // 「服务/网络」直接带到打印机硬件上 —— 而打印机到底通不通，是后端
    // 直连探测的结论，由 deviceState 单独呈现。
    printerInfoError.value = '读取打印机状态失败：' + (e?.message || e)
  } finally {
    loadingPrinterInfo.value = false
  }
}

function onPrinterChange() {
  printerInfo.value = null
  printerInfoError.value = ''
  mediaSource.value = 'auto'
  loadPrinterInfo()
}

// 加载打印机列表(issue #50)。silent=true 时不弹 toast,专供 refreshAll 使用;
// 若当前选中的打印机在最新列表里仍然存在就保留,否则回退到列表第一台。
async function loadPrinters(silent = false) {
  try {
    const resp = await apiFetch('/api/printers', {}, () => emit('logout'))
    if (!resp.ok) {
      if (!silent && resp.status !== 401) {
        toast.add({ title: '加载打印机失败', description: await readError(resp), color: 'error' })
      }
      return
    }
    const list = (await readJSON(resp)) || []
    printers.value = list
    if (list.length === 0) {
      printer.value = ''
      return
    }
    // 首次进入:优先按 localStorage 恢复上次选择;refreshAll 场景下:若当前选中的
    // 打印机仍在列表里就保留(避免刷新时把用户已选的下拉重置)。
    const current = printer.value
    if (current && list.some(p => p.uri === current)) {
      return
    }
    const last = localStorage.getItem('last_printer')
    if (last && list.some(p => p.uri === last)) {
      printer.value = last
    } else {
      printer.value = list[0].uri
    }
  } catch (e) {
    if (!silent) {
      toast.add({ title: '加载打印机失败', description: e.message, color: 'error' })
    }
  }
}

async function refreshAll() {
  refreshing.value = true
  // 打印机列表也一并刷新(issue #50):打印机被拔走再插回、或在 CUPS 631 页面
  // 重新添加队列后,用户无需 F5 或退出重进,点一下"刷新"就能重新拉到最新列表。
  // 先拉打印机列表 —— 当前选中的可能已经消失,loadPrinters 会重新选一台,
  // 之后再去查这台的详情/记录才有意义。
  await loadPrinters(true)
  await loadPrinterInfo(true)
  refreshing.value = false
}

// ─── 打印模式 ─────────────────────────────────────────────
// applyModeDefaults 每个模式自带的参数默认值。
//
// ⚠️ 单独成函数是因为【恢复重载会话】也要用它：那条路径不走 switchMode
// （switchMode 会先 clearFile/clearModeState 清空状态，而恢复恰恰是想要
// 保留），但模式的默认参数一样得生效 —— 少了它，恢复出来的身份证模式会
// 带着 printScaling='auto' 而不是 'none'，出来的东西和用户当时看到的不一样。
function applyModeDefaults(mode) {
  scalePercent.value = 100
  if (mode === 'invoice') {
    isColor.value = false
    printScaling.value = 'none'
  } else if (mode === 'id_card') {
    isColor.value = true
    printScaling.value = 'none'
    paperSize.value = 'A4'
  } else {
    isColor.value = true
    printScaling.value = 'auto'
  }
}

function switchMode(mode) {
  if (printMode.value === mode) return
  clearFile()
  clearModeState()
  printMode.value = mode
  localStorage.setItem('print_mode', mode)
  applyModeDefaults(mode)
}

function setIdCardPaper(p) {
  idCardPaper.value = p
  paperSize.value = p
}

function clearModeState() {
  invoiceFiles.value = []
  if (idCardFrontPreview.value) { try { URL.revokeObjectURL(idCardFrontPreview.value) } catch (_) {} }
  if (idCardBackPreview.value) { try { URL.revokeObjectURL(idCardBackPreview.value) } catch (_) {} }
  idCardFront.value = null
  idCardBack.value = null
  idCardFrontPreview.value = ''
  idCardBackPreview.value = ''
  idCardPaper.value = 'A4'
  composing.value = false
  quickText.value = ''
}

// 文字速印(issue #70):把 textarea 里的字符串包成一个 text/plain 的 File,
// 直接喂给现有 processFile → 走标准的 convert→preview→uploadAndPrint 流程,
// 后端的 fileKindText / convertTextToPDF 已经能处理这类输入。
async function convertQuickText() {
  const text = quickText.value
  if (!text.trim()) return
  const filename = `quick-print-${Date.now()}.txt`
  const f = new File([text], filename, { type: 'text/plain' })
  processFile(f)
  // processFile 内部把 selectedFile 设好之后,再触发一次 PDF 转换预览。
  await convertToPdf()
}

function onInvoiceDrop(e) {
  invoiceDragging.value = false
  addInvoiceFiles(e.dataTransfer.files)
}

function onInvoiceFileChange(e) {
  addInvoiceFiles(e.target.files)
  if (invoiceInput.value) invoiceInput.value.value = ''
}

function addInvoiceFiles(files) {
  if (!files || files.length === 0) return
  invoiceFiles.value = [...invoiceFiles.value, ...Array.from(files)]
  // 清除之前的合并结果
  clearPreviewUrl()
  pdfBlob.value = null
  converted.value = false
  previewType.value = ''
}

function removeInvoiceFile(idx) {
  invoiceFiles.value.splice(idx, 1)
  clearPreviewUrl()
  pdfBlob.value = null
  converted.value = false
  previewType.value = ''
}

function onIdCardFront(file) {
  if (idCardFrontPreview.value) { try { URL.revokeObjectURL(idCardFrontPreview.value) } catch (_) {} }
  idCardFront.value = file
  idCardFrontPreview.value = file ? URL.createObjectURL(file) : ''
  clearPreviewUrl()
  pdfBlob.value = null
  converted.value = false
  previewType.value = ''
}

function onIdCardBack(file) {
  if (idCardBackPreview.value) { try { URL.revokeObjectURL(idCardBackPreview.value) } catch (_) {} }
  idCardBack.value = file
  idCardBackPreview.value = file ? URL.createObjectURL(file) : ''
  clearPreviewUrl()
  pdfBlob.value = null
  converted.value = false
  previewType.value = ''
}

async function composeAndPreview() {
  composing.value = true
  try {
    const fd = new FormData()
    if (printMode.value === 'invoice') {
      fd.append('mode', 'invoice')
      for (const f of invoiceFiles.value) {
        fd.append('files', f, f.name)
      }
    } else if (printMode.value === 'id_card') {
      fd.append('mode', 'id_card')
      fd.append('paper', idCardPaper.value)
      fd.append('files', idCardFront.value, idCardFront.value.name)
      fd.append('files', idCardBack.value, idCardBack.value.name)
    }

    const resp = await apiFetch('/api/compose', { method: 'POST', body: fd }, () => emit('logout'))
    if (!resp.ok) {
      throw new Error(await readError(resp))
    }
    const blob = await resp.blob()
    clearPreviewUrl()
    pdfBlob.value = blob
    previewUrl.value = URL.createObjectURL(blob)
    previewType.value = 'pdf'
    converted.value = true
    downloadName.value = printMode.value === 'invoice' ? '发票合并.pdf' : '身份证.pdf'
    toast.add({ title: '合并成功', color: 'success', icon: 'i-lucide-check-circle' })
  } catch (e) {
    toast.add({ title: '合并失败', description: e.message, color: 'error', icon: 'i-lucide-x-circle' })
  } finally {
    composing.value = false
  }
}

// ─── 定时器 ───────────────────────────────────────────────
let printerInfoTimer = null

// ─── 生命周期 ─────────────────────────────────────────────
// ─── 计费 ───────────────────────────────────────────────
// 计费开启时，「开始打印」不再是直接打印，而是创建订单 → 支付 → 送印。
// prices/wallet 只在计费开启时才拉取，免费部署完全不产生额外请求。
const billing = ref({ enabled: false, currency: 'CNY', fallbackUnitCents: 0, rules: [] })
// 刷卡取件模式。与计费【互斥】，两个不会同时为真。
// 它决定提交走哪条路（/api/pull-orders），以及提交成功后说什么话。
const pull = ref({ enabled: false, ttlHours: 72 })
// 价目/余额没拉到时的原因。非空时价格条改显示这句话，不显示金额 ——
// 详见 PriceSummary 的 error prop 注释。
const billingError = ref('')
const wallet = ref({ balanceCents: 0 })
const docPages = ref(1)
const pagesEstimated = ref(false)
const orderBatch = ref(null)
const showPaymentModal = ref(false)

async function loadBilling() {
  billingError.value = ''
  try {
    const pub = await fetch('/api/public-settings', { credentials: 'include' })
    if (pub.ok) {
      const s = await readJSON(pub)
      billing.value.enabled = s.billingEnabled === true
      pull.value.enabled = s.pullModeEnabled === true
      pull.value.ttlHours = s.pullTtlHours || 72
    }
    // 取件模式不需要价目和余额，拉到模式开关就够了。
    if (pull.value.enabled) return
    if (!billing.value.enabled) return

    const [pricesResp, walletResp] = await Promise.all([
      apiFetch('/api/prices', {}, () => emit('logout')),
      apiFetch('/api/wallet', {}, () => emit('logout'))
    ])
    if (pricesResp.ok) {
      const data = await readJSON(pricesResp)
      billing.value = { ...data, enabled: true }
    } else {
      // ⚠️ 价目没拉到就【不能】照常渲染价格条：billing.rules 还是空的，
      // 算出来是 0 元，界面上会显示「该纸张组合尚未配置价目，将按 0 元计费。
      // 请联系管理员配置。」—— 把接口故障说成管理员没配价目，让人白翻一遍
      // 价目表。改成把真实原因交给 PriceSummary（它的 error 分支本来就在那儿）。
      billingError.value = '价目加载失败，暂时无法估价。请点右上角刷新重试。'
    }
    if (walletResp.ok) {
      wallet.value = await readJSON(walletResp)
    } else if (!billingError.value) {
      // 余额拉不到时同样不能假装是 0：价格条会显示 ¥0.00 并弹出「去充值」，
      // 用户会以为自己余额不足。
      billingError.value = '余额加载失败，暂时无法估价。请点右上角刷新重试。'
    }
  } catch {
    // 计费信息拉取失败就退回免费模式，让用户至少还能用基本功能。
    billing.value.enabled = false
  }
}

// estimateDocPages 预估待提交文件的页数，用于在提交前把价格算给用户看。
//
// 提交的可能是转换后的 pdfBlob（多图合并 / 发票 / 身份证模式），
// 所以预估的必须是【即将提交的那个文件】，否则页数会差得离谱。
async function estimateDocPages() {
  if (!billing.value.enabled) return

  // 多图【未合并】时 pdfBlob 与 selectedFile 都是空的（多图存在 selectedImages 里），
  // 走下面的分支会拿不到文件、页数停在默认值 1 —— 而提交时 uploadAndPrint 会
  // 先自动合并再上传，后端按 N 页扣款。于是「显示 1 页的钱、实扣 N 页的钱」。
  //
  // 这里不必上传：每张图会变成 PDF 的一页（后端 convertImagesMultiToPDF 在
  // per_page<=1 时一张一页），设了「每页张数」则按网格向上取整。
  if (isMultiImage.value && !pdfBlob.value) {
    docPages.value = estimateImageDocPages(selectedImages.value.length, imagesPerPage.value)
    pagesEstimated.value = false
    return
  }

  const f = pdfBlob.value || selectedFile.value
  if (!f) {
    docPages.value = 1
    pagesEstimated.value = false
    return
  }
  try {
    const fd = new FormData()
    fd.append('file', f, f.name || 'document')
    const resp = await apiFetch('/api/estimate', { method: 'POST', body: fd }, () => emit('logout'))
    if (!resp.ok) {
      // ⚠️ 不能就这么静默 return。
      //
      // 预估失败时 docPages 会停在【上一个文件】的页数（或默认的 1），而价格条
      // 照样按那个数字算钱给用户看 —— 一个看不出错的错价。
      // 置上 pagesEstimated，价格条就会显示它本来就有的那句
      // 「页数为预估值，最终金额以提交后服务端确认为准」，用户至少知道别信这个数。
      //
      // 原因只写控制台：真正的错误（比如「服务器未安装 LibreOffice」）在下单时
      // 后端还会再报一次，那才是必须让用户看见的时机 —— 这里再弹一遍是重复打扰。
      console.warn('预估页数失败：', await readError(resp))
      pagesEstimated.value = true
      return
    }
    const j = await readJSON(resp)
    docPages.value = Math.max(1, j.pages || 1)
    pagesEstimated.value = j.estimated === true
  } catch {
    // 预估失败不影响提交 —— 服务端下单时会算出真实价格，以那个为准。
  }
}

// 任何会改变「待打印文件」或页数的操作都要重新预估。
// selectedImages 必须一起 watch：多图增删、以及「每页张数」变化都会改页数。
watch([pdfBlob, selectedFile, selectedImages, imagesPerPage], () => { estimateDocPages() })

async function submitOrder(form) {
  printing.value = true
  try {
    const resp = await apiFetch('/api/orders', { method: 'POST', body: form }, () => emit('logout'))
    if (!resp.ok) {
      throw new Error(await readError(resp))
    }
    orderBatch.value = await readJSON(resp)
    showPaymentModal.value = true
  } catch (e) {
    toast.add({ title: '下单失败', description: e.message, color: 'error', icon: 'i-lucide-x-circle' })
  } finally {
    printing.value = false
  }
}

// submitPullOrder 提交刷卡取件的任务：**只暂存，不打印**。
//
// ⚠️ 成功文案必须是「已暂存，请到打印机刷卡取件」。
// 这里原先（计费/免费两条路）说的是「打印任务已提交」——在取件模式下那句话
// 是**谎言**：纸一张都不会出来，员工看到「已提交」转身就走，到了打印机那儿
// 才发现什么都没有。整个功能的成败基本全押在这一句提示上。
async function submitPullOrder(form) {
  printing.value = true
  try {
    const resp = await apiFetch('/api/pull-orders', { method: 'POST', body: form }, () => emit('logout'))
    if (!resp.ok) {
      throw new Error(await readError(resp))
    }
    const data = await readJSON(resp)
    const okCount = data.orders?.length || 0
    const failed = data.failed || []

    // 部分失败要说清楚 —— 只说「已提交」会让员工以为全都进去了，
    // 结果到了打印机少打一份，还得回来重来一遍。
    const description = failed.length
      ? `${okCount} 个已提交，${failed.length} 个失败：${failed[0].error}`
      : `${okCount} 个任务已暂存，请在 ${pull.value.ttlHours} 小时内到打印机刷工牌取件`

    toast.add({
      title: '已暂存，请到打印机刷卡取件',
      description,
      color: failed.length ? 'warning' : 'success',
      icon: 'i-lucide-scan-line',
      duration: 8000
    })
    localStorage.setItem('last_printer', printer.value)
    // 与计费模式一致：文件已经离开用户手上、交给服务器了，清掉本地暂存。
    clearPendingUpload()
    // 跳中间页，把「去哪台打印机、什么时候之前取」讲清楚。
    // 🚫 不要跳 /done —— 那个页面讲的是「钱付了、任务在打印」，
    // 而此刻任务还躺在待取件里，跳过去只会传达完全相反的信息。
    // 批次号从响应里带过去，不用「最近一条」去猜（多设备时必然猜错）。
    if (data.batchId) {
      router.push({ path: '/pending', query: { batch: data.batchId } })
    }
  } catch (e) {
    toast.add({ title: '提交失败', description: e.message, color: 'error', icon: 'i-lucide-x-circle' })
  } finally {
    printing.value = false
  }
}

async function onPaid() {
  await loadWallet()
  localStorage.setItem('last_printer', printer.value)
  // 计费模式下，付款成功才算这份文档真的被消费掉了（订单已送印）。
  clearPendingUpload()
}

async function loadWallet() {
  try {
    const resp = await apiFetch('/api/wallet', {}, () => emit('logout'))
    if (resp.ok) {
      wallet.value = await readJSON(resp)
      // 拉到了就把之前的「余额加载失败」撤掉，否则用户刷新成功之后
      // 价格条还挂着一句已经不成立的提示。
      if (billingError.value) billingError.value = ''
    }
  } catch { /* 钱包刷新失败不影响主流程 —— 但价格条上的余额会停在上一次的值，
               这个取舍是有意的：为了看一眼余额而打断正在进行的打印不值得。 */ }
}

function goWallet() {
  showPaymentModal.value = false
  emit('go-wallet')
}

// 支付成功页结束后，去中间页收尾。
//
// 付完钱用户最想知道的是「我的任务在哪」。停在打印页的话，弹窗一关这件事
// 在界面上就没有任何痕迹了，只能自己摸到「个人中心 → 打印记录」。
//
// 批次号此刻还在内存里（orderBatch），直接带过去 —— 不用「最近一条订单」
// 去猜，多设备或连着打几次时会猜错。
function goPrintResult() {
  const b = orderBatch.value?.batchId
  router.push(b ? { path: '/done', query: { batch: b } } : '/done')
}

// 回跳结算完成后重拉余额：价格条上的「当前余额」也得跟着变，
// 否则用户付完款回来看到余额还是付款前的数字。
watch(paymentSettledTick, () => { loadWallet() })

// 记住「进到哪个模式、调了哪些参数」，供重载后恢复。
// 都是小数据，直接在每次变化时同步写 —— sessionStorage 是同步 API，
// 一次写几 KB 的开销可以忽略。
//
// ⚠️ 位置很讲究：watch 的【源数组立即求值】，下面这些 ref 里 quickText
// 声明在文件中部。这条 watch 原本注册在文件前段，直接踩了暂时性死区
// （`Cannot access 'quickText' before initialization`），整页白屏，而构建与
// 静态检查全绿。凡是要把一批 ref 写进 watch 源数组的，都必须放到这些 ref
// 全部声明之后 —— 也就是这里。
watch([
  entered, printMode, quickText,
  isColor, duplex, orientation, copies, paperSize, paperType, mediaSource,
  printScaling, scalePercent, pageRange, pageSet, mirror, watermarkText,
  numberUp, numberUpLayout, pageBorder
], saveSession)

// 文件集合一变就同步暂存。
//
// ⚠️ 用一条 watcher 覆盖全部入口（选文件 / 多图增删 / 发票增删 / 身份证 /
// 批量 / 文字 / 换模式），而不是往 processFile、removeImage、
// removeInvoiceFile… 里逐个插调用 —— 那样迟早漏一个，而漏掉的表现是
// 「某种方式选的文件丢了」，极难复现。
//
// deep 在这里是安全的：Vue 不会把 File/Blob 变成响应式代理（只代理普通对象
// 与数组），遍历它们没有自有可枚举属性，代价可忽略。
watch(
  [selectedFile, selectedImages, invoiceFiles, idCardFront, idCardBack, batchFiles, quickText, printMode],
  syncPendingUpload,
  { deep: true }
)

// ⚠️ 这里才恢复会话 —— 必须在【所有 ref 声明之后】。
//
// restoreAfterReload 会写 quickText、printMode 和一堆打印参数，其中 quickText
// 声明在文件中部（打印模式那一段之后）。放在 setup 偏前的位置调用会踩
// 暂时性死区，抛 `Cannot access 'quickText' before initialization`，整页白屏，
// 而构建、静态检查全绿 —— 只有浏览器控制台能看到。
//
// 放在这里仍然早于首次渲染（setup 是同步跑完才渲染的），所以不会闪一下落地页。
if (restoreAfterReload()) {
  entered.value = true
  restoredFromReload.value = true
}

onMounted(async () => {
  await loadBilling()
  await loadPrinters()
  if (printer.value) loadPrinterInfo()

  printerInfoTimer = setInterval(() => loadPrinterInfo(true), 15000)

  // 放在最后：先把打印机列表这些基础数据准备好，再把文件接回去 ——
  // 文件一进去就会触发页数预估和打印机查询。
  restorePendingUpload()
})

onUnmounted(() => {
  clearInterval(printerInfoTimer)
  if (jobsTimer) clearInterval(jobsTimer)
  clearFile()
  clearModeState()
})
</script>
