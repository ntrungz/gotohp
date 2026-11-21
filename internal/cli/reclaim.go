package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"app/core"

	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func newReclaimCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reclaim",
		Short: "Reclaim Google Photos storage quota by re-uploading without quota",
		Long: "Scan your Google Photos library for media items consuming storage quota,\n" +
			"download originals locally, move the quota-charged copies to Trash (and\n" +
			"optionally delete them permanently), and re-upload them without using quota.\n\n" +
			"By default, items are only moved to Trash and downloaded originals are kept\n" +
			"in --download-dir as local backups. Use --dry-run first to inspect what\n" +
			"would be processed.",
		Args: cobra.NoArgs,
		RunE: runReclaimCommand,
	}

	f := cmd.Flags()
	f.SortFlags = false
	f.String("account", "", "account email to reclaim quota on (default: the selected account)")
	f.String("proxy", "", "proxy URL for all requests")
	f.String("download-dir", core.DefaultReclaimDownloadDir(), "directory where downloaded original backups are saved")
	f.Bool("dry-run", false, "scan and list quota-consuming items without downloading, trashing, or re-uploading")
	f.Bool("delete-permanently", false, "permanently delete trashed items before re-uploading (avoids Trash SHA-1 deduplication)")
	f.Bool("cleanup-local", false, "delete each downloaded local file after its re-upload succeeds")
	f.Bool("saver", false, "re-upload in Storage Saver quality (Pixel 2) instead of Original quality (Pixel XL)")
	f.Bool("no-restore-metadata", false, "do not restore favorite, archived, and caption metadata after re-upload")
	f.Int("limit", 0, "maximum number of quota-consuming items to process (0 = all)")
	f.IntP("threads", "t", 1, "number of concurrent reclaim workers")
	f.StringP("log-level", "l", "info", "log level: debug, info, warn, error")
	f.Bool("no-tui", false, "disable the interactive progress UI")
	f.Bool("json-progress", false, "stream machine-readable NDJSON progress events to stderr")
	return cmd
}

type reclaimRunSettings struct {
	logLevel     string
	noTUI        bool
	jsonProgress bool
}

func reclaimOptionsFromFlags(f *pflag.FlagSet) core.ReclaimOptions {
	getBool := func(name string) bool { v, _ := f.GetBool(name); return v }
	getStr := func(name string) string { v, _ := f.GetString(name); return v }
	getInt := func(name string) int { v, _ := f.GetInt(name); return v }

	return core.ReclaimOptions{
		Api: core.ApiOptions{
			Account:  getStr("account"),
			Proxy:    getStr("proxy"),
			Saver:    getBool("saver"),
			UseQuota: false,
		},
		DownloadDir:       getStr("download-dir"),
		DeletePermanently: getBool("delete-permanently"),
		CleanupLocal:      getBool("cleanup-local"),
		DryRun:            getBool("dry-run"),
		RestoreMetadata:   !getBool("no-restore-metadata"),
		Limit:             getInt("limit"),
		Threads:           getInt("threads"),
	}
}

func runReclaimCommand(cmd *cobra.Command, _ []string) error {
	f := cmd.Flags()
	if threads, _ := f.GetInt("threads"); threads < 1 {
		return fmt.Errorf("threads must be a positive integer, got %d", threads)
	}
	if limit, _ := f.GetInt("limit"); limit < 0 {
		return fmt.Errorf("limit must be non-negative, got %d", limit)
	}

	configPath, _ := f.GetString("config")
	if err := core.LoadConfig(configPath); err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	opts := reclaimOptionsFromFlags(f)
	logLevel, _ := f.GetString("log-level")
	noTUI, _ := f.GetBool("no-tui")
	jsonProgress, _ := f.GetBool("json-progress")
	return runReclaim(opts, reclaimRunSettings{
		logLevel:     logLevel,
		noTUI:        noTUI,
		jsonProgress: jsonProgress,
	})
}

// Bubbletea messages for reclaim TUI
type reclaimScanProgressMsg struct{ stats core.LibraryScanStats }
type reclaimScanCompleteMsg struct{ result core.ReclaimScanResult }
type reclaimStartMsg struct{ batch core.ReclaimBatchStart }
type reclaimItemProgressMsg struct{ progress core.ReclaimItemProgress }
type reclaimItemResultMsg struct{ result core.ReclaimItemResult }
type reclaimStopMsg struct{ summary core.ReclaimSummary }

type reclaimModel struct {
	progress       progress.Model
	phase          string // "scanning", "reclaiming", "done"
	scanStats      core.LibraryScanStats
	scanResult     *core.ReclaimScanResult
	total          int
	totalBytes     int64
	succeeded      int
	failed         int
	reclaimedBytes int64
	workers        map[int]string
	summary        core.ReclaimSummary
	cancelFn       func()
	dryRun         bool
	quitting       bool
}

func initialReclaimModel(dryRun bool, cancelFn func()) reclaimModel {
	return reclaimModel{
		progress: progress.New(progress.WithDefaultGradient()),
		phase:    "scanning",
		workers:  make(map[int]string),
		cancelFn: cancelFn,
		dryRun:   dryRun,
	}
}

func (m reclaimModel) Init() tea.Cmd { return nil }

func (m reclaimModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.progress.Width = max(20, msg.Width-4)
		return m, nil
	case reclaimScanProgressMsg:
		m.scanStats = msg.stats
		return m, nil
	case reclaimScanCompleteMsg:
		r := msg.result
		m.scanResult = &r
		m.scanStats = r.Stats
		return m, nil
	case reclaimStartMsg:
		m.phase = "reclaiming"
		m.total = msg.batch.Total
		m.totalBytes = msg.batch.TotalQuotaBytes
		return m, nil
	case reclaimItemProgressMsg:
		m.workers[msg.progress.WorkerID] = fmt.Sprintf("[%d] %s: %s (%s)",
			msg.progress.WorkerID, msg.progress.Stage, msg.progress.FileName, msg.progress.Message)
		return m, nil
	case reclaimItemResultMsg:
		if msg.result.IsError {
			m.failed++
		} else {
			m.succeeded++
			m.reclaimedBytes += msg.result.QuotaChargedBytes
		}
		return m, nil
	case reclaimStopMsg:
		m.phase = "done"
		m.summary = msg.summary
		m.quitting = true
		return m, tea.Quit
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			if m.cancelFn != nil {
				m.cancelFn()
			}
			m.quitting = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m reclaimModel) View() string {
	if m.quitting {
		return ""
	}
	var b strings.Builder
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	if m.dryRun {
		b.WriteString(titleStyle.Render("Scanning Google Photos Quota (Dry Run)"))
	} else {
		b.WriteString(titleStyle.Render("Reclaiming Google Photos Quota"))
	}
	b.WriteString("\n\n")

	if m.phase == "scanning" {
		fmt.Fprintf(&b, "Scanning library... Pages: %d | Scanned: %d | Quota items: %d (%s)\n",
			m.scanStats.PagesScanned,
			m.scanStats.TotalScanned,
			m.scanStats.EligibleCount,
			formatHumanBytes(m.scanStats.TotalQuotaBytes),
		)
	} else {
		if m.total > 0 {
			percent := float64(m.succeeded+m.failed) / float64(m.total)
			b.WriteString(m.progress.ViewAs(percent))
			fmt.Fprintf(&b, "\n%d/%d items (%d succeeded, %d failed) | Reclaimed: %s / %s\n\n",
				m.succeeded+m.failed,
				m.total,
				m.succeeded,
				m.failed,
				formatHumanBytes(m.reclaimedBytes),
				formatHumanBytes(m.totalBytes),
			)
		}
		for i := 0; i < len(m.workers); i++ {
			if line, ok := m.workers[i]; ok {
				b.WriteString(line)
				b.WriteString("\n")
			}
		}
	}

	b.WriteString("\nPress Ctrl+C to cancel\n")
	return b.String()
}

type jsonProgressEvent struct {
	Event string `json:"event"`
	Data  any    `json:"data"`
}

type jsonProgressWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (jw *jsonProgressWriter) Emit(event string, data any) {
	if jw == nil || jw.w == nil {
		return
	}
	encoded, err := json.Marshal(jsonProgressEvent{Event: event, Data: data})
	if err != nil {
		return
	}
	jw.mu.Lock()
	_, _ = jw.w.Write(append(encoded, '\n'))
	jw.mu.Unlock()
}

type teaReclaimReporter struct {
	p  *tea.Program
	jw *jsonProgressWriter
}

func (r teaReclaimReporter) ReclaimScanProgress(stats core.LibraryScanStats) {
	r.jw.Emit("scan_progress", stats)
	r.p.Send(reclaimScanProgressMsg{stats: stats})
}

func (r teaReclaimReporter) ReclaimScanComplete(res core.ReclaimScanResult) {
	r.jw.Emit("scan_complete", struct {
		Stats        core.LibraryScanStats `json:"stats"`
		DownloadDir  string                `json:"downloadDir"`
		ErrorMessage string                `json:"errorMessage,omitempty"`
	}{
		Stats:        res.Stats,
		DownloadDir:  res.DownloadDir,
		ErrorMessage: res.ErrorMessage,
	})
	r.p.Send(reclaimScanCompleteMsg{result: res})
}

func (r teaReclaimReporter) ReclaimStart(batch core.ReclaimBatchStart) {
	r.jw.Emit("reclaim_start", batch)
	r.p.Send(reclaimStartMsg{batch: batch})
}

func (r teaReclaimReporter) ReclaimProgress(progress core.ReclaimItemProgress) {
	r.jw.Emit("item_progress", progress)
	r.p.Send(reclaimItemProgressMsg{progress: progress})
}

func (r teaReclaimReporter) ReclaimItemResult(res core.ReclaimItemResult) {
	r.jw.Emit("item_result", res)
	r.p.Send(reclaimItemResultMsg{result: res})
}

func (r teaReclaimReporter) ReclaimStop(summary core.ReclaimSummary) {
	r.jw.Emit("reclaim_stop", struct {
		Total          int    `json:"total"`
		Succeeded      int    `json:"succeeded"`
		Failed         int    `json:"failed"`
		ReclaimedBytes int64  `json:"reclaimedBytes"`
		DownloadDir    string `json:"downloadDir"`
		Canceled       bool   `json:"canceled,omitempty"`
		ErrorMessage   string `json:"errorMessage,omitempty"`
	}{
		Total:          summary.Total,
		Succeeded:      summary.Succeeded,
		Failed:         summary.Failed,
		ReclaimedBytes: summary.ReclaimedBytes,
		DownloadDir:    summary.DownloadDir,
		Canceled:       summary.Canceled,
		ErrorMessage:   summary.ErrorMessage,
	})
	r.p.Send(reclaimStopMsg{summary: summary})
}

func shouldUseReclaimTUI(settings reclaimRunSettings) bool {
	return !settings.noTUI &&
		!settings.jsonProgress &&
		term.IsTerminal(os.Stdin.Fd()) &&
		term.IsTerminal(os.Stdout.Fd())
}

func runReclaim(opts core.ReclaimOptions, settings reclaimRunSettings) error {
	var mgr *core.ReclaimManager
	model := initialReclaimModel(opts.DryRun, func() {
		if mgr != nil {
			mgr.Cancel()
		}
	})

	programOptions := []tea.ProgramOption{}
	if !shouldUseReclaimTUI(settings) {
		programOptions = append(programOptions, tea.WithInput(nil), tea.WithoutRenderer())
	}
	p := tea.NewProgram(model, programOptions...)

	var jw *jsonProgressWriter
	if settings.jsonProgress {
		jw = &jsonProgressWriter{w: os.Stderr}
	}

	mgr = core.NewReclaimManager(teaReclaimReporter{p: p, jw: jw}, newLogger(parseLogLevel(settings.logLevel)))
	go mgr.Run(opts)

	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("error running reclaim program: %w", err)
	}
	if m, ok := finalModel.(reclaimModel); ok {
		return finishReclaim(m, os.Stdout)
	}
	return nil
}

type reclaimCLIOutput struct {
	DryRun  bool                    `json:"dryRun,omitempty"`
	Scan    *core.ReclaimScanResult `json:"scan,omitempty"`
	Summary *core.ReclaimSummary    `json:"summary,omitempty"`
}

func finishReclaim(model reclaimModel, output io.Writer) error {
	payload := reclaimCLIOutput{
		DryRun: model.dryRun,
		Scan:   model.scanResult,
	}
	if !model.dryRun {
		s := model.summary
		payload.Summary = &s
	}

	jsonOutput, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("error generating JSON: %w", err)
	}
	if _, err := fmt.Fprintln(output, string(jsonOutput)); err != nil {
		return fmt.Errorf("error writing JSON: %w", err)
	}
	if model.summary.ErrorMessage != "" || model.summary.Failed > 0 {
		return exitError{code: 1}
	}
	if model.scanResult != nil && model.scanResult.ErrorMessage != "" {
		return exitError{code: 1}
	}
	return nil
}

func formatHumanBytes(b int64) string {
	if b <= 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	val := float64(b)
	idx := 0
	for val >= 1024 && idx < len(units)-1 {
		val /= 1024
		idx++
	}
	return fmt.Sprintf("%.1f %s", val, units[idx])
}
