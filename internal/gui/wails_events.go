//go:build !cli

package gui

import (
	"app/core"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func init() {
	application.RegisterEvent[core.AlbumStatus]("albumProgress")
	application.RegisterEvent[core.AlbumStatus]("albumComplete")
	application.RegisterEvent[core.AlbumError]("albumError")
	application.RegisterEvent[core.UploadBatchStart]("uploadStart")
	application.RegisterEvent[application.Void]("uploadStop")
	application.RegisterEvent[core.FileUploadResult]("FileStatus")
	application.RegisterEvent[core.ThreadStatus]("ThreadStatus")
	application.RegisterEvent[core.PreflightWarning]("uploadWarning")
	application.RegisterEvent[application.Void]("uploadCancel")
	application.RegisterEvent[int64]("uploadTotalBytes")
	application.RegisterEvent[int64]("uploadTotalBytesDelta")
	application.RegisterEvent[core.FilesDroppedEvent]("files-dropped")
	application.RegisterEvent[core.StartUploadEvent]("startUpload")
	application.RegisterEvent[core.LibraryScanStats]("reclaimScanProgress")
	application.RegisterEvent[core.ReclaimScanResult]("reclaimScanComplete")
	application.RegisterEvent[core.ReclaimBatchStart]("reclaimStart")
	application.RegisterEvent[core.ReclaimItemProgress]("reclaimProgress")
	application.RegisterEvent[core.ReclaimItemResult]("reclaimItemResult")
	application.RegisterEvent[core.ReclaimSummary]("reclaimStop")
	application.RegisterEvent[core.StartReclaimScanEvent]("startReclaimScan")
	application.RegisterEvent[core.StartReclaimRunEvent]("startReclaimRun")
	application.RegisterEvent[application.Void]("reclaimCancel")
}
