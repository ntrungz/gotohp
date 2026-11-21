<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import {
  ArrowLeft,
  CheckCircle2,
  Copy,
  HardDriveDownload,
  Info,
  Loader2,
  Play,
  RefreshCw,
  Trash2,
  X,
  AlertTriangle,
} from '@lucide/vue'
import Button from './components/ui/button/Button.vue'
import { Input } from './components/ui/input'
import { Label } from './components/ui/label'
import { Progress } from './components/ui/progress'
import { ScrollArea } from './components/ui/scroll-area'
import { Switch } from './components/ui/switch'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from './components/ui/tooltip'
import {
  NumberField,
  NumberFieldContent,
  NumberFieldDecrement,
  NumberFieldIncrement,
  NumberFieldInput,
} from './components/ui/number-field'
import { reclaimManager } from './utils/ReclaimManager'

const props = defineProps<{
  account: string
}>()

const emit = defineEmits<{
  close: []
}>()

const { state } = reclaimManager

const downloadDir = ref('')
const deletePermanently = ref(false)
const cleanupLocal = ref(false)
const saver = ref(false)
const restoreMetadata = ref(true)
const threads = ref(1)
const limit = ref(0)
const copyLabel = ref('Copy JSON')

watch(
  () => state.downloadDir,
  (dir) => {
    if (dir && !downloadDir.value) {
      downloadDir.value = dir
    }
  },
  { immediate: true },
)

const isBusy = computed(() => state.phase === 'scanning' || state.phase === 'running')

const progressPercent = computed(() => {
  if (state.total === 0) return 0
  return Math.round((state.processed / state.total) * 100)
})

const workersList = computed(() => {
  return Array.from(state.workers.values()).sort((a, b) => a.workerId - b.workerId)
})

const effectiveCount = computed(() => {
  if (limit.value > 0 && limit.value < state.items.length) {
    return limit.value
  }
  return state.items.length
})

const effectiveQuotaBytes = computed(() => {
  return state.items
    .slice(0, effectiveCount.value)
    .reduce((sum, item) => sum + item.quotaChargedBytes, 0)
})

function formatBytes(bytes: number): string {
  if (!bytes || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let val = bytes
  let idx = 0
  while (val >= 1024 && idx < units.length - 1) {
    val /= 1024
    idx += 1
  }
  return `${val.toFixed(1)} ${units[idx]}`
}

function stageLabel(stage: string): string {
  switch (stage) {
    case 'downloading':
      return 'Downloading'
    case 'trashing':
      return 'Moving to Trash'
    case 'deleting':
      return 'Deleting from Trash'
    case 'uploading':
      return 'Re-uploading (0 quota)'
    case 'restoring_metadata':
      return 'Restoring metadata'
    case 'restoring_trash':
      return 'Restoring from Trash'
    case 'cleaning_up':
      return 'Cleaning local file'
    case 'completed':
      return 'Completed'
    case 'error':
      return 'Error'
    default:
      return stage
  }
}

function startScan() {
  reclaimManager.startScan(props.account)
}

function startReclaim() {
  if (state.items.length === 0) return
  reclaimManager.startReclaim({
    items: state.items,
    downloadDir: downloadDir.value.trim(),
    deletePermanently: deletePermanently.value,
    cleanupLocal: cleanupLocal.value,
    saver: saver.value,
    restoreMetadata: restoreMetadata.value,
    threads: Math.max(1, threads.value),
    limit: Math.max(0, limit.value),
  })
}

async function copyJson() {
  const ok = await reclaimManager.copyResultsAsJson()
  if (ok) {
    copyLabel.value = 'Copied!'
    setTimeout(() => {
      copyLabel.value = 'Copy JSON'
    }, 1200)
  }
}

function handleBack() {
  if (isBusy.value) return
  emit('close')
}
</script>

<template>
  <TooltipProvider disable-hoverable-content>
    <div
      class="flex flex-col h-full w-full px-4 pt-6 pb-4 gap-3"
      style="--wails-draggable: none"
    >
      <!-- Header -->
      <div class="flex items-center justify-between">
        <Button
          variant="ghost"
          size="sm"
          class="cursor-pointer px-2"
          :disabled="isBusy"
          @click="handleBack"
        >
          <ArrowLeft
            :size="15"
            class="mr-1"
          />
          Back
        </Button>
        <h1 class="text-base font-semibold select-none">
          Reclaim Quota
        </h1>
        <div class="w-14" />
      </div>

      <!-- Idle: Explain workflow & Scan button -->
      <div
        v-if="state.phase === 'idle'"
        class="flex-1 flex flex-col items-center justify-center text-center gap-4 px-2"
      >
        <div class="flex size-12 items-center justify-center rounded-full bg-muted text-muted-foreground">
          <HardDriveDownload :size="22" />
        </div>
        <div class="space-y-1.5">
          <h2 class="text-base font-semibold">
            Find Quota-Consuming Media
          </h2>
          <p class="text-xs text-muted-foreground leading-relaxed">
            Scan your Google Photos library for photos &amp; videos using storage quota,
            download originals locally, move them to Trash, and re-upload without quota.
          </p>
        </div>
        <p
          v-if="state.scanError"
          class="text-xs text-destructive bg-destructive/10 rounded-md px-3 py-2 w-full"
        >
          {{ state.scanError }}
        </p>
        <Button
          class="cursor-pointer w-full max-w-xs"
          @click="startScan"
        >
          <RefreshCw
            :size="14"
            class="mr-1.5"
          />
          Scan Library
        </Button>
      </div>

      <!-- Scanning -->
      <div
        v-else-if="state.phase === 'scanning'"
        class="flex-1 flex flex-col items-center justify-center gap-4 px-2"
      >
        <Loader2
          :size="28"
          class="animate-spin text-primary"
        />
        <div class="text-center space-y-1">
          <p class="text-sm font-medium">
            Scanning Google Photos library...
          </p>
          <p class="text-xs text-muted-foreground tabular-nums">
            Pages: {{ state.scanStats.pagesScanned }} · Checked: {{ state.scanStats.totalScanned }}
          </p>
          <p class="text-xs text-foreground tabular-nums">
            Quota items: {{ state.scanStats.eligibleCount }} ({{ formatBytes(state.scanStats.totalQuotaBytes) }})
          </p>
        </div>
        <Button
          variant="destructive"
          size="sm"
          class="cursor-pointer w-full max-w-xs mt-2"
          @click="reclaimManager.cancel()"
        >
          <X
            :size="14"
            class="mr-1"
          />
          Cancel Scan
        </Button>
      </div>

      <!-- Reviewed: Show findings & options -->
      <template v-else-if="state.phase === 'reviewed'">
        <div class="rounded-lg border bg-muted/30 p-3 space-y-1">
          <div class="flex items-center justify-between">
            <span class="text-xs text-muted-foreground">Quota items found</span>
            <span class="text-sm font-semibold tabular-nums">
              {{ state.items.length }} ({{ formatBytes(state.scanStats.totalQuotaBytes) }})
            </span>
          </div>
          <div class="text-[11px] text-muted-foreground flex flex-wrap gap-x-2">
            <span>Scanned: {{ state.scanStats.totalScanned }}</span>
            <span v-if="state.scanStats.skippedTrashed">· In Trash: {{ state.scanStats.skippedTrashed }}</span>
            <span v-if="state.scanStats.skippedLocked">· Locked: {{ state.scanStats.skippedLocked }}</span>
            <span v-if="state.scanStats.skippedNotOwned">· Shared: {{ state.scanStats.skippedNotOwned }}</span>
          </div>
        </div>

        <div
          v-if="state.items.length === 0"
          class="flex-1 flex flex-col items-center justify-center text-center gap-3"
        >
          <CheckCircle2
            :size="32"
            class="text-emerald-500"
          />
          <p class="text-sm font-medium">
            No quota-consuming items found!
          </p>
          <Button
            variant="outline"
            size="sm"
            class="cursor-pointer"
            @click="startScan"
          >
            <RefreshCw
              :size="13"
              class="mr-1.5"
            />
            Scan Again
          </Button>
        </div>

        <template v-else>
          <!-- Scrollable options & preview -->
          <ScrollArea class="flex-1 min-h-0 pr-2">
            <div class="space-y-3 pb-2">
              <!-- Backup Folder -->
              <div class="space-y-1">
                <Label
                  for="reclaim-dir"
                  class="text-xs text-muted-foreground"
                >
                  Local backup folder
                </Label>
                <Input
                  id="reclaim-dir"
                  v-model="downloadDir"
                  placeholder="Default ~/gotohp-reclaim"
                  class="h-8 text-xs"
                />
              </div>

              <!-- Options -->
              <div class="space-y-2 rounded-lg border p-2.5">
                <div class="flex items-center justify-between gap-2">
                  <div class="flex items-center gap-1 min-w-0">
                    <Label
                      for="reclaim-delete-perm"
                      class="text-xs cursor-pointer"
                    >
                      Permanently delete from Trash
                    </Label>
                    <Tooltip>
                      <TooltipTrigger as-child>
                        <button
                          type="button"
                          class="text-muted-foreground hover:text-foreground"
                        >
                          <Info :size="13" />
                        </button>
                      </TooltipTrigger>
                      <TooltipContent class="max-w-64 text-xs">
                        Default only moves items to Trash. Enable this to permanently clear each trashed item before re-uploading so Google Photos does not deduplicate the same SHA-1 hash.
                      </TooltipContent>
                    </Tooltip>
                  </div>
                  <Switch
                    id="reclaim-delete-perm"
                    v-model="deletePermanently"
                    variant="destructive"
                  />
                </div>

                <div class="flex items-center justify-between gap-2">
                  <Label
                    for="reclaim-cleanup"
                    class="text-xs cursor-pointer"
                  >
                    Delete local backup after re-upload
                  </Label>
                  <Switch
                    id="reclaim-cleanup"
                    v-model="cleanupLocal"
                  />
                </div>

                <div class="flex items-center justify-between gap-2">
                  <Label
                    for="reclaim-saver"
                    class="text-xs cursor-pointer"
                  >
                    Storage Saver quality (Pixel 2)
                  </Label>
                  <Switch
                    id="reclaim-saver"
                    v-model="saver"
                  />
                </div>

                <div class="flex items-center justify-between gap-2">
                  <Label
                    for="reclaim-meta"
                    class="text-xs cursor-pointer"
                  >
                    Restore favorite, archive &amp; caption
                  </Label>
                  <Switch
                    id="reclaim-meta"
                    v-model="restoreMetadata"
                  />
                </div>

                <div class="flex items-center justify-between gap-2 pt-1">
                  <Label class="text-xs">Limit (0 = all)</Label>
                  <NumberField
                    v-model="limit"
                    :min="0"
                    :max="state.items.length"
                    class="w-28"
                  >
                    <NumberFieldContent>
                      <NumberFieldDecrement class="cursor-pointer" />
                      <NumberFieldInput class="h-7 text-xs" />
                      <NumberFieldIncrement class="cursor-pointer" />
                    </NumberFieldContent>
                  </NumberField>
                </div>

                <div class="flex items-center justify-between gap-2">
                  <Label class="text-xs">Threads</Label>
                  <NumberField
                    v-model="threads"
                    :min="1"
                    :max="5"
                    class="w-28"
                  >
                    <NumberFieldContent>
                      <NumberFieldDecrement class="cursor-pointer" />
                      <NumberFieldInput class="h-7 text-xs" />
                      <NumberFieldIncrement class="cursor-pointer" />
                    </NumberFieldContent>
                  </NumberField>
                </div>
              </div>

              <!-- Preview list -->
              <div class="space-y-1">
                <p class="text-[11px] font-medium text-muted-foreground">
                  Largest items ({{ Math.min(state.items.length, 30) }} of {{ state.items.length }})
                </p>
                <div class="space-y-1">
                  <div
                    v-for="item in state.items.slice(0, 30)"
                    :key="item.mediaKey"
                    class="flex items-center justify-between rounded border px-2 py-1 text-xs"
                  >
                    <div class="min-w-0 flex-1 pr-2">
                      <p class="truncate font-medium">
                        {{ item.fileName }}
                      </p>
                      <p class="text-[10px] text-muted-foreground">
                        {{ item.isOriginalQuality ? 'Original' : 'Storage Saver' }}
                        <span v-if="item.isFavorite"> · Favorite</span>
                        <span v-if="item.isArchived"> · Archived</span>
                        <span v-if="item.isEdited"> · Edited</span>
                      </p>
                    </div>
                    <span class="shrink-0 tabular-nums text-muted-foreground">
                      {{ formatBytes(item.quotaChargedBytes) }}
                    </span>
                  </div>
                </div>
              </div>
            </div>
          </ScrollArea>

          <!-- Action buttons -->
          <div class="flex gap-2 pt-1">
            <Button
              variant="outline"
              size="sm"
              class="cursor-pointer"
              @click="startScan"
            >
              <RefreshCw :size="14" />
            </Button>
            <Button
              size="sm"
              class="cursor-pointer flex-1"
              @click="startReclaim"
            >
              <Play
                :size="14"
                class="mr-1.5"
              />
              Reclaim {{ effectiveCount }} ({{ formatBytes(effectiveQuotaBytes) }})
            </Button>
          </div>
        </template>
      </template>

      <!-- Running -->
      <template v-else-if="state.phase === 'running'">
        <div class="text-center">
          <p class="text-2xl font-bold tabular-nums">
            {{ state.processed }}<span class="text-muted-foreground font-normal">/</span><span class="text-muted-foreground">{{ state.total }}</span>
          </p>
          <p class="text-xs text-muted-foreground">
            Reclaimed {{ formatBytes(state.reclaimedBytes) }} / {{ formatBytes(state.totalQuotaBytes) }}
          </p>
        </div>

        <div>
          <Progress
            :model-value="progressPercent"
            class="h-2.5"
          />
          <div class="flex justify-between mt-1 text-xs text-muted-foreground">
            <span>{{ state.succeeded }} ok · {{ state.failed }} failed</span>
            <span class="font-medium">{{ progressPercent }}%</span>
          </div>
        </div>

        <ScrollArea class="flex-1 min-h-0">
          <div class="space-y-2 pr-2">
            <div
              v-for="w in workersList"
              :key="w.workerId"
              class="rounded-md border bg-muted/20 p-2.5 space-y-1"
            >
              <div class="flex items-center justify-between gap-2 text-xs">
                <span class="truncate font-medium">{{ w.fileName }}</span>
                <span class="shrink-0 text-[10px] px-1.5 py-0.5 rounded bg-muted text-muted-foreground">
                  {{ stageLabel(w.stage) }}
                </span>
              </div>
              <p class="text-[11px] text-muted-foreground truncate">
                {{ w.message }}
              </p>
              <Progress
                v-if="w.bytesTotal > 0 && (w.stage === 'downloading' || w.stage === 'uploading')"
                :model-value="Math.min(100, Math.round((w.bytesDone / w.bytesTotal) * 100))"
                class="h-1"
              />
            </div>
          </div>
        </ScrollArea>

        <Button
          variant="destructive"
          size="sm"
          class="cursor-pointer w-full"
          @click="reclaimManager.cancel()"
        >
          <X
            :size="14"
            class="mr-1"
          />
          Cancel Reclaim
        </Button>
      </template>

      <!-- Done -->
      <template v-else-if="state.phase === 'done' && state.summary">
        <div class="rounded-lg border p-4 space-y-2 text-center">
          <div class="flex justify-center">
            <AlertTriangle
              v-if="state.summary.failed > 0 || state.summary.errorMessage"
              :size="28"
              class="text-amber-500"
            />
            <CheckCircle2
              v-else
              :size="28"
              class="text-emerald-500"
            />
          </div>
          <h2 class="text-base font-semibold">
            {{ state.summary.canceled ? 'Reclaim Canceled' : 'Reclaim Finished' }}
          </h2>
          <p class="text-xs text-muted-foreground">
            Reclaimed: <span class="text-foreground font-medium">{{ formatBytes(state.summary.reclaimedBytes) }}</span>
          </p>
          <div class="flex justify-center gap-4 text-xs pt-1">
            <span>Succeeded: <strong>{{ state.summary.succeeded }}</strong></span>
            <span>Failed: <strong>{{ state.summary.failed }}</strong></span>
          </div>
          <p
            v-if="state.summary.downloadDir"
            class="text-[11px] text-muted-foreground break-all pt-1"
          >
            Backups: {{ state.summary.downloadDir }}
          </p>
          <p
            v-if="state.summary.errorMessage"
            class="text-xs text-destructive"
          >
            {{ state.summary.errorMessage }}
          </p>
        </div>

        <ScrollArea
          v-if="state.results.length > 0"
          class="flex-1 min-h-0"
        >
          <div class="space-y-1.5 pr-2">
            <div
              v-for="item in state.results"
              :key="item.mediaKey"
              class="rounded border px-2.5 py-1.5 text-xs space-y-0.5"
            >
              <div class="flex items-center justify-between gap-2">
                <span class="truncate font-medium">{{ item.fileName }}</span>
                <span
                  :class="item.isError ? 'text-destructive' : 'text-emerald-500'"
                  class="shrink-0 text-[11px]"
                >
                  {{ item.isError ? 'Failed' : formatBytes(item.quotaChargedBytes) }}
                </span>
              </div>
              <p
                v-if="item.errorMessage"
                class="text-[11px] text-destructive break-words"
              >
                {{ item.errorMessage }}
              </p>
              <p
                v-else
                class="text-[10px] text-muted-foreground"
              >
                <Trash2
                  :size="10"
                  class="inline mr-0.5"
                />
                {{ item.deletedPermanently ? 'Deleted from Trash' : 'Moved to Trash' }}
                · Re-uploaded (0 quota)
                {{ item.localCleanedUp ? '· Local cleaned' : '· Backup kept' }}
              </p>
            </div>
          </div>
        </ScrollArea>

        <div class="flex gap-2">
          <Button
            variant="outline"
            size="sm"
            class="cursor-pointer flex-1"
            @click="copyJson"
          >
            <Copy
              :size="13"
              class="mr-1.5"
            />
            {{ copyLabel }}
          </Button>
          <Button
            size="sm"
            class="cursor-pointer flex-1"
            @click="startScan"
          >
            <RefreshCw
              :size="13"
              class="mr-1.5"
            />
            Scan Again
          </Button>
        </div>
      </template>
    </div>
  </TooltipProvider>
</template>
