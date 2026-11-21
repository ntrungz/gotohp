//go:build !cli

package gui

import (
	"app/core"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// WailsReclaimReporter forwards quota reclaim events to the frontend as Wails events.
type WailsReclaimReporter struct {
	app *application.App
}

func NewWailsReclaimReporter(app *application.App) *WailsReclaimReporter {
	return &WailsReclaimReporter{app: app}
}

func (w *WailsReclaimReporter) ReclaimScanProgress(s core.LibraryScanStats) {
	w.app.Event.Emit("reclaimScanProgress", s)
}

func (w *WailsReclaimReporter) ReclaimScanComplete(r core.ReclaimScanResult) {
	w.app.Event.Emit("reclaimScanComplete", r)
}

func (w *WailsReclaimReporter) ReclaimStart(s core.ReclaimBatchStart) {
	w.app.Event.Emit("reclaimStart", s)
}

func (w *WailsReclaimReporter) ReclaimProgress(p core.ReclaimItemProgress) {
	w.app.Event.Emit("reclaimProgress", p)
}

func (w *WailsReclaimReporter) ReclaimItemResult(r core.ReclaimItemResult) {
	w.app.Event.Emit("reclaimItemResult", r)
}

func (w *WailsReclaimReporter) ReclaimStop(s core.ReclaimSummary) {
	w.app.Event.Emit("reclaimStop", s)
}
