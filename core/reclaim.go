package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultReclaimDownloadDir returns the default local folder used to store
// downloaded media backups during quota reclaim.
func DefaultReclaimDownloadDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "gotohp-reclaim"
	}
	return filepath.Join(home, "gotohp-reclaim")
}

// ReclaimOptions controls how a quota-reclaim scan or run behaves.
type ReclaimOptions struct {
	// Api selects the account, proxy, and saver quality. UseQuota is always
	// forced to false during reclaim re-uploads.
	Api ApiOptions

	// DownloadDir is the directory where original files are downloaded before
	// trashing and re-uploading. Empty defaults to DefaultReclaimDownloadDir().
	DownloadDir string

	// DeletePermanently removes trashed items permanently before re-uploading.
	// Defaults to false (move to Trash only).
	DeletePermanently bool

	// CleanupLocal removes each downloaded local file after its re-upload
	// succeeds. Defaults to false (keep downloaded files as local backup).
	CleanupLocal bool

	// DryRun scans and reports quota-consuming items without modifying remote
	// or local files.
	DryRun bool

	// RestoreMetadata restores favorite, archived, and caption fields on the
	// newly uploaded item after re-upload succeeds.
	RestoreMetadata bool

	// Limit caps how many quota-consuming items are processed in one run (0 = all).
	Limit int

	// Threads sets concurrent reclaim workers (default 1).
	Threads int

	// Items optionally supplies pre-scanned items to process without scanning again.
	Items []RemoteMediaItem
}

func (o ReclaimOptions) normalized() ReclaimOptions {
	o.Api.UseQuota = false
	if strings.TrimSpace(o.DownloadDir) == "" {
		o.DownloadDir = DefaultReclaimDownloadDir()
	}
	if o.Threads < 1 {
		o.Threads = 1
	}
	if o.Limit < 0 {
		o.Limit = 0
	}
	return o
}

// ReclaimScanResult is emitted when scanning the remote library finishes.
type ReclaimScanResult struct {
	Items        []RemoteMediaItem `json:"items"`
	Stats        LibraryScanStats  `json:"stats"`
	DownloadDir  string            `json:"downloadDir"`
	ErrorMessage string            `json:"errorMessage,omitempty"`
}

// ReclaimBatchStart is emitted when the download/trash/re-upload phase starts.
type ReclaimBatchStart struct {
	Total             int    `json:"total"`
	TotalQuotaBytes   int64  `json:"totalQuotaBytes"`
	DownloadDir       string `json:"downloadDir"`
	DeletePermanently bool   `json:"deletePermanently"`
	CleanupLocal      bool   `json:"cleanupLocal"`
	Saver             bool   `json:"saver"`
}

// ReclaimItemProgress reports live stage progress for a single item.
type ReclaimItemProgress struct {
	WorkerID   int    `json:"workerId"`
	MediaKey   string `json:"mediaKey"`
	FileName   string `json:"fileName"`
	Stage      string `json:"stage"`
	Message    string `json:"message"`
	BytesDone  int64  `json:"bytesDone"`
	BytesTotal int64  `json:"bytesTotal"`
}

// ReclaimItemResult records the final outcome of processing one remote item.
type ReclaimItemResult struct {
	MediaKey           string   `json:"mediaKey"`
	NewMediaKey        string   `json:"newMediaKey,omitempty"`
	FileName           string   `json:"fileName"`
	QuotaChargedBytes  int64    `json:"quotaChargedBytes"`
	LocalPath          string   `json:"localPath,omitempty"`
	Downloaded         bool     `json:"downloaded"`
	Trashed            bool     `json:"trashed"`
	DeletedPermanently bool     `json:"deletedPermanently"`
	RestoredFromTrash  bool     `json:"restoredFromTrash,omitempty"`
	Reuploaded         bool     `json:"reuploaded"`
	LocalCleanedUp     bool     `json:"localCleanedUp"`
	IsError            bool     `json:"isError"`
	ErrorMessage       string   `json:"errorMessage,omitempty"`
	Warnings           []string `json:"warnings,omitempty"`
}

// ReclaimSummary summarizes an entire reclaim run.
type ReclaimSummary struct {
	Total          int                 `json:"total"`
	Succeeded      int                 `json:"succeeded"`
	Failed         int                 `json:"failed"`
	ReclaimedBytes int64               `json:"reclaimedBytes"`
	DownloadDir    string              `json:"downloadDir"`
	Canceled       bool                `json:"canceled,omitempty"`
	ErrorMessage   string              `json:"errorMessage,omitempty"`
	Results        []ReclaimItemResult `json:"results,omitempty"`
}

// StartReclaimScanEvent is emitted by the GUI to start scanning quota-consuming items.
type StartReclaimScanEvent struct {
	Account string `json:"account,omitempty"`
}

// StartReclaimRunEvent is emitted by the GUI to start reclaiming scanned items.
type StartReclaimRunEvent struct {
	Items             []RemoteMediaItem `json:"items"`
	DownloadDir       string            `json:"downloadDir"`
	DeletePermanently bool              `json:"deletePermanently"`
	CleanupLocal      bool              `json:"cleanupLocal"`
	Saver             bool              `json:"saver"`
	RestoreMetadata   bool              `json:"restoreMetadata"`
	Threads           int               `json:"threads"`
	Limit             int               `json:"limit"`
}

// ReclaimReporter receives progress events from ReclaimManager.
type ReclaimReporter interface {
	ReclaimScanProgress(LibraryScanStats)
	ReclaimScanComplete(ReclaimScanResult)
	ReclaimStart(ReclaimBatchStart)
	ReclaimProgress(ReclaimItemProgress)
	ReclaimItemResult(ReclaimItemResult)
	ReclaimStop(ReclaimSummary)
}

// NopReclaimReporter discards all reclaim events.
type NopReclaimReporter struct{}

func (NopReclaimReporter) ReclaimScanProgress(LibraryScanStats)  {}
func (NopReclaimReporter) ReclaimScanComplete(ReclaimScanResult) {}
func (NopReclaimReporter) ReclaimStart(ReclaimBatchStart)        {}
func (NopReclaimReporter) ReclaimProgress(ReclaimItemProgress)   {}
func (NopReclaimReporter) ReclaimItemResult(ReclaimItemResult)   {}
func (NopReclaimReporter) ReclaimStop(ReclaimSummary)            {}

// ReclaimManager coordinates scanning quota-consuming remote items, downloading
// them locally, trashing (and optionally permanently deleting) the quota-charged
// copies, and re-uploading them without quota.
type ReclaimManager struct {
	mu       sync.Mutex
	running  bool
	cancelFn context.CancelFunc
	reporter ReclaimReporter
	logger   *slog.Logger
	newAPI   func(ApiOptions) (*Api, error)
}

// NewReclaimManager creates a ReclaimManager reporting events to reporter.
func NewReclaimManager(reporter ReclaimReporter, logger *slog.Logger) *ReclaimManager {
	if reporter == nil {
		reporter = NopReclaimReporter{}
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &ReclaimManager{
		reporter: reporter,
		logger:   logger,
		newAPI:   NewApi,
	}
}

func (m *ReclaimManager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

func (m *ReclaimManager) Cancel() {
	m.mu.Lock()
	cancel := m.cancelFn
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (m *ReclaimManager) beginRun() (context.Context, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return nil, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.running = true
	m.cancelFn = cancel
	return ctx, true
}

func (m *ReclaimManager) endRun() {
	m.mu.Lock()
	cancel := m.cancelFn
	m.cancelFn = nil
	m.running = false
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Scan scans the remote Google Photos library for quota-consuming items in the
// background and reports results through the manager's ReclaimReporter.
func (m *ReclaimManager) Scan(opts ReclaimOptions) {
	opts = opts.normalized()
	ctx, ok := m.beginRun()
	if !ok {
		return
	}

	go func() {
		defer m.endRun()
		res := m.scanSync(ctx, opts)
		m.reporter.ReclaimScanComplete(res)
	}()
}

// Run executes the reclaim workflow in the background (scanning first if
// opts.Items is empty, unless opts.DryRun is set in which case it stops after
// reporting the scan).
func (m *ReclaimManager) Run(opts ReclaimOptions) {
	opts = opts.normalized()
	ctx, ok := m.beginRun()
	if !ok {
		return
	}

	go func() {
		defer m.endRun()
		summary := m.runSync(ctx, opts)
		m.reporter.ReclaimStop(summary)
	}()
}

func (m *ReclaimManager) scanSync(ctx context.Context, opts ReclaimOptions) ReclaimScanResult {
	api, err := m.newAPI(opts.Api)
	if err != nil {
		return ReclaimScanResult{
			DownloadDir:  opts.DownloadDir,
			ErrorMessage: err.Error(),
		}
	}

	items, stats, err := api.ScanQuotaConsumingMedia(ctx, func(s LibraryScanStats) {
		m.reporter.ReclaimScanProgress(s)
	})
	if err != nil {
		return ReclaimScanResult{
			Stats:        stats,
			DownloadDir:  opts.DownloadDir,
			ErrorMessage: err.Error(),
		}
	}

	return ReclaimScanResult{
		Items:       items,
		Stats:       stats,
		DownloadDir: opts.DownloadDir,
	}
}

func (m *ReclaimManager) runSync(ctx context.Context, opts ReclaimOptions) ReclaimSummary {
	items := opts.Items
	if len(items) == 0 {
		scanRes := m.scanSync(ctx, opts)
		m.reporter.ReclaimScanComplete(scanRes)
		if scanRes.ErrorMessage != "" {
			return ReclaimSummary{
				DownloadDir:  opts.DownloadDir,
				Canceled:     errors.Is(ctx.Err(), context.Canceled),
				ErrorMessage: scanRes.ErrorMessage,
			}
		}
		items = scanRes.Items
	}

	// Trash/delete RPCs operate on DedupKey, so items sharing one must never be
	// processed by separate workers (a later worker could trash the copy an
	// earlier worker just re-uploaded, since it has the same SHA-1).
	items, _ = dedupeMediaItemsByDedupKey(items)

	if opts.Limit > 0 && len(items) > opts.Limit {
		items = items[:opts.Limit]
	}

	if opts.DryRun {
		var totalBytes int64
		for _, item := range items {
			totalBytes += item.QuotaChargedBytes
		}
		return ReclaimSummary{
			Total:          len(items),
			ReclaimedBytes: 0,
			DownloadDir:    opts.DownloadDir,
		}
	}

	var totalQuotaBytes int64
	for _, item := range items {
		totalQuotaBytes += item.QuotaChargedBytes
	}

	m.reporter.ReclaimStart(ReclaimBatchStart{
		Total:             len(items),
		TotalQuotaBytes:   totalQuotaBytes,
		DownloadDir:       opts.DownloadDir,
		DeletePermanently: opts.DeletePermanently,
		CleanupLocal:      opts.CleanupLocal,
		Saver:             opts.Api.Saver,
	})

	if len(items) == 0 {
		return ReclaimSummary{
			DownloadDir: opts.DownloadDir,
		}
	}

	if err := os.MkdirAll(opts.DownloadDir, 0o755); err != nil {
		return ReclaimSummary{
			Total:        len(items),
			DownloadDir:  opts.DownloadDir,
			ErrorMessage: fmt.Sprintf("failed to create download directory %q: %v", opts.DownloadDir, err),
		}
	}

	// Verify API client can be initialized before spawning workers.
	if _, err := m.newAPI(opts.Api); err != nil {
		return ReclaimSummary{
			Total:        len(items),
			DownloadDir:  opts.DownloadDir,
			ErrorMessage: err.Error(),
		}
	}

	numWorkers := min(opts.Threads, len(items))
	workChan := make(chan RemoteMediaItem, len(items))
	resultsChan := make(chan ReclaimItemResult, len(items))

	var wg sync.WaitGroup
	for i := range numWorkers {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			api, err := m.newAPI(opts.Api)
			if err != nil {
				for item := range workChan {
					resultsChan <- ReclaimItemResult{
						MediaKey:          item.MediaKey,
						FileName:          item.FileName,
						QuotaChargedBytes: item.QuotaChargedBytes,
						IsError:           true,
						ErrorMessage:      fmt.Sprintf("failed to initialize API: %v", err),
					}
				}
				return
			}
			for item := range workChan {
				if ctx.Err() != nil {
					return
				}
				res := m.processItem(ctx, api, item, opts, workerID)
				resultsChan <- res
			}
		}(i)
	}

	go func() {
	LOOP:
		for _, item := range items {
			select {
			case <-ctx.Done():
				break LOOP
			case workChan <- item:
			}
		}
		close(workChan)
		wg.Wait()
		close(resultsChan)
	}()

	summary := ReclaimSummary{
		Total:       len(items),
		DownloadDir: opts.DownloadDir,
		Results:     make([]ReclaimItemResult, 0, len(items)),
	}
	for res := range resultsChan {
		m.reporter.ReclaimItemResult(res)
		summary.Results = append(summary.Results, res)
		if res.IsError {
			summary.Failed++
			m.logger.Error("reclaim item failed", "mediaKey", res.MediaKey, "fileName", res.FileName, "error", res.ErrorMessage)
		} else {
			summary.Succeeded++
			summary.ReclaimedBytes += res.QuotaChargedBytes
			m.logger.Info("reclaim item succeeded", "mediaKey", res.MediaKey, "newMediaKey", res.NewMediaKey, "fileName", res.FileName)
		}
	}
	summary.Canceled = errors.Is(ctx.Err(), context.Canceled)
	return summary
}

func (m *ReclaimManager) processItem(
	ctx context.Context,
	api *Api,
	item RemoteMediaItem,
	opts ReclaimOptions,
	workerID int,
) ReclaimItemResult {
	res := ReclaimItemResult{
		MediaKey:          item.MediaKey,
		FileName:          item.FileName,
		QuotaChargedBytes: item.QuotaChargedBytes,
	}

	emit := func(stage, message string, done, total int64) {
		m.reporter.ReclaimProgress(ReclaimItemProgress{
			WorkerID:   workerID,
			MediaKey:   item.MediaKey,
			FileName:   item.FileName,
			Stage:      stage,
			Message:    message,
			BytesDone:  done,
			BytesTotal: total,
		})
	}

	fail := func(err error) ReclaimItemResult {
		res.IsError = true
		res.ErrorMessage = err.Error()
		emit("error", err.Error(), 0, item.SizeBytes)
		return res
	}

	// Stage 1: Resolve download URL and download original file locally.
	emit("downloading", "Fetching download link...", 0, item.SizeBytes)
	urls, err := api.GetDownloadURLs(ctx, item.MediaKey)
	if err != nil {
		return fail(err)
	}
	downloadURL := urls.BestURL()
	if downloadURL == "" {
		return fail(fmt.Errorf("no download URL available for %s", item.MediaKey))
	}

	localPath := buildReclaimLocalPath(opts.DownloadDir, item.MediaKey, item.FileName)
	res.LocalPath = localPath

	written, err := api.DownloadMediaFile(ctx, downloadURL, localPath, func(bytesDone, bytesTotal int64, attempt int) {
		msg := "Downloading original..."
		if attempt > 1 {
			msg = fmt.Sprintf("Retrying download (attempt %d)...", attempt)
		}
		emit("downloading", msg, bytesDone, bytesTotal)
	})
	if err != nil {
		return fail(err)
	}
	if written <= 0 {
		return fail(errors.New("downloaded file is empty"))
	}
	res.Downloaded = true

	// Preserve original capture timestamp on the downloaded file so CommitUpload
	// uses it as the upload timestamp.
	if tsSec := normalizeUnixSeconds(item.UTCTimestamp); tsSec > 0 {
		t := time.Unix(tsSec, 0)
		_ = os.Chtimes(localPath, t, t)
	}

	if ctx.Err() != nil {
		return fail(ctx.Err())
	}

	// Stage 2: Move the quota-consuming remote copy to Trash.
	emit("trashing", "Moving remote copy to Trash...", written, written)
	if err := api.MoveRemoteMediaToTrash(ctx, []string{item.DedupKey}); err != nil {
		return fail(err)
	}
	res.Trashed = true

	// Stage 3 (opt-in): Permanently delete from Trash before re-uploading to
	// avoid Google Photos SHA-1 deduplication against the trashed copy.
	if opts.DeletePermanently {
		emit("deleting", "Permanently deleting from Trash...", written, written)
		if err := api.DeleteRemoteMediaPermanently(ctx, []string{item.DedupKey}); err != nil {
			return fail(err)
		}
		res.DeletedPermanently = true
	}

	// Stage 4: Re-upload locally downloaded file with UseQuota=false (Pixel XL / Pixel 2).
	emit("uploading", "Re-uploading without quota...", 0, written)
	uploadOpts := UploadOptions{
		Api: ApiOptions{
			Account:  opts.Api.Account,
			Proxy:    opts.Api.Proxy,
			Saver:    opts.Api.Saver,
			UseQuota: false,
		},
		ForceUpload:    true,
		DeleteFromHost: false,
	}

	// Use a scoped reporter adapter so upload byte progress streams into ReclaimProgress.
	uploadReporter := reclaimUploadProgressAdapter{
		onProgress: func(status ThreadStatus) {
			if status.Status == "uploading" || status.Status == "finalizing" || status.Status == "hashing" {
				emit("uploading", status.Message, status.BytesUploaded, status.BytesTotal)
			}
		},
	}

	newMediaKey, err := uploadSingleFile(ctx, api, localPath, uploadOpts, workerID, uploadReporter)
	if err != nil {
		// Safety rollback: when the remote item was only moved to Trash (not
		// permanently deleted), attempt to restore it from Trash so the library
		// remains intact while the local backup stays on disk.
		if res.Trashed && !res.DeletedPermanently {
			restoreCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			emit("restoring_trash", "Re-upload failed; restoring item from Trash...", 0, written)
			if restoreErr := api.RestoreRemoteMediaFromTrash(restoreCtx, []string{item.DedupKey}); restoreErr == nil {
				res.RestoredFromTrash = true
				res.Warnings = append(res.Warnings, "re-upload failed; original item was restored from Trash and local backup was kept")
			} else {
				res.Warnings = append(res.Warnings, fmt.Sprintf("re-upload failed and automatic restore from Trash also failed (%v); local backup was kept at %s", restoreErr, localPath))
			}
		} else {
			res.Warnings = append(res.Warnings, fmt.Sprintf("re-upload failed; local backup was kept at %s", localPath))
		}
		return fail(fmt.Errorf("re-upload failed: %w", err))
	}
	res.NewMediaKey = newMediaKey
	res.Reuploaded = true

	// Stage 5: Best-effort metadata restoration (favorite, archived, caption).
	if opts.RestoreMetadata && (item.IsFavorite || item.IsArchived || item.Caption != "") {
		emit("restoring_metadata", "Restoring metadata...", written, written)
		sha1Bytes, hashErr := CalculateSHA1(ctx, localPath)
		if hashErr != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("could not compute hash to restore metadata: %v", hashErr))
		} else {
			newDedupKey := urlSafeBase64SHA1(sha1Bytes)
			if item.IsFavorite {
				if err := api.SetFavorite(ctx, newDedupKey, true); err != nil {
					res.Warnings = append(res.Warnings, fmt.Sprintf("failed to restore favorite flag: %v", err))
				}
			}
			if item.IsArchived {
				if err := api.SetArchived(ctx, []string{newDedupKey}, true); err != nil {
					res.Warnings = append(res.Warnings, fmt.Sprintf("failed to restore archived flag: %v", err))
				}
			}
			if item.Caption != "" {
				if err := api.SetItemCaption(ctx, newDedupKey, item.Caption); err != nil {
					res.Warnings = append(res.Warnings, fmt.Sprintf("failed to restore caption: %v", err))
				}
			}
		}
	}

	// Stage 6 (opt-in): Remove local copy only after verified re-upload.
	if opts.CleanupLocal {
		emit("cleaning_up", "Removing local temporary copy...", written, written)
		if err := os.Remove(localPath); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("re-upload succeeded but failed to remove local file: %v", err))
		} else {
			res.LocalCleanedUp = true
			// Remove the per-item subdirectory if now empty.
			_ = os.Remove(filepath.Dir(localPath))
		}
	}

	emit("completed", "Completed", written, written)
	return res
}

type reclaimUploadProgressAdapter struct {
	NopReporter
	onProgress func(ThreadStatus)
}

func (a reclaimUploadProgressAdapter) ThreadStatus(status ThreadStatus) {
	if a.onProgress != nil {
		a.onProgress(status)
	}
}

func normalizeUnixSeconds(ts int64) int64 {
	if ts <= 0 {
		return 0
	}
	// Google Photos library sync timestamps are in Unix milliseconds.
	if ts > 1_000_000_000_000 {
		return ts / 1000
	}
	return ts
}

func buildReclaimLocalPath(downloadDir, mediaKey, fileName string) string {
	safeKey := sanitizePathSegment(mediaKey, "item")
	safeName := sanitizePathSegment(fileName, safeKey+".bin")
	return filepath.Join(downloadDir, safeKey, safeName)
}

func sanitizePathSegment(name, fallback string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	if name == "" || name == "." || name == ".." || name == "/" {
		return fallback
	}
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f:
			b.WriteByte('_')
		case strings.ContainsRune(`<>:"/\|?*`, r):
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" || out == "." || out == ".." {
		return fallback
	}
	return out
}
