package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"app/core"
)

func TestReclaimCLIFlagParsingAndDefaults(t *testing.T) {
	if !IsCLIInvocation([]string{"reclaim", "--dry-run"}) {
		t.Fatal("IsCLIInvocation should recognize reclaim subcommand")
	}
	if !IsCLIInvocation([]string{"--config", "c.yaml", "reclaim"}) {
		t.Fatal("IsCLIInvocation should recognize reclaim after --config")
	}

	cmd := newReclaimCommand()
	if err := cmd.ParseFlags([]string{
		"--account", "user@example.com",
		"--proxy", "http://127.0.0.1:8080",
		"--download-dir", "/tmp/reclaim-backups",
		"--dry-run",
		"--delete-permanently",
		"--cleanup-local",
		"--saver",
		"--no-restore-metadata",
		"--limit", "25",
		"--threads", "3",
	}); err != nil {
		t.Fatalf("ParseFlags failed: %v", err)
	}

	opts := reclaimOptionsFromFlags(cmd.Flags())
	if opts.Api.Account != "user@example.com" || opts.Api.Proxy != "http://127.0.0.1:8080" || !opts.Api.Saver || opts.Api.UseQuota {
		t.Fatalf("unexpected Api options: %+v", opts.Api)
	}
	if opts.DownloadDir != "/tmp/reclaim-backups" || !opts.DryRun || !opts.DeletePermanently || !opts.CleanupLocal || opts.RestoreMetadata || opts.Limit != 25 || opts.Threads != 3 {
		t.Fatalf("unexpected reclaim options: %+v", opts)
	}
}

func TestReclaimCLIDefaultSafeFlags(t *testing.T) {
	cmd := newReclaimCommand()
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatalf("ParseFlags failed: %v", err)
	}
	opts := reclaimOptionsFromFlags(cmd.Flags())
	if opts.DeletePermanently {
		t.Fatal("DeletePermanently must default to false")
	}
	if opts.CleanupLocal {
		t.Fatal("CleanupLocal must default to false")
	}
	if !opts.RestoreMetadata {
		t.Fatal("RestoreMetadata must default to true")
	}
	if opts.Threads != 1 {
		t.Fatalf("Threads = %d, want 1", opts.Threads)
	}
	if opts.DownloadDir == "" {
		t.Fatal("DownloadDir must have a non-empty default")
	}
}

func TestFinishReclaimOutputAndExitCodes(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var buf bytes.Buffer
		m := reclaimModel{
			summary: core.ReclaimSummary{
				Total:          2,
				Succeeded:      2,
				Failed:         0,
				ReclaimedBytes: 4096,
				DownloadDir:    "/tmp/dl",
			},
		}
		if err := finishReclaim(m, &buf); err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		var out reclaimCLIOutput
		if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
			t.Fatalf("invalid JSON output: %v", err)
		}
		if out.Summary == nil || out.Summary.Succeeded != 2 || out.Summary.ReclaimedBytes != 4096 {
			t.Fatalf("unexpected summary JSON: %+v", out)
		}
	})

	t.Run("failure exits 1", func(t *testing.T) {
		var buf bytes.Buffer
		m := reclaimModel{
			summary: core.ReclaimSummary{
				Total:     1,
				Succeeded: 0,
				Failed:    1,
			},
		}
		err := finishReclaim(m, &buf)
		var exit exitError
		if !errorsAs(err, &exit) || exit.code != 1 {
			t.Fatalf("expected exitError{1}, got %v", err)
		}
	})

	t.Run("dry run prints scan only", func(t *testing.T) {
		var buf bytes.Buffer
		m := reclaimModel{
			dryRun: true,
			scanResult: &core.ReclaimScanResult{
				Stats: core.LibraryScanStats{EligibleCount: 3, TotalQuotaBytes: 9999},
			},
		}
		if err := finishReclaim(m, &buf); err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		var out reclaimCLIOutput
		if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if !out.DryRun || out.Scan == nil || out.Scan.Stats.EligibleCount != 3 || out.Summary != nil {
			t.Fatalf("unexpected dry-run JSON: %+v", out)
		}
	})
}
