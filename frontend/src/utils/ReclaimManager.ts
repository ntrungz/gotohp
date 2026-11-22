import { Clipboard, Events } from "@wailsio/runtime";
import { reactive } from "vue";

export interface RemoteMediaItem {
  mediaKey: string;
  dedupKey: string;
  fileName: string;
  caption?: string;
  type: number;
  sizeBytes: number;
  quotaChargedBytes: number;
  utcTimestamp: number;
  origin: string;
  trashTimestamp?: number;
  isArchived?: boolean;
  isFavorite?: boolean;
  isLocked?: boolean;
  isOriginalQuality?: boolean;
  isEdited?: boolean;
  isMicroVideo?: boolean;
}

export interface LibraryScanStats {
  pagesScanned: number;
  totalScanned: number;
  eligibleCount: number;
  totalQuotaBytes: number;
  skippedTrashed: number;
  skippedLocked: number;
  skippedNotOwned: number;
  skippedNoDedupId: number;
  skippedDuplicate?: number;
}

export interface ReclaimScanResult {
  items: RemoteMediaItem[];
  stats: LibraryScanStats;
  downloadDir: string;
  errorMessage?: string;
}

export interface ReclaimBatchStart {
  total: number;
  totalQuotaBytes: number;
  downloadDir: string;
  deletePermanently: boolean;
  cleanupLocal: boolean;
  saver: boolean;
}

export interface ReclaimItemProgress {
  workerId: number;
  mediaKey: string;
  fileName: string;
  stage: string;
  message: string;
  bytesDone: number;
  bytesTotal: number;
}

export interface ReclaimItemResult {
  mediaKey: string;
  newMediaKey?: string;
  fileName: string;
  quotaChargedBytes: number;
  localPath?: string;
  downloaded: boolean;
  trashed: boolean;
  deletedPermanently: boolean;
  restoredFromTrash?: boolean;
  reuploaded: boolean;
  localCleanedUp: boolean;
  isError: boolean;
  errorMessage?: string;
  warnings?: string[];
}

export interface ReclaimSummary {
  total: number;
  succeeded: number;
  failed: number;
  reclaimedBytes: number;
  downloadDir: string;
  canceled?: boolean;
  errorMessage?: string;
  results?: ReclaimItemResult[];
}

export interface StartReclaimRunOptions {
  items: RemoteMediaItem[];
  downloadDir: string;
  deletePermanently: boolean;
  cleanupLocal: boolean;
  saver: boolean;
  restoreMetadata: boolean;
  threads: number;
  limit: number;
}

export type ReclaimPhase = "idle" | "scanning" | "reviewed" | "running" | "done";

export interface ReclaimState {
  phase: ReclaimPhase;
  scanStats: LibraryScanStats;
  scanError: string;
  items: RemoteMediaItem[];
  downloadDir: string;
  total: number;
  totalQuotaBytes: number;
  processed: number;
  succeeded: number;
  failed: number;
  reclaimedBytes: number;
  workers: Map<number, ReclaimItemProgress>;
  results: ReclaimItemResult[];
  summary: ReclaimSummary | null;
}

function emptyScanStats(): LibraryScanStats {
  return {
    pagesScanned: 0,
    totalScanned: 0,
    eligibleCount: 0,
    totalQuotaBytes: 0,
    skippedTrashed: 0,
    skippedLocked: 0,
    skippedNotOwned: 0,
    skippedNoDedupId: 0,
    skippedDuplicate: 0,
  };
}

class ReclaimManager {
  private static instance: ReclaimManager;

  public state = reactive<ReclaimState>({
    phase: "idle",
    scanStats: emptyScanStats(),
    scanError: "",
    items: [],
    downloadDir: "",
    total: 0,
    totalQuotaBytes: 0,
    processed: 0,
    succeeded: 0,
    failed: 0,
    reclaimedBytes: 0,
    workers: new Map<number, ReclaimItemProgress>(),
    results: [],
    summary: null,
  });

  private constructor() {
    this.setupEventListeners();
  }

  public static getInstance(): ReclaimManager {
    if (!ReclaimManager.instance) {
      ReclaimManager.instance = new ReclaimManager();
    }
    return ReclaimManager.instance;
  }

  private setupEventListeners() {
    Events.On("reclaimScanProgress", (event: { data: LibraryScanStats }) => {
      this.state.scanStats = event.data;
    });

    Events.On("reclaimScanComplete", (event: { data: ReclaimScanResult }) => {
      const data = event.data;
      this.state.scanStats = data.stats ?? emptyScanStats();
      this.state.items = data.items ?? [];
      if (data.downloadDir) {
        this.state.downloadDir = data.downloadDir;
      }
      if (data.errorMessage) {
        this.state.scanError = data.errorMessage;
        this.state.phase = "idle";
        window.dispatchEvent(new CustomEvent("reclaimError", { detail: { message: data.errorMessage } }));
      } else {
        this.state.scanError = "";
        this.state.phase = "reviewed";
      }
    });

    Events.On("reclaimStart", (event: { data: ReclaimBatchStart }) => {
      this.state.phase = "running";
      this.state.total = event.data.total;
      this.state.totalQuotaBytes = event.data.totalQuotaBytes;
      this.state.downloadDir = event.data.downloadDir;
      this.state.processed = 0;
      this.state.succeeded = 0;
      this.state.failed = 0;
      this.state.reclaimedBytes = 0;
      this.state.workers.clear();
      this.state.results = [];
      this.state.summary = null;
    });

    Events.On("reclaimProgress", (event: { data: ReclaimItemProgress }) => {
      this.state.workers.set(event.data.workerId, event.data);
    });

    Events.On("reclaimItemResult", (event: { data: ReclaimItemResult }) => {
      this.state.results.push(event.data);
      this.state.processed += 1;
      if (event.data.isError) {
        this.state.failed += 1;
      } else {
        this.state.succeeded += 1;
        this.state.reclaimedBytes += event.data.quotaChargedBytes;
      }
    });

    Events.On("reclaimStop", (event: { data: ReclaimSummary }) => {
      this.state.summary = event.data;
      this.state.phase = "done";
      if (event.data.errorMessage) {
        window.dispatchEvent(new CustomEvent("reclaimError", { detail: { message: event.data.errorMessage } }));
      }
    });
  }

  public startScan(account = "") {
    this.state.phase = "scanning";
    this.state.scanStats = emptyScanStats();
    this.state.scanError = "";
    this.state.items = [];
    this.state.summary = null;
    Events.Emit("startReclaimScan", { account });
  }

  public startReclaim(options: StartReclaimRunOptions) {
    this.state.phase = "running";
    this.state.processed = 0;
    this.state.succeeded = 0;
    this.state.failed = 0;
    this.state.reclaimedBytes = 0;
    this.state.workers.clear();
    this.state.results = [];
    this.state.summary = null;
    Events.Emit("startReclaimRun", options);
  }

  public cancel() {
    Events.Emit("reclaimCancel");
  }

  public reset() {
    this.state.phase = "idle";
    this.state.scanStats = emptyScanStats();
    this.state.scanError = "";
    this.state.items = [];
    this.state.workers.clear();
    this.state.results = [];
    this.state.summary = null;
  }

  public async copyResultsAsJson(): Promise<boolean> {
    const payload = this.state.summary ?? {
      stats: this.state.scanStats,
      items: this.state.items,
    };
    try {
      await Clipboard.SetText(JSON.stringify(payload, null, 2));
      return true;
    } catch (error) {
      console.error("Failed to copy reclaim results:", error);
      return false;
    }
  }
}

export const reclaimManager = ReclaimManager.getInstance();
