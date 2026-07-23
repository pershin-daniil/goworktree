package tui

import (
	"errors"
	"strings"
	"testing"
)

func TestDiagnosticReportIsStableAndContextual(t *testing.T) {
	m := appModel{
		actions:     AppActions{Version: "0.1.0"},
		pendingArgs: []string{"sync", "ticket-42"},
		selected:    "ticket-42",
		err:         errors.New("git fetch origin: offline"),
		log:         []string{"syncing ticket-42", "failed api"},
	}
	report := m.diagnostic()
	for _, want := range []string{
		"goworktree diagnostic report",
		"report_version: 1",
		"app_version: 0.1.0",
		"action: sync ticket-42",
		"project: ticket-42",
		"error: git fetch origin: offline",
		"operation_log:\nsyncing ticket-42\nfailed api",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

func TestSelectedIDsAreSorted(t *testing.T) {
	got := selectedIDs(map[string]bool{"z": true, "a": true, "off": false})
	if strings.Join(got, ",") != "a,z" {
		t.Fatalf("selectedIDs = %v", got)
	}
}
