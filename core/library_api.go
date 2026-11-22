package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	photosLibraryStateEndpoint    = "https://photosdata-pa.googleapis.com/6439526531001121323/18047484249733410717"
	photosTrashActionEndpoint     = "https://photosdata-pa.googleapis.com/6439526531001121323/17490284929287180316"
	photosPrepareDownloadEndpoint = "https://photosdata-pa.googleapis.com/$rpc/social.frontend.photos.preparedownloaddata.v1.PhotosPrepareDownloadDataService/PhotosPrepareDownload"
	photosSetCaptionEndpoint      = "https://photosdata-pa.googleapis.com/6439526531001121323/1552790390512470739"
	photosSetFavoriteEndpoint     = "https://photosdata-pa.googleapis.com/6439526531001121323/5144645502632292153"
	photosSetArchivedEndpoint     = "https://photosdata-pa.googleapis.com/6439526531001121323/6715446385130606868"
)

// RemoteMediaItem describes a media item discovered in the remote Google Photos library.
type RemoteMediaItem struct {
	MediaKey          string `json:"mediaKey"`
	DedupKey          string `json:"dedupKey"`
	FileName          string `json:"fileName"`
	Caption           string `json:"caption,omitempty"`
	Type              int    `json:"type"`
	SizeBytes         int64  `json:"sizeBytes"`
	QuotaChargedBytes int64  `json:"quotaChargedBytes"`
	UTCTimestamp      int64  `json:"utcTimestamp"`
	Origin            string `json:"origin"`
	TrashTimestamp    int64  `json:"trashTimestamp,omitempty"`
	IsArchived        bool   `json:"isArchived,omitempty"`
	IsFavorite        bool   `json:"isFavorite,omitempty"`
	IsLocked          bool   `json:"isLocked,omitempty"`
	IsOriginalQuality bool   `json:"isOriginalQuality,omitempty"`
	IsEdited          bool   `json:"isEdited,omitempty"`
	IsMicroVideo      bool   `json:"isMicroVideo,omitempty"`
}

// LibraryPage holds a single page returned by Google Photos library sync RPCs.
type LibraryPage struct {
	SyncToken   string
	ResumeToken string
	Items       []RemoteMediaItem
}

// LibraryScanStats summarizes a remote library scan for quota-consuming media.
type LibraryScanStats struct {
	PagesScanned     int   `json:"pagesScanned"`
	TotalScanned     int   `json:"totalScanned"`
	EligibleCount    int   `json:"eligibleCount"`
	TotalQuotaBytes  int64 `json:"totalQuotaBytes"`
	SkippedTrashed   int   `json:"skippedTrashed"`
	SkippedLocked    int   `json:"skippedLocked"`
	SkippedNotOwned  int   `json:"skippedNotOwned"`
	SkippedNoDedupID int   `json:"skippedNoDedupId"`
	// SkippedDuplicate counts extra entries sharing a DedupKey (same file
	// content) with an already selected item. Trash/delete RPCs act on the
	// DedupKey, so each unique file must be reclaimed exactly once.
	SkippedDuplicate int `json:"skippedDuplicate"`
}

// DownloadURLs holds the remote download URLs for a media item.
type DownloadURLs struct {
	OriginalURL string
	EditedURL   string
}

// BestURL returns the original download URL when available, falling back to the edited URL.
func (d DownloadURLs) BestURL() string {
	if d.OriginalURL != "" {
		return d.OriginalURL
	}
	return d.EditedURL
}

func (a *Api) postPhotosRPC(ctx context.Context, endpoint string, payload []byte, includeExtHeaders bool) ([]byte, error) {
	retryConfig := DefaultRetryConfig()
	var lastErr error

	for attempt := 0; attempt <= retryConfig.MaxRetries; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt > 0 {
			delay := CalculateBackoff(attempt-1, retryConfig)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		bearerToken, err := a.BearerToken()
		if err != nil {
			lastErr = fmt.Errorf("failed to get bearer token: %w", err)
			continue
		}

		req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Accept-Encoding", "gzip")
		req.Header.Set("Accept-Language", a.language)
		req.Header.Set("Content-Type", "application/x-protobuf")
		req.Header.Set("User-Agent", a.userAgent)
		req.Header.Set("Authorization", "Bearer "+bearerToken)
		if includeExtHeaders {
			req.Header.Set("x-goog-ext-173412678-bin", "CgcIAhClARgC")
			req.Header.Set("x-goog-ext-174067345-bin", "CgIIAg==")
		}

		resp, err := a.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("request failed: %w", err)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}

		bodyBytes, readErr := ReadResponseBody(resp)
		_ = resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if readErr != nil {
				return nil, fmt.Errorf("failed to read response body: %w", readErr)
			}
			return bodyBytes, nil
		}

		lastErr = fmt.Errorf("request failed with status %d: %s", resp.StatusCode, string(bodyBytes))
		if !ShouldRetry(resp, nil) {
			return nil, lastErr
		}
	}

	return nil, fmt.Errorf("RPC failed after %d attempts: %w", retryConfig.MaxRetries+1, lastErr)
}

// GetLibraryState fetches the first page of the remote Google Photos library state.
func (a *Api) GetLibraryState(ctx context.Context, syncToken string) (LibraryPage, error) {
	endpoint := a.libraryStateEndpoint
	if endpoint == "" {
		endpoint = photosLibraryStateEndpoint
	}
	payload := buildGetLibraryStatePayload(syncToken)
	body, err := a.postPhotosRPC(ctx, endpoint, payload, true)
	if err != nil {
		return LibraryPage{}, fmt.Errorf("get library state: %w", err)
	}
	return parseLibraryPageResponse(body)
}

// GetLibraryPageInit fetches subsequent pages during an initial library scan using resumeToken.
func (a *Api) GetLibraryPageInit(ctx context.Context, resumeToken string) (LibraryPage, error) {
	endpoint := a.libraryStateEndpoint
	if endpoint == "" {
		endpoint = photosLibraryStateEndpoint
	}
	payload := buildGetLibraryPageInitPayload(resumeToken)
	body, err := a.postPhotosRPC(ctx, endpoint, payload, true)
	if err != nil {
		return LibraryPage{}, fmt.Errorf("get library page: %w", err)
	}
	return parseLibraryPageResponse(body)
}

// ScanQuotaConsumingMedia paginates through the remote library and returns all
// eligible items currently consuming Google Photos storage quota, sorted by
// QuotaChargedBytes descending.
func (a *Api) ScanQuotaConsumingMedia(
	ctx context.Context,
	onPage func(stats LibraryScanStats),
) ([]RemoteMediaItem, LibraryScanStats, error) {
	var stats LibraryScanStats
	seenMedia := make(map[string]struct{})
	seenTokens := make(map[string]struct{})
	eligibleByDedup := make(map[string]int)
	var eligible []RemoteMediaItem

	processPage := func(page LibraryPage) {
		stats.PagesScanned++
		for _, item := range page.Items {
			if item.MediaKey == "" {
				continue
			}
			if _, dup := seenMedia[item.MediaKey]; dup {
				continue
			}
			seenMedia[item.MediaKey] = struct{}{}
			stats.TotalScanned++

			if item.QuotaChargedBytes <= 0 {
				continue
			}
			if item.TrashTimestamp > 0 {
				stats.SkippedTrashed++
				continue
			}
			if item.IsLocked {
				stats.SkippedLocked++
				continue
			}
			if item.Origin != "" && item.Origin != "self" {
				stats.SkippedNotOwned++
				continue
			}
			if item.DedupKey == "" {
				stats.SkippedNoDedupID++
				continue
			}
			if idx, dup := eligibleByDedup[item.DedupKey]; dup {
				mergeDuplicateMediaItem(&eligible[idx], item)
				stats.SkippedDuplicate++
				continue
			}

			eligibleByDedup[item.DedupKey] = len(eligible)
			eligible = append(eligible, item)
			stats.EligibleCount++
			stats.TotalQuotaBytes += item.QuotaChargedBytes
		}
		if onPage != nil {
			onPage(stats)
		}
	}

	page, err := a.GetLibraryState(ctx, "")
	if err != nil {
		return nil, stats, err
	}
	processPage(page)

	resumeToken := page.ResumeToken
	for resumeToken != "" {
		if ctx.Err() != nil {
			return nil, stats, ctx.Err()
		}
		if _, looped := seenTokens[resumeToken]; looped {
			break
		}
		seenTokens[resumeToken] = struct{}{}

		nextPage, err := a.GetLibraryPageInit(ctx, resumeToken)
		if err != nil {
			return nil, stats, err
		}
		processPage(nextPage)
		resumeToken = nextPage.ResumeToken
	}

	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].QuotaChargedBytes != eligible[j].QuotaChargedBytes {
			return eligible[i].QuotaChargedBytes > eligible[j].QuotaChargedBytes
		}
		return eligible[i].UTCTimestamp > eligible[j].UTCTimestamp
	})

	return eligible, stats, nil
}

// mergeDuplicateMediaItem folds metadata from dup (same DedupKey, i.e. same file
// content) into dst so the single reclaimed copy keeps favorite, archived, and
// caption flags present on any of the duplicate entries.
func mergeDuplicateMediaItem(dst *RemoteMediaItem, dup RemoteMediaItem) {
	dst.IsFavorite = dst.IsFavorite || dup.IsFavorite
	dst.IsArchived = dst.IsArchived || dup.IsArchived
	if dst.Caption == "" {
		dst.Caption = dup.Caption
	}
	if dup.QuotaChargedBytes > dst.QuotaChargedBytes {
		dst.QuotaChargedBytes = dup.QuotaChargedBytes
	}
}

// dedupeMediaItemsByDedupKey keeps the first item for each DedupKey, merging
// metadata from later duplicates, and preserves the original order.
func dedupeMediaItemsByDedupKey(items []RemoteMediaItem) ([]RemoteMediaItem, int) {
	out := make([]RemoteMediaItem, 0, len(items))
	index := make(map[string]int, len(items))
	dups := 0
	for _, item := range items {
		key := item.DedupKey
		if key == "" {
			key = item.MediaKey
		}
		if idx, ok := index[key]; ok {
			mergeDuplicateMediaItem(&out[idx], item)
			dups++
			continue
		}
		index[key] = len(out)
		out = append(out, item)
	}
	return out, dups
}

// GetDownloadURLs fetches direct download URLs (original and edited) for a media item.
func (a *Api) GetDownloadURLs(ctx context.Context, mediaKey string) (DownloadURLs, error) {
	if mediaKey == "" {
		return DownloadURLs{}, errors.New("media key is empty")
	}
	endpoint := a.downloadURLsEndpoint
	if endpoint == "" {
		endpoint = photosPrepareDownloadEndpoint
	}
	payload := buildGetDownloadURLsPayload(mediaKey)
	body, err := a.postPhotosRPC(ctx, endpoint, payload, true)
	if err != nil {
		return DownloadURLs{}, fmt.Errorf("get download URLs for %s: %w", mediaKey, err)
	}
	urls, err := parseDownloadURLsResponse(body)
	if err != nil {
		return DownloadURLs{}, fmt.Errorf("parse download URLs for %s: %w", mediaKey, err)
	}
	if urls.BestURL() == "" {
		return DownloadURLs{}, fmt.Errorf("no download URL returned for %s", mediaKey)
	}
	return urls, nil
}

// DownloadMediaFile downloads a remote media file to destPath atomically via a temporary
// .part file. It retries transient failures and verifies that at least 1 byte was written.
func (a *Api) DownloadMediaFile(
	ctx context.Context,
	downloadURL string,
	destPath string,
	onProgress UploadProgressCallback,
) (int64, error) {
	if downloadURL == "" {
		return 0, errors.New("download URL is empty")
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return 0, fmt.Errorf("create download directory: %w", err)
	}

	retryConfig := DefaultRetryConfig()
	var lastErr error

	for attempt := 0; attempt <= retryConfig.MaxRetries; attempt++ {
		attemptNum := attempt + 1
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if attempt > 0 {
			delay := CalculateBackoff(attempt-1, retryConfig)
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(delay):
			}
		}

		n, retryable, err := a.doDownloadAttempt(ctx, downloadURL, destPath, attemptNum, onProgress)
		if err == nil {
			return n, nil
		}
		lastErr = err
		if !retryable || ctx.Err() != nil {
			return 0, err
		}
	}

	return 0, fmt.Errorf("download failed after %d attempts: %w", retryConfig.MaxRetries+1, lastErr)
}

func (a *Api) doDownloadAttempt(
	ctx context.Context,
	downloadURL string,
	destPath string,
	attemptNum int,
	onProgress UploadProgressCallback,
) (int64, bool, error) {
	bearerToken, err := a.BearerToken()
	if err != nil {
		return 0, true, fmt.Errorf("failed to get bearer token: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
	if err != nil {
		return 0, false, fmt.Errorf("failed to create download request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearerToken)
	req.Header.Set("User-Agent", a.userAgent)

	resp, err := a.client.Do(req)
	if err != nil {
		return 0, true, fmt.Errorf("download request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := ReadResponseBody(resp)
		return 0, ShouldRetry(resp, nil), fmt.Errorf("download failed with status %d: %s", resp.StatusCode, string(body))
	}

	partPath := destPath + ".part"
	f, err := os.Create(partPath)
	if err != nil {
		return 0, false, fmt.Errorf("create temp download file: %w", err)
	}
	cleanupPart := true
	defer func() {
		if cleanupPart {
			_ = os.Remove(partPath)
		}
	}()

	total := resp.ContentLength
	var reader io.Reader = resp.Body
	if onProgress != nil {
		onProgress(0, total, attemptNum)
		reader = NewProgressReader(resp.Body, total, func(bytesRead, bytesTotal int64) {
			onProgress(bytesRead, bytesTotal, attemptNum)
		})
	}

	written, copyErr := io.Copy(f, reader)
	closeErr := f.Close()
	if copyErr != nil {
		return 0, true, fmt.Errorf("write download file: %w", copyErr)
	}
	if closeErr != nil {
		return 0, false, fmt.Errorf("close download file: %w", closeErr)
	}
	if written <= 0 {
		return 0, true, errors.New("downloaded file is empty (0 bytes)")
	}
	if total > 0 && written != total {
		return 0, true, fmt.Errorf("incomplete download: got %d bytes, expected %d", written, total)
	}

	if err := os.Rename(partPath, destPath); err != nil {
		return 0, false, fmt.Errorf("finalize download file: %w", err)
	}
	cleanupPart = false
	return written, false, nil
}

// MoveRemoteMediaToTrash moves remote media items identified by dedupKeys into Google Photos Trash.
func (a *Api) MoveRemoteMediaToTrash(ctx context.Context, dedupKeys []string) error {
	if len(dedupKeys) == 0 {
		return nil
	}
	endpoint := a.trashEndpoint
	if endpoint == "" {
		endpoint = photosTrashActionEndpoint
	}
	payload := buildMoveToTrashPayload(dedupKeys, a.clientVersionCode, a.androidAPIVersion)
	if _, err := a.postPhotosRPC(ctx, endpoint, payload, false); err != nil {
		return fmt.Errorf("move media to trash: %w", err)
	}
	return nil
}

// DeleteRemoteMediaPermanently permanently deletes remote media items (removing them from Trash).
func (a *Api) DeleteRemoteMediaPermanently(ctx context.Context, dedupKeys []string) error {
	if len(dedupKeys) == 0 {
		return nil
	}
	endpoint := a.trashEndpoint
	if endpoint == "" {
		endpoint = photosTrashActionEndpoint
	}
	payload := buildDeletePermanentlyPayload(dedupKeys)
	if _, err := a.postPhotosRPC(ctx, endpoint, payload, false); err != nil {
		return fmt.Errorf("permanently delete media: %w", err)
	}
	return nil
}

// RestoreRemoteMediaFromTrash restores remote media items from Trash back into the library.
func (a *Api) RestoreRemoteMediaFromTrash(ctx context.Context, dedupKeys []string) error {
	if len(dedupKeys) == 0 {
		return nil
	}
	endpoint := a.trashEndpoint
	if endpoint == "" {
		endpoint = photosTrashActionEndpoint
	}
	payload := buildRestoreFromTrashPayload(dedupKeys, a.clientVersionCode, a.androidAPIVersion)
	if _, err := a.postPhotosRPC(ctx, endpoint, payload, true); err != nil {
		return fmt.Errorf("restore media from trash: %w", err)
	}
	return nil
}

// SetItemCaption sets the caption/description for a remote media item.
func (a *Api) SetItemCaption(ctx context.Context, dedupKey, caption string) error {
	if dedupKey == "" {
		return errors.New("dedup key is empty")
	}
	endpoint := a.setCaptionEndpoint
	if endpoint == "" {
		endpoint = photosSetCaptionEndpoint
	}
	payload := encodeWireMsg(
		wfString(2, caption),
		wfString(3, dedupKey),
	)
	if _, err := a.postPhotosRPC(ctx, endpoint, payload, true); err != nil {
		return fmt.Errorf("set caption: %w", err)
	}
	return nil
}

// SetFavorite marks or unmarks a remote media item as favorite.
func (a *Api) SetFavorite(ctx context.Context, dedupKey string, isFavorite bool) error {
	if dedupKey == "" {
		return errors.New("dedup key is empty")
	}
	endpoint := a.setFavoriteEndpoint
	if endpoint == "" {
		endpoint = photosSetFavoriteEndpoint
	}
	action := uint64(2)
	if isFavorite {
		action = 1
	}
	payload := encodeWireMsg(
		wfMsg(1, wfString(2, dedupKey)),
		wfMsg(2, wfVarint(1, action)),
		wfMsg(3, wfMsg(1, wfEmptyMsg(19))),
	)
	if _, err := a.postPhotosRPC(ctx, endpoint, payload, true); err != nil {
		return fmt.Errorf("set favorite: %w", err)
	}
	return nil
}

// SetArchived marks or unmarks remote media items as archived.
func (a *Api) SetArchived(ctx context.Context, dedupKeys []string, isArchived bool) error {
	if len(dedupKeys) == 0 {
		return nil
	}
	endpoint := a.setArchivedEndpoint
	if endpoint == "" {
		endpoint = photosSetArchivedEndpoint
	}
	action := uint64(2)
	if isArchived {
		action = 1
	}
	fields := make([]wireField, 0, len(dedupKeys)+1)
	for _, key := range dedupKeys {
		fields = append(fields, wfMsg(1,
			wfString(1, key),
			wfMsg(2, wfVarint(1, action)),
		))
	}
	fields = append(fields, wfVarint(3, 1))
	if _, err := a.postPhotosRPC(ctx, endpoint, encodeWireMsg(fields...), true); err != nil {
		return fmt.Errorf("set archived: %w", err)
	}
	return nil
}

// urlSafeBase64SHA1 converts raw SHA-1 bytes to Google Photos' URL-safe base64 dedup key.
func urlSafeBase64SHA1(sha1Bytes []byte) string {
	return base64.RawURLEncoding.EncodeToString(sha1Bytes)
}

func buildMoveToTrashPayload(dedupKeys []string, clientVersionCode, androidAPIVersion int64) []byte {
	fields := make([]wireField, 0, len(dedupKeys)+4)
	fields = append(fields, wfVarint(2, 1))
	fields = append(fields, wfRepeatedString(3, dedupKeys...)...)
	fields = append(fields,
		wfVarint(4, 1),
		wfMsg(8, wfMsg(4,
			wfEmptyMsg(2),
			wfMsg(3, wfEmptyMsg(1)),
			wfEmptyMsg(4),
			wfMsg(5, wfEmptyMsg(1)),
		)),
		wfMsg(9,
			wfVarint(1, 5),
			wfMsg(2,
				wfVarint(1, uint64(clientVersionCode)),
				wfString(2, strconv.FormatInt(androidAPIVersion, 10)),
			),
		),
	)
	return encodeWireMsg(fields...)
}

func buildDeletePermanentlyPayload(dedupKeys []string) []byte {
	fields := make([]wireField, 0, len(dedupKeys)+4)
	fields = append(fields, wfVarint(2, 2))
	fields = append(fields, wfRepeatedString(3, dedupKeys...)...)
	fields = append(fields,
		wfVarint(4, 2),
		wfMsg(8, wfMsg(4,
			wfEmptyMsg(2),
			wfMsg(3, wfEmptyMsg(1)),
			wfEmptyMsg(4),
			wfMsg(5, wfEmptyMsg(1)),
		)),
		wfString(9, ""),
	)
	return encodeWireMsg(fields...)
}

func buildRestoreFromTrashPayload(dedupKeys []string, clientVersionCode, androidAPIVersion int64) []byte {
	fields := make([]wireField, 0, len(dedupKeys)+4)
	fields = append(fields, wfVarint(2, 3))
	fields = append(fields, wfRepeatedString(3, dedupKeys...)...)
	fields = append(fields,
		wfVarint(4, 2),
		wfMsg(8, wfMsg(4,
			wfEmptyMsg(2),
			wfMsg(3, wfEmptyMsg(1)),
		)),
		wfMsg(9,
			wfVarint(1, 5),
			wfMsg(2,
				wfVarint(1, uint64(clientVersionCode)),
				wfString(2, strconv.FormatInt(androidAPIVersion, 10)),
			),
		),
	)
	return encodeWireMsg(fields...)
}

func buildGetDownloadURLsPayload(mediaKey string) []byte {
	return encodeWireMsg(
		wfMsg(1, wfMsg(1, wfString(1, mediaKey))),
		wfMsg(2,
			wfMsg(1, wfMsg(7, wfEmptyMsg(2))),
			wfMsg(5,
				wfEmptyMsg(2),
				wfEmptyMsg(3),
				wfMsg(5, wfEmptyMsg(1), wfVarint(3, 0)),
			),
		),
	)
}

func parseDownloadURLsResponse(body []byte) (DownloadURLs, error) {
	root, err := decodeWireMsg(body)
	if err != nil {
		return DownloadURLs{}, err
	}
	var out DownloadURLs
	if edited, ok := root.nestedString(1, 5, 2, 5); ok && strings.HasPrefix(edited, "http") {
		out.EditedURL = edited
	}
	if orig, ok := root.nestedString(1, 5, 2, 6); ok && strings.HasPrefix(orig, "http") {
		out.OriginalURL = orig
	}
	if out.BestURL() == "" {
		// Fallback recursive scan if the server returns URLs in a different sub-field.
		var found []string
		collectHTTPURLs(root, &found, 0)
		if len(found) > 0 {
			out.OriginalURL = found[len(found)-1]
			if len(found) > 1 {
				out.EditedURL = found[0]
			}
		}
	}
	return out, nil
}

func collectHTTPURLs(node wireNode, out *[]string, depth int) {
	if node == nil || depth > 8 {
		return
	}
	for _, vals := range node {
		for _, v := range vals {
			if v.wireType != 2 || len(v.bytesVal) == 0 {
				continue
			}
			s := string(v.bytesVal)
			if strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://") {
				*out = append(*out, s)
				continue
			}
			if child, err := decodeWireMsg(v.bytesVal); err == nil && len(child) > 0 {
				collectHTTPURLs(child, out, depth+1)
			}
		}
	}
}

func parseLibraryPageResponse(body []byte) (LibraryPage, error) {
	outer, err := decodeWireMsg(body)
	if err != nil {
		return LibraryPage{}, fmt.Errorf("decode library response: %w", err)
	}
	root, ok := outer.firstMsg(1)
	if !ok {
		return LibraryPage{}, nil
	}

	var page LibraryPage
	page.ResumeToken, _ = root.firstString(1)
	page.SyncToken, _ = root.firstString(6)

	for _, entry := range root.allMsgs(2) {
		item, ok := parseRemoteMediaItem(entry)
		if ok {
			page.Items = append(page.Items, item)
		}
	}
	return page, nil
}

func parseRemoteMediaItem(d wireNode) (RemoteMediaItem, bool) {
	mediaKey, ok := d.firstString(1)
	if !ok || mediaKey == "" {
		return RemoteMediaItem{}, false
	}

	d2, _ := d.firstMsg(2)
	d5, _ := d.firstMsg(5)

	dedupKey := ""
	if dedupContainer, ok := d2.firstMsg(21); ok {
		if k, ok := dedupContainer.firstString(1); ok && k != "" {
			dedupKey = k
		}
	}
	if dedupKey == "" {
		if hashBytes, ok := d2.nestedBytes(13, 1); ok && len(hashBytes) > 0 {
			dedupKey = urlSafeBase64SHA1(hashBytes)
		}
	}
	if dedupKey == "" {
		dedupKey = mediaKey
	}

	fileName, _ := d2.firstString(4)
	if fileName == "" {
		fileName = mediaKey + ".bin"
	}
	caption, _ := d2.firstString(3)
	sizeBytes, _ := d2.firstVarint(10)
	utcTimestamp, _ := d2.firstVarint(7)
	quotaChargedBytes, _ := d2.nestedVarint(35, 2)
	storagePolicy, _ := d2.nestedVarint(35, 3)
	trashTimestamp, _ := d2.nestedVarint(16, 3)
	archivedVal, _ := d2.nestedVarint(29, 1)
	favoriteVal, _ := d2.nestedVarint(31, 1)
	lockedVal, _ := d2.nestedVarint(39, 1)
	originVal, hasOrigin := d2.nestedVarint(30, 1)
	origin := "self"
	if hasOrigin {
		switch originVal {
		case 3:
			origin = "partner"
		case 4:
			origin = "shared"
		}
	}

	mediaType, _ := d5.firstVarint(1)
	isEdited := false
	if photoData, ok := d5.firstMsg(2); ok && photoData.has(4) {
		isEdited = true
	}
	_, isMicroVideo := d5.nestedMsg(5, 2, 4)

	return RemoteMediaItem{
		MediaKey:          mediaKey,
		DedupKey:          dedupKey,
		FileName:          fileName,
		Caption:           caption,
		Type:              int(mediaType),
		SizeBytes:         sizeBytes,
		QuotaChargedBytes: quotaChargedBytes,
		UTCTimestamp:      utcTimestamp,
		Origin:            origin,
		TrashTimestamp:    trashTimestamp,
		IsArchived:        archivedVal == 1,
		IsFavorite:        favoriteVal == 1,
		IsLocked:          lockedVal == 1,
		IsOriginalQuality: storagePolicy == 2,
		IsEdited:          isEdited,
		IsMicroVideo:      isMicroVideo,
	}, true
}

func commonLibraryField1_1_1() wireField {
	return wfMsg(1,
		wfEmptyMsg(1),
		wfEmptyMsg(3),
		wfEmptyMsg(4),
		wfMsg(5,
			wfEmptyMsg(1),
			wfEmptyMsg(2),
			wfEmptyMsg(3),
			wfEmptyMsg(4),
			wfEmptyMsg(5),
			wfEmptyMsg(7),
		),
		wfEmptyMsg(6),
		wfMsg(7, wfEmptyMsg(2)),
		wfEmptyMsg(15),
		wfEmptyMsg(16),
		wfEmptyMsg(17),
		wfEmptyMsg(19),
		wfEmptyMsg(20),
		wfMsg(21,
			wfMsg(5, wfEmptyMsg(3)),
			wfEmptyMsg(6),
		),
		wfEmptyMsg(25),
		wfMsg(30, wfEmptyMsg(2)),
		wfEmptyMsg(31),
		wfEmptyMsg(32),
		wfMsg(33, wfEmptyMsg(1)),
		wfEmptyMsg(34),
		wfEmptyMsg(36),
		wfEmptyMsg(37),
		wfEmptyMsg(38),
		wfEmptyMsg(39),
		wfEmptyMsg(40),
		wfEmptyMsg(41),
	)
}

func commonPrintingPromoOptions() wireField {
	fields := wfRepeatedVarint(4, 2, 1, 6, 8, 10, 15, 18, 13, 17, 19, 14, 20)
	fields = append(fields,
		wfVarint(5, 6),
		wfVarint(6, 2),
		wfVarint(7, 1),
		wfVarint(8, 2),
		wfVarint(11, 3),
		wfVarint(12, 1),
		wfVarint(13, 3),
		wfVarint(15, 1),
		wfVarint(16, 1),
		wfVarint(17, 1),
		wfVarint(18, 2),
	)
	return wfMsg(1, fields...)
}

func commonLibraryField1_2(includeExtras bool) wireField {
	fields := []wireField{
		wfMsg(1,
			wfEmptyMsg(2),
			wfEmptyMsg(3),
			wfEmptyMsg(4),
			wfEmptyMsg(5),
			wfMsg(6,
				wfEmptyMsg(1),
				wfEmptyMsg(2),
				wfEmptyMsg(3),
				wfEmptyMsg(4),
				wfEmptyMsg(5),
				wfEmptyMsg(7),
			),
			wfEmptyMsg(7),
			wfEmptyMsg(8),
			wfEmptyMsg(10),
			wfEmptyMsg(12),
			wfMsg(13, wfEmptyMsg(2), wfEmptyMsg(3)),
			wfMsg(15, wfEmptyMsg(1)),
			wfEmptyMsg(18),
		),
		wfMsg(4, wfEmptyMsg(1)),
		wfEmptyMsg(9),
		wfMsg(11, wfMsg(1,
			wfEmptyMsg(1),
			wfEmptyMsg(4),
			wfEmptyMsg(5),
			wfEmptyMsg(6),
			wfEmptyMsg(9),
		)),
		wfMsg(14, wfMsg(1,
			wfMsg(1,
				wfEmptyMsg(1),
				wfMsg(2, wfMsg(2, wfMsg(1, wfEmptyMsg(1)), wfEmptyMsg(3))),
				wfMsg(3,
					wfMsg(4, wfMsg(1, wfEmptyMsg(1)), wfEmptyMsg(3)),
					wfMsg(5, wfMsg(1, wfEmptyMsg(1)), wfEmptyMsg(3)),
				),
			),
			wfEmptyMsg(2),
		)),
		wfEmptyMsg(17),
		wfMsg(18, wfEmptyMsg(1), wfMsg(2, wfEmptyMsg(1))),
		wfMsg(20, wfMsg(2, wfEmptyMsg(1), wfEmptyMsg(2))),
	}
	if includeExtras {
		fields = append(fields, wfEmptyMsg(22), wfEmptyMsg(23), wfEmptyMsg(24))
	} else {
		fields = append(fields, wfEmptyMsg(23))
	}
	return wfMsg(2, fields...)
}

func commonLibraryField1_19() wireField {
	return wfMsg(19,
		wfMsg(1, wfEmptyMsg(1), wfEmptyMsg(2)),
		wfMsg(2, wfRepeatedVarint(1, 1, 2, 4, 6, 5, 7)...),
		wfMsg(3, wfEmptyMsg(1), wfEmptyMsg(2)),
		wfMsg(5, wfEmptyMsg(1), wfEmptyMsg(2)),
		wfMsg(6, wfEmptyMsg(1)),
		wfMsg(7, wfEmptyMsg(1), wfEmptyMsg(2)),
		wfMsg(8, wfEmptyMsg(1)),
	)
}

func commonLibraryOuterField2() wireField {
	return wfMsg(2,
		wfMsg(1, wfMsg(1, wfMsg(1, wfEmptyMsg(1)), wfEmptyMsg(2))),
		wfEmptyMsg(2),
	)
}

func buildGetLibraryStatePayload(syncToken string) []byte {
	f1_1 := wfMsg(1,
		commonLibraryField1_1_1(),
		wfMsg(5,
			wfMsg(2,
				wfMsg(2, wfMsg(3, wfEmptyMsg(2)), wfMsg(4, wfEmptyMsg(2), wfEmptyMsg(4))),
				wfMsg(4, wfMsg(2, wfVarint(2, 1))),
				wfMsg(5, wfEmptyMsg(2)),
				wfVarint(6, 1),
			),
			wfMsg(3,
				wfMsg(2, wfEmptyMsg(3), wfEmptyMsg(4)),
				wfMsg(3, wfEmptyMsg(2), wfMsg(3, wfVarint(2, 1), wfEmptyMsg(3))),
				wfEmptyMsg(4),
				wfMsg(5, wfMsg(2, wfVarint(2, 1))),
				wfEmptyMsg(7),
			),
			wfMsg(4, wfMsg(2, wfEmptyMsg(2))),
			wfMsg(5,
				wfMsg(1,
					wfMsg(2, wfEmptyMsg(3), wfEmptyMsg(4)),
					wfMsg(3, wfEmptyMsg(2), wfMsg(3, wfVarint(2, 1), wfEmptyMsg(3))),
				),
				wfVarint(3, 1),
			),
		),
		wfEmptyMsg(8),
		wfMsg(9,
			wfEmptyMsg(2),
			wfMsg(3, wfEmptyMsg(1), wfEmptyMsg(2)),
			wfMsg(4, wfMsg(1,
				wfMsg(3, wfMsg(1, wfMsg(1,
					wfMsg(5, wfEmptyMsg(1)),
					wfEmptyMsg(6),
					wfEmptyMsg(7),
				), wfEmptyMsg(2), wfMsg(3, wfMsg(1,
					wfMsg(5, wfEmptyMsg(1)),
					wfEmptyMsg(6),
					wfEmptyMsg(7),
				), wfEmptyMsg(2)))),
				wfMsg(4, wfMsg(1, wfEmptyMsg(2))),
			)),
		),
		wfMsg(11, wfEmptyMsg(2), wfEmptyMsg(3), wfMsg(4, wfMsg(2, wfVarint(1, 1), wfVarint(2, 2)))),
		wfEmptyMsg(12),
		wfMsg(14, wfEmptyMsg(2), wfEmptyMsg(3), wfMsg(4, wfMsg(2, wfVarint(1, 1), wfVarint(2, 2)))),
		wfMsg(15, wfEmptyMsg(1), wfEmptyMsg(4)),
		wfMsg(17, wfEmptyMsg(1), wfEmptyMsg(4)),
		wfMsg(19, wfEmptyMsg(2), wfEmptyMsg(3), wfMsg(4, wfMsg(2, wfVarint(1, 1), wfVarint(2, 2)))),
		wfMsg(21, wfEmptyMsg(1)),
		wfEmptyMsg(22),
		wfEmptyMsg(23),
		wfEmptyMsg(24),
	)

	f1_3 := wfMsg(3,
		wfEmptyMsg(2),
		wfMsg(3,
			wfEmptyMsg(2), wfEmptyMsg(3), wfEmptyMsg(7), wfEmptyMsg(8),
			wfMsg(14, wfEmptyMsg(1)),
			wfEmptyMsg(16),
			wfMsg(17, wfEmptyMsg(2)),
			wfEmptyMsg(18), wfEmptyMsg(19), wfEmptyMsg(20), wfEmptyMsg(21),
			wfEmptyMsg(22), wfEmptyMsg(23),
			wfMsg(27, wfEmptyMsg(1), wfMsg(2, wfEmptyMsg(1))),
			wfEmptyMsg(29), wfEmptyMsg(30), wfEmptyMsg(31), wfEmptyMsg(32),
			wfEmptyMsg(34), wfEmptyMsg(37), wfEmptyMsg(38), wfEmptyMsg(39),
			wfEmptyMsg(41),
			wfMsg(43, wfEmptyMsg(1)),
			wfMsg(45, wfMsg(1, wfEmptyMsg(1))),
			wfMsg(46, wfEmptyMsg(1), wfEmptyMsg(2), wfEmptyMsg(3)),
			wfEmptyMsg(47),
		),
		wfMsg(4, wfEmptyMsg(2), wfMsg(3, wfEmptyMsg(1)), wfEmptyMsg(4), wfMsg(5, wfEmptyMsg(1))),
		wfEmptyMsg(7), wfEmptyMsg(12), wfEmptyMsg(13),
		wfMsg(14,
			wfEmptyMsg(1),
			wfMsg(2, wfEmptyMsg(1), wfMsg(2, wfEmptyMsg(1)), wfEmptyMsg(3), wfMsg(4, wfEmptyMsg(1))),
			wfMsg(3, wfEmptyMsg(1), wfMsg(2, wfEmptyMsg(1)), wfEmptyMsg(3), wfEmptyMsg(4)),
		),
		wfEmptyMsg(15),
		wfMsg(16, wfEmptyMsg(1)),
		wfEmptyMsg(18),
		wfMsg(19,
			wfMsg(4, wfEmptyMsg(2)),
			wfMsg(6, wfEmptyMsg(2), wfEmptyMsg(3)),
			wfMsg(7, wfEmptyMsg(2), wfEmptyMsg(3)),
			wfEmptyMsg(8),
			wfEmptyMsg(9),
		),
		wfEmptyMsg(20), wfEmptyMsg(22), wfEmptyMsg(24), wfEmptyMsg(25), wfEmptyMsg(26),
	)

	f1Fields := []wireField{
		f1_1,
		commonLibraryField1_2(true),
		f1_3,
		wfString(6, syncToken),
		wfVarint(7, 2),
		wfMsg(9,
			wfMsg(1, wfMsg(2, wfEmptyMsg(1), wfEmptyMsg(2))),
			wfMsg(2, wfMsg(3, wfVarint(2, 1))),
			wfMsg(3, wfEmptyMsg(2)),
			wfEmptyMsg(4),
			wfMsg(7, wfEmptyMsg(1)),
			wfMsg(8, wfVarint(1, 2), wfBytes(2, []byte{1, 2, 3, 5, 6, 7})),
			wfEmptyMsg(9),
			wfMsg(11, wfEmptyMsg(1)),
		),
	}
	f1Fields = append(f1Fields, wfRepeatedVarint(11, 1, 2, 6)...)
	f1Fields = append(f1Fields,
		wfMsg(12, wfMsg(2, wfEmptyMsg(1), wfEmptyMsg(2)), wfMsg(3, wfEmptyMsg(1)), wfEmptyMsg(4)),
		wfEmptyMsg(13),
		wfMsg(15, wfMsg(3, wfVarint(1, 1))),
		wfMsg(18, wfMsg(169945741, wfMsg(1, commonPrintingPromoOptions()))),
		commonLibraryField1_19(),
		wfMsg(20,
			wfVarint(1, 1),
			wfString(2, ""),
			wfMsg(3,
				wfString(1, "type.googleapis.com/photos.printing.client.PrintingPromotionSyncOptions"),
				wfMsg(2, commonPrintingPromoOptions()),
			),
		),
		wfMsg(21,
			wfMsg(2, wfMsg(2, wfEmptyMsg(4)), wfEmptyMsg(4), wfEmptyMsg(5)),
			wfMsg(3, wfMsg(2, wfVarint(1, 1)), wfMsg(4, wfEmptyMsg(2))),
			wfMsg(5, wfEmptyMsg(1)),
			wfMsg(6, wfEmptyMsg(1), wfMsg(2, wfEmptyMsg(1))),
			wfMsg(7,
				wfVarint(1, 2),
				wfString(2, "\x01\x07\x08\t\n\r\x0e\x0f\x11\x13\x14\x16\x17-./01:\x06\x18267;>?@A89<GBED"),
				wfString(3, "\x01"),
			),
			wfMsg(8,
				wfMsg(3, wfMsg(1, wfMsg(1, wfMsg(2, wfVarint(1, 1)), wfMsg(4, wfEmptyMsg(2)))), wfEmptyMsg(3)),
				wfMsg(4, wfEmptyMsg(1)),
				wfMsg(5, wfMsg(1, wfMsg(2, wfVarint(1, 1)), wfMsg(4, wfEmptyMsg(2)))),
			),
			wfMsg(9, wfEmptyMsg(1)),
			wfMsg(10,
				wfMsg(1, wfEmptyMsg(1)),
				wfEmptyMsg(3), wfEmptyMsg(5),
				wfMsg(6, wfEmptyMsg(1)),
				wfEmptyMsg(7), wfEmptyMsg(9), wfEmptyMsg(10),
			),
			wfEmptyMsg(11), wfEmptyMsg(12), wfEmptyMsg(13), wfEmptyMsg(14),
			wfMsg(16, wfEmptyMsg(1)),
		),
		wfMsg(22, wfVarint(1, 1), wfString(2, "107818234414673686888")),
		wfMsg(25, wfMsg(1, wfMsg(1, wfMsg(1, wfEmptyMsg(1)))), wfEmptyMsg(2)),
		wfEmptyMsg(26),
	)

	return encodeWireMsg(wfMsg(1, f1Fields...), commonLibraryOuterField2())
}

func buildGetLibraryPageInitPayload(resumeToken string) []byte {
	f1_1 := wfMsg(1,
		commonLibraryField1_1_1(),
		wfMsg(5,
			wfMsg(2,
				wfMsg(2, wfMsg(3, wfEmptyMsg(2)), wfMsg(4, wfEmptyMsg(2))),
				wfMsg(4, wfMsg(2, wfVarint(2, 1))),
				wfMsg(5, wfEmptyMsg(2)),
				wfVarint(6, 1),
			),
			wfMsg(3,
				wfMsg(2, wfEmptyMsg(3), wfEmptyMsg(4)),
				wfMsg(3, wfEmptyMsg(2), wfMsg(3, wfVarint(2, 1))),
				wfEmptyMsg(4),
				wfMsg(5, wfMsg(2, wfVarint(2, 1))),
				wfEmptyMsg(7),
			),
			wfMsg(4, wfMsg(2, wfEmptyMsg(2))),
			wfMsg(5,
				wfMsg(1,
					wfMsg(2, wfEmptyMsg(3), wfEmptyMsg(4)),
					wfMsg(3, wfEmptyMsg(2), wfMsg(3, wfVarint(2, 1))),
				),
				wfVarint(3, 1),
			),
		),
		wfEmptyMsg(8),
		wfMsg(9,
			wfEmptyMsg(2),
			wfMsg(3, wfEmptyMsg(1), wfEmptyMsg(2)),
			wfMsg(4, wfMsg(1,
				wfMsg(3, wfMsg(1, wfMsg(1,
					wfMsg(5, wfEmptyMsg(1)),
					wfEmptyMsg(6),
				), wfEmptyMsg(2), wfMsg(3, wfMsg(1,
					wfMsg(5, wfEmptyMsg(1)),
					wfEmptyMsg(6),
				), wfEmptyMsg(2)))),
				wfMsg(4, wfMsg(1, wfEmptyMsg(2))),
			)),
		),
		wfMsg(11, wfEmptyMsg(2), wfEmptyMsg(3), wfMsg(4, wfMsg(2, wfVarint(1, 1), wfVarint(2, 2)))),
		wfEmptyMsg(12),
		wfMsg(14, wfEmptyMsg(2), wfEmptyMsg(3), wfMsg(4, wfMsg(2, wfVarint(1, 1), wfVarint(2, 2)))),
		wfMsg(15, wfEmptyMsg(1), wfEmptyMsg(4)),
		wfMsg(17, wfEmptyMsg(1), wfEmptyMsg(4)),
		wfMsg(19, wfEmptyMsg(2), wfEmptyMsg(3), wfMsg(4, wfMsg(2, wfVarint(1, 1), wfVarint(2, 2)))),
		wfEmptyMsg(22),
		wfEmptyMsg(23),
	)

	f1_3 := wfMsg(3,
		wfEmptyMsg(2),
		wfMsg(3,
			wfEmptyMsg(2), wfEmptyMsg(3), wfEmptyMsg(7), wfEmptyMsg(8),
			wfMsg(14, wfEmptyMsg(1)),
			wfEmptyMsg(16),
			wfMsg(17, wfEmptyMsg(2)),
			wfEmptyMsg(18), wfEmptyMsg(19), wfEmptyMsg(20), wfEmptyMsg(21),
			wfEmptyMsg(22), wfEmptyMsg(23),
			wfMsg(27, wfEmptyMsg(1), wfMsg(2, wfEmptyMsg(1))),
			wfEmptyMsg(29), wfEmptyMsg(30), wfEmptyMsg(31), wfEmptyMsg(32),
			wfEmptyMsg(34), wfEmptyMsg(37), wfEmptyMsg(38), wfEmptyMsg(39),
			wfEmptyMsg(41),
		),
		wfMsg(4, wfEmptyMsg(2), wfEmptyMsg(3), wfEmptyMsg(4)),
		wfEmptyMsg(7), wfEmptyMsg(12), wfEmptyMsg(13),
		wfMsg(14,
			wfEmptyMsg(1),
			wfMsg(2, wfEmptyMsg(1), wfMsg(2, wfEmptyMsg(1)), wfEmptyMsg(3), wfMsg(4, wfEmptyMsg(1))),
			wfMsg(3, wfEmptyMsg(1), wfMsg(2, wfEmptyMsg(1)), wfEmptyMsg(3), wfEmptyMsg(4)),
		),
		wfEmptyMsg(15),
		wfMsg(16, wfEmptyMsg(1)),
		wfEmptyMsg(18),
		wfMsg(19,
			wfMsg(4, wfEmptyMsg(2)),
			wfMsg(6, wfEmptyMsg(2), wfEmptyMsg(3)),
			wfMsg(7, wfEmptyMsg(2), wfEmptyMsg(3)),
			wfEmptyMsg(8),
		),
		wfEmptyMsg(20), wfEmptyMsg(24), wfEmptyMsg(25),
	)

	f1Fields := []wireField{
		f1_1,
		commonLibraryField1_2(false),
		f1_3,
		wfString(4, resumeToken),
		wfVarint(7, 2),
		wfMsg(9,
			wfMsg(1, wfMsg(2, wfEmptyMsg(1), wfEmptyMsg(2))),
			wfMsg(2, wfMsg(3, wfVarint(2, 1))),
			wfMsg(3, wfEmptyMsg(2)),
			wfEmptyMsg(4),
			wfMsg(7, wfEmptyMsg(1)),
			wfMsg(8, wfVarint(1, 2), wfBytes(2, []byte{1, 2, 3, 5, 6})),
			wfEmptyMsg(9),
		),
	}
	f1Fields = append(f1Fields, wfRepeatedVarint(11, 1, 2)...)
	f1Fields = append(f1Fields,
		wfMsg(12, wfMsg(2, wfEmptyMsg(1), wfEmptyMsg(2)), wfMsg(3, wfEmptyMsg(1)), wfEmptyMsg(4)),
		wfEmptyMsg(13),
		wfMsg(15, wfMsg(3, wfVarint(1, 1))),
		wfMsg(18, wfMsg(169945741, wfMsg(1, commonPrintingPromoOptions()))),
		commonLibraryField1_19(),
		wfMsg(20,
			wfVarint(1, 1),
			wfMsg(3,
				wfString(1, "type.googleapis.com/photos.printing.client.PrintingPromotionSyncOptions"),
				wfMsg(2, commonPrintingPromoOptions()),
			),
		),
		wfMsg(21,
			wfMsg(2, wfEmptyMsg(2), wfEmptyMsg(4), wfEmptyMsg(5)),
			wfMsg(3, wfMsg(2, wfVarint(1, 1))),
			wfMsg(5, wfEmptyMsg(1)),
			wfMsg(6, wfEmptyMsg(1), wfMsg(2, wfEmptyMsg(1))),
			wfMsg(7,
				wfVarint(1, 2),
				wfString(2, "\x01\x07\x08\t\n\r\x0e\x0f\x11\x13\x14\x16\x17-./01:\x06\x18267;>?@A89<"),
				wfString(3, "\x01"),
			),
			wfMsg(8,
				wfMsg(3, wfMsg(1, wfMsg(1, wfMsg(2, wfVarint(1, 1))))),
				wfMsg(4, wfEmptyMsg(1)),
			),
			wfMsg(9, wfEmptyMsg(1)),
			wfMsg(10,
				wfMsg(1, wfEmptyMsg(1)),
				wfEmptyMsg(3), wfEmptyMsg(5),
				wfMsg(6, wfEmptyMsg(1)),
				wfEmptyMsg(7), wfEmptyMsg(9), wfEmptyMsg(10),
			),
			wfEmptyMsg(11), wfEmptyMsg(12), wfEmptyMsg(13),
		),
		wfMsg(22, wfVarint(1, 2)),
		wfMsg(25, wfMsg(1, wfMsg(1, wfMsg(1, wfEmptyMsg(1)))), wfEmptyMsg(2)),
	)

	return encodeWireMsg(wfMsg(1, f1Fields...), commonLibraryOuterField2())
}
