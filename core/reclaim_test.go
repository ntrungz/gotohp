package core

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"app/generated"

	"google.golang.org/protobuf/proto"
)

func buildTestMediaItemWire(
	mediaKey, dedupKey, fileName, caption string,
	sizeBytes, quotaBytes, utcMillis, trashTs int64,
	origin int64,
	archived, favorite, locked bool,
) wireField {
	d2Fields := []wireField{
		wfString(4, fileName),
		wfVarint(7, uint64(utcMillis)),
		wfVarint(10, uint64(sizeBytes)),
		wfMsg(21, wfString(1, dedupKey)),
		wfMsg(35, wfVarint(2, uint64(quotaBytes)), wfVarint(3, 2)),
	}
	if caption != "" {
		d2Fields = append(d2Fields, wfString(3, caption))
	}
	if trashTs > 0 {
		d2Fields = append(d2Fields, wfMsg(16, wfVarint(3, uint64(trashTs))))
	}
	if archived {
		d2Fields = append(d2Fields, wfMsg(29, wfVarint(1, 1)))
	}
	if origin > 0 {
		d2Fields = append(d2Fields, wfMsg(30, wfVarint(1, uint64(origin))))
	}
	if favorite {
		d2Fields = append(d2Fields, wfMsg(31, wfVarint(1, 1)))
	}
	if locked {
		d2Fields = append(d2Fields, wfMsg(39, wfVarint(1, 1)))
	}
	return wfMsg(2,
		wfString(1, mediaKey),
		wfMsg(2, d2Fields...),
		wfMsg(5, wfVarint(1, 1)),
	)
}

func buildTestLibraryPageResponse(syncToken, resumeToken string, items ...wireField) []byte {
	rootFields := []wireField{}
	if resumeToken != "" {
		rootFields = append(rootFields, wfString(1, resumeToken))
	}
	rootFields = append(rootFields, items...)
	if syncToken != "" {
		rootFields = append(rootFields, wfString(6, syncToken))
	}
	return encodeWireMsg(wfMsg(1, rootFields...))
}

func newTestAPIForServer(serverURL string, saver bool) *Api {
	return &Api{
		androidAPIVersion: 28,
		model:             "Pixel XL",
		make:              "Google",
		clientVersionCode: 49029607,
		userAgent:         "test-agent",
		language:          "en-US",
		client:            http.DefaultClient,
		authResponseCache: map[string]string{
			"Expiry": "4102444800", // year 2100
			"Auth":   "test-bearer-token",
		},
		commitEndpoint:       serverURL + "/commit",
		uploadTokenEndpoint:  serverURL + "/upload-token",
		uploadMediaBaseURL:   serverURL + "/upload-media",
		libraryStateEndpoint: serverURL + "/library-state",
		trashEndpoint:        serverURL + "/trash",
		downloadURLsEndpoint: serverURL + "/download-urls",
		setCaptionEndpoint:   serverURL + "/set-caption",
		setFavoriteEndpoint:  serverURL + "/set-favorite",
		setArchivedEndpoint:  serverURL + "/set-archived",
		saver:                saver,
		useQuota:             false,
		commitRetryConfig:    &RetryConfig{MaxRetries: 0, InitialDelay: time.Millisecond, MaxDelay: time.Millisecond},
	}
}

type recordingReclaimReporter struct {
	mu           sync.Mutex
	scanProgress []LibraryScanStats
	scanResults  []ReclaimScanResult
	starts       []ReclaimBatchStart
	progress     []ReclaimItemProgress
	itemResults  []ReclaimItemResult
	stops        []ReclaimSummary
	done         chan ReclaimSummary
}

func newRecordingReclaimReporter() *recordingReclaimReporter {
	return &recordingReclaimReporter{done: make(chan ReclaimSummary, 1)}
}

func (r *recordingReclaimReporter) ReclaimScanProgress(s LibraryScanStats) {
	r.mu.Lock()
	r.scanProgress = append(r.scanProgress, s)
	r.mu.Unlock()
}

func (r *recordingReclaimReporter) ReclaimScanComplete(res ReclaimScanResult) {
	r.mu.Lock()
	r.scanResults = append(r.scanResults, res)
	r.mu.Unlock()
}

func (r *recordingReclaimReporter) ReclaimStart(s ReclaimBatchStart) {
	r.mu.Lock()
	r.starts = append(r.starts, s)
	r.mu.Unlock()
}

func (r *recordingReclaimReporter) ReclaimProgress(p ReclaimItemProgress) {
	r.mu.Lock()
	r.progress = append(r.progress, p)
	r.mu.Unlock()
}

func (r *recordingReclaimReporter) ReclaimItemResult(res ReclaimItemResult) {
	r.mu.Lock()
	r.itemResults = append(r.itemResults, res)
	r.mu.Unlock()
}

func (r *recordingReclaimReporter) ReclaimStop(sum ReclaimSummary) {
	r.mu.Lock()
	r.stops = append(r.stops, sum)
	r.mu.Unlock()
	select {
	case r.done <- sum:
	default:
	}
}

func TestScanQuotaConsumingMediaPaginationAndFiltering(t *testing.T) {
	var pageCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/library-state" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		reqNode, err := decodeWireMsg(body)
		if err != nil {
			t.Errorf("invalid request protobuf: %v", err)
			http.Error(w, "bad proto", 400)
			return
		}
		pageCalls++
		resumeTok, _ := reqNode.nestedString(1, 4)

		switch resumeTok {
		case "":
			resp := buildTestLibraryPageResponse("sync-1", "resume-page-2",
				buildTestMediaItemWire("mk-free", "dk-free", "free.jpg", "", 5000, 0, 1700000000000, 0, 1, false, false, false),
				buildTestMediaItemWire("mk-small", "dk-small", "small.jpg", "Beach", 1000, 1000, 1700000001000, 0, 1, false, true, false),
				buildTestMediaItemWire("mk-trashed", "dk-trashed", "trashed.jpg", "", 2000, 2000, 1700000002000, 1700000099000, 1, false, false, false),
				buildTestMediaItemWire("mk-locked", "dk-locked", "locked.jpg", "", 3000, 3000, 1700000003000, 0, 1, false, false, true),
			)
			_, _ = w.Write(resp)
		case "resume-page-2":
			resp := buildTestLibraryPageResponse("sync-2", "",
				buildTestMediaItemWire("mk-big", "dk-big", "big.mp4", "", 9000, 9000, 1700000004000, 0, 1, true, false, false),
				buildTestMediaItemWire("mk-partner", "dk-partner", "partner.jpg", "", 4000, 4000, 1700000005000, 0, 3, false, false, false),
			)
			_, _ = w.Write(resp)
		default:
			t.Errorf("unexpected resume token %q", resumeTok)
			http.Error(w, "unexpected token", 400)
		}
	}))
	defer srv.Close()

	api := newTestAPIForServer(srv.URL, false)
	items, stats, err := api.ScanQuotaConsumingMedia(context.Background(), nil)
	if err != nil {
		t.Fatalf("ScanQuotaConsumingMedia failed: %v", err)
	}
	if pageCalls != 2 {
		t.Fatalf("pageCalls = %d, want 2", pageCalls)
	}
	if stats.PagesScanned != 2 || stats.TotalScanned != 6 || stats.EligibleCount != 2 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if stats.SkippedTrashed != 1 || stats.SkippedLocked != 1 || stats.SkippedNotOwned != 1 {
		t.Fatalf("unexpected skip counts: %+v", stats)
	}
	if stats.TotalQuotaBytes != 10000 {
		t.Fatalf("TotalQuotaBytes = %d, want 10000", stats.TotalQuotaBytes)
	}
	if len(items) != 2 || items[0].MediaKey != "mk-big" || items[1].MediaKey != "mk-small" {
		t.Fatalf("expected items sorted by quota desc [mk-big, mk-small], got %+v", items)
	}
	if !items[0].IsArchived || !items[1].IsFavorite || items[1].Caption != "Beach" {
		t.Fatalf("metadata not parsed properly: %+v", items)
	}
}

func TestScanQuotaConsumingMediaDedupesSharedDedupKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := buildTestLibraryPageResponse("sync-1", "",
			buildTestMediaItemWire("mk-a1", "dk-same", "IMG_4587.HEIC", "", 2400, 2400, 1700000000000, 0, 1, false, false, false),
			buildTestMediaItemWire("mk-a2", "dk-same", "IMG_4587.HEIC", "Trip", 2400, 2400, 1700000000000, 0, 1, false, true, false),
			buildTestMediaItemWire("mk-a3", "dk-same", "IMG_4587.HEIC", "", 2400, 2400, 1700000000000, 0, 1, true, false, false),
			buildTestMediaItemWire("mk-b", "dk-other", "IMG_2533.HEIC", "", 1000, 1000, 1700000001000, 0, 1, false, false, false),
		)
		_, _ = w.Write(resp)
	}))
	defer srv.Close()

	api := newTestAPIForServer(srv.URL, false)
	items, stats, err := api.ScanQuotaConsumingMedia(context.Background(), nil)
	if err != nil {
		t.Fatalf("ScanQuotaConsumingMedia failed: %v", err)
	}
	if len(items) != 2 || stats.EligibleCount != 2 || stats.SkippedDuplicate != 2 {
		t.Fatalf("expected 2 unique items and 2 duplicates, got items=%d stats=%+v", len(items), stats)
	}
	if stats.TotalQuotaBytes != 3400 {
		t.Fatalf("TotalQuotaBytes = %d, want 3400 (duplicates must not be double counted)", stats.TotalQuotaBytes)
	}
	first := items[0]
	if first.MediaKey != "mk-a1" || !first.IsFavorite || !first.IsArchived || first.Caption != "Trip" {
		t.Fatalf("duplicate metadata not merged into kept item: %+v", first)
	}
}

func TestDedupeMediaItemsByDedupKey(t *testing.T) {
	in := []RemoteMediaItem{
		{MediaKey: "m1", DedupKey: "d1"},
		{MediaKey: "m2", DedupKey: "d2"},
		{MediaKey: "m3", DedupKey: "d1", IsFavorite: true},
	}
	out, dups := dedupeMediaItemsByDedupKey(in)
	if dups != 1 || len(out) != 2 || out[0].MediaKey != "m1" || out[1].MediaKey != "m2" || !out[0].IsFavorite {
		t.Fatalf("unexpected dedupe result: dups=%d out=%+v", dups, out)
	}
}

func TestReclaimManagerEndToEndDefaultTrashOnlyAndMetadataRestore(t *testing.T) {
	fileBytes := []byte("fake-original-jpeg-content-1234567890")
	var (
		mu             sync.Mutex
		trashActions   []int64
		committedModel string
		committedQual  int64
		committedName  string
		captionSet     string
		favoriteSet    bool
		archivedSet    bool
	)

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/library-state":
			resp := buildTestLibraryPageResponse("sync-1", "",
				buildTestMediaItemWire("mk-1", "dk-1", "holiday.jpg", "Summer Trip", int64(len(fileBytes)), 5242880, 1700000000000, 0, 1, true, true, false),
			)
			_, _ = w.Write(resp)
		case "/download-urls":
			resp := encodeWireMsg(wfMsg(1, wfMsg(5, wfMsg(2,
				wfString(5, srv.URL+"/file/edited"),
				wfString(6, srv.URL+"/file/original"),
			))))
			_, _ = w.Write(resp)
		case "/file/original":
			_, _ = w.Write(fileBytes)
		case "/trash":
			node, _ := decodeWireMsg(body)
			action, _ := node.firstVarint(2)
			mu.Lock()
			trashActions = append(trashActions, action)
			mu.Unlock()
			_, _ = w.Write([]byte{})
		case "/upload-token":
			w.Header().Set("X-GUploader-UploadID", "up-token-1")
			w.WriteHeader(http.StatusOK)
		case "/upload-media":
			commitToken, _ := proto.Marshal(&generated.CommitToken{
				Field1: 2,
				Field2: []byte("tok-a"),
			})
			_, _ = w.Write(commitToken)
		case "/commit":
			var req generated.CommitUpload
			if err := proto.Unmarshal(body, &req); err != nil {
				t.Errorf("failed to unmarshal CommitUpload: %v", err)
			}
			mu.Lock()
			committedModel = req.GetField2().GetModel()
			committedQual = req.GetField1().GetQuality()
			committedName = req.GetField1().GetFileName()
			mu.Unlock()

			resp, _ := proto.Marshal(&generated.CreateMediaItemsResponse{
				Item: []*generated.CreateMediaItemResponseItem{
					{ResultItem: &generated.CreateMediaItemResult{MediaKey: "mk-new-unlimited"}},
				},
			})
			_, _ = w.Write(resp)
		case "/set-favorite":
			mu.Lock()
			favoriteSet = true
			mu.Unlock()
			_, _ = w.Write([]byte{})
		case "/set-archived":
			mu.Lock()
			archivedSet = true
			mu.Unlock()
			_, _ = w.Write([]byte{})
		case "/set-caption":
			node, _ := decodeWireMsg(body)
			capText, _ := node.firstString(2)
			mu.Lock()
			captionSet = capText
			mu.Unlock()
			_, _ = w.Write([]byte{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	downloadDir := t.TempDir()
	reporter := newRecordingReclaimReporter()
	mgr := NewReclaimManager(reporter, nil)
	mgr.newAPI = func(opts ApiOptions) (*Api, error) {
		if opts.UseQuota {
			t.Errorf("expected UseQuota=false during reclaim, got true")
		}
		return newTestAPIForServer(srv.URL, opts.Saver), nil
	}

	mgr.Run(ReclaimOptions{
		Api:               ApiOptions{UseQuota: true}, // must be forced false
		DownloadDir:       downloadDir,
		DeletePermanently: false,
		CleanupLocal:      false,
		RestoreMetadata:   true,
		Threads:           1,
	})

	summary := <-reporter.done
	if summary.ErrorMessage != "" || summary.Succeeded != 1 || summary.Failed != 0 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if summary.ReclaimedBytes != 5242880 {
		t.Fatalf("ReclaimedBytes = %d, want 5242880", summary.ReclaimedBytes)
	}

	res := summary.Results[0]
	if !res.Downloaded || !res.Trashed || res.DeletedPermanently || !res.Reuploaded || res.LocalCleanedUp {
		t.Fatalf("unexpected item result flags: %+v", res)
	}
	if res.NewMediaKey != "mk-new-unlimited" {
		t.Fatalf("NewMediaKey = %q, want mk-new-unlimited", res.NewMediaKey)
	}
	if _, err := os.Stat(res.LocalPath); err != nil {
		t.Fatalf("expected local backup to remain at %s: %v", res.LocalPath, err)
	}
	if filepath.Base(res.LocalPath) != "holiday.jpg" {
		t.Fatalf("local filename = %q, want holiday.jpg", filepath.Base(res.LocalPath))
	}

	mu.Lock()
	defer mu.Unlock()
	if len(trashActions) != 1 || trashActions[0] != 1 {
		t.Fatalf("trashActions = %v, want [1] (move to trash only)", trashActions)
	}
	if committedModel != "Pixel XL" || committedQual != 3 || committedName != "holiday.jpg" {
		t.Fatalf("committed = (%q, %d, %q), want (Pixel XL, 3, holiday.jpg)", committedModel, committedQual, committedName)
	}
	if !favoriteSet || !archivedSet || captionSet != "Summer Trip" {
		t.Fatalf("metadata restore mismatch: fav=%v arch=%v cap=%q", favoriteSet, archivedSet, captionSet)
	}
}

func TestReclaimManagerPermanentDeleteAndCleanupWithSaver(t *testing.T) {
	fileBytes := []byte("video-bytes-for-saver-reupload")
	var (
		mu             sync.Mutex
		trashActions   []int64
		committedModel string
		committedQual  int64
	)

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/download-urls":
			resp := encodeWireMsg(wfMsg(1, wfMsg(5, wfMsg(2, wfString(6, srv.URL+"/file/orig")))))
			_, _ = w.Write(resp)
		case "/file/orig":
			_, _ = w.Write(fileBytes)
		case "/trash":
			node, _ := decodeWireMsg(body)
			action, _ := node.firstVarint(2)
			mu.Lock()
			trashActions = append(trashActions, action)
			mu.Unlock()
			_, _ = w.Write([]byte{})
		case "/upload-token":
			w.Header().Set("X-GUploader-UploadID", "up-2")
			w.WriteHeader(http.StatusOK)
		case "/upload-media":
			commitToken, _ := proto.Marshal(&generated.CommitToken{Field1: 2, Field2: []byte("t")})
			_, _ = w.Write(commitToken)
		case "/commit":
			var req generated.CommitUpload
			_ = proto.Unmarshal(body, &req)
			mu.Lock()
			committedModel = req.GetField2().GetModel()
			committedQual = req.GetField1().GetQuality()
			mu.Unlock()
			resp, _ := proto.Marshal(&generated.CreateMediaItemsResponse{
				Item: []*generated.CreateMediaItemResponseItem{
					{ResultItem: &generated.CreateMediaItemResult{MediaKey: "mk-saver-new"}},
				},
			})
			_, _ = w.Write(resp)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	downloadDir := t.TempDir()
	reporter := newRecordingReclaimReporter()
	mgr := NewReclaimManager(reporter, nil)
	mgr.newAPI = func(opts ApiOptions) (*Api, error) {
		return newTestAPIForServer(srv.URL, opts.Saver), nil
	}

	mgr.Run(ReclaimOptions{
		Api:               ApiOptions{Saver: true},
		DownloadDir:       downloadDir,
		DeletePermanently: true,
		CleanupLocal:      true,
		Items: []RemoteMediaItem{
			{MediaKey: "mk-v1", DedupKey: "dk-v1", FileName: "clip.mp4", QuotaChargedBytes: 123456},
		},
	})

	summary := <-reporter.done
	if summary.Succeeded != 1 || summary.Failed != 0 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	res := summary.Results[0]
	if !res.Trashed || !res.DeletedPermanently || !res.Reuploaded || !res.LocalCleanedUp {
		t.Fatalf("unexpected item result: %+v", res)
	}
	if _, err := os.Stat(res.LocalPath); !os.IsNotExist(err) {
		t.Fatalf("expected local file to be cleaned up, stat err=%v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(trashActions) != 2 || trashActions[0] != 1 || trashActions[1] != 2 {
		t.Fatalf("trashActions = %v, want [1 2] (trash then permanent delete)", trashActions)
	}
	if committedModel != "Pixel 2" || committedQual != 1 {
		t.Fatalf("saver commit = (%q, %d), want (Pixel 2, 1)", committedModel, committedQual)
	}
}

func TestReclaimManagerRestoresFromTrashAndKeepsBackupOnUploadError(t *testing.T) {
	fileBytes := []byte("important-photo-must-not-be-lost")
	var (
		mu           sync.Mutex
		trashActions []int64
	)

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/download-urls":
			resp := encodeWireMsg(wfMsg(1, wfMsg(5, wfMsg(2, wfString(6, srv.URL+"/file/orig")))))
			_, _ = w.Write(resp)
		case "/file/orig":
			_, _ = w.Write(fileBytes)
		case "/trash":
			node, _ := decodeWireMsg(body)
			action, _ := node.firstVarint(2)
			mu.Lock()
			trashActions = append(trashActions, action)
			mu.Unlock()
			_, _ = w.Write([]byte{})
		case "/upload-token":
			http.Error(w, "simulated upload failure", http.StatusBadRequest)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	downloadDir := t.TempDir()
	reporter := newRecordingReclaimReporter()
	mgr := NewReclaimManager(reporter, nil)
	mgr.newAPI = func(opts ApiOptions) (*Api, error) {
		return newTestAPIForServer(srv.URL, opts.Saver), nil
	}

	mgr.Run(ReclaimOptions{
		DownloadDir:       downloadDir,
		DeletePermanently: false,
		CleanupLocal:      true, // must NOT delete local backup when upload fails!
		Items: []RemoteMediaItem{
			{MediaKey: "mk-safe", DedupKey: "dk-safe", FileName: "family.jpg", QuotaChargedBytes: 2048},
		},
	})

	summary := <-reporter.done
	if summary.Succeeded != 0 || summary.Failed != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	res := summary.Results[0]
	if !res.IsError || !res.Trashed || !res.RestoredFromTrash || res.LocalCleanedUp {
		t.Fatalf("expected rollback + preserved local backup, got %+v", res)
	}
	if _, err := os.Stat(res.LocalPath); err != nil {
		t.Fatalf("local backup must be kept on upload error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(trashActions) != 2 || trashActions[0] != 1 || trashActions[1] != 3 {
		t.Fatalf("trashActions = %v, want [1 3] (trash then restore)", trashActions)
	}
}

func TestSanitizePathSegment(t *testing.T) {
	if got := sanitizePathSegment("../../etc/passwd", "fallback"); got != "passwd" {
		t.Fatalf("got %q, want passwd", got)
	}
	if got := sanitizePathSegment("..", "fallback"); got != "fallback" {
		t.Fatalf("got %q, want fallback", got)
	}
	if got := sanitizePathSegment("a:b*c?d.jpg", "fallback"); got != "a_b_c_d.jpg" {
		t.Fatalf("got %q, want a_b_c_d.jpg", got)
	}
}

func TestCommitUploadRetriesOn409Conflict(t *testing.T) {
	var commitAttempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/commit" {
			http.NotFound(w, r)
			return
		}
		commitAttempts++
		if commitAttempts < 3 {
			http.Error(w, "The operation was aborted.", http.StatusConflict)
			return
		}
		respProto := &generated.CreateMediaItemsResponse{
			Item: []*generated.CreateMediaItemResponseItem{
				{
					ResultItem: &generated.CreateMediaItemResult{
						MediaKey: "mk-after-409-retry",
					},
				},
			},
		}
		out, _ := proto.Marshal(respProto)
		_, _ = w.Write(out)
	}))
	defer srv.Close()

	api := newTestAPIForServer(srv.URL, false)
	api.commitRetryConfig = &RetryConfig{
		MaxRetries:   3,
		InitialDelay: time.Millisecond,
		MaxDelay:     2 * time.Millisecond,
	}

	mk, err := api.CommitUpload(&generated.CommitToken{Field1: 2, Field2: []byte("f2")}, "IMG_3555.HEIC", []byte("12345678901234567890"), 1700000000)
	if err != nil {
		t.Fatalf("expected CommitUpload to succeed after 409 retries, got err: %v", err)
	}
	if mk != "mk-after-409-retry" || commitAttempts != 3 {
		t.Fatalf("got mediaKey=%q attempts=%d, want mk-after-409-retry attempts=3", mk, commitAttempts)
	}
}
