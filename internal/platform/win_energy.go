//go:build windows

package platform

import (
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// Windows energy diagnostics via powercfg /energy - the same built-in tool
// OEMs use to certify power efficiency. The XML schema below was verified
// against a REAL report generated on real hardware while building this
// feature (not written from documentation alone): EnergyReport ->
// Troubleshooter (a category, e.g. "Platform Timer Resolution", "USB
// Suspend") -> AnalysisLog -> LogEntry (Name/Severity/Description/Details),
// each Detail a Name/Value pair. Confirmed real findings on that run
// included "Sleep timeout is disabled" (Error, both AC and battery) and a
// named process holding an execution-required sleep-prevention request -
// genuinely actionable, not theoretical.

type xmlEnergyReport struct {
	Troubleshooters []xmlTroubleshooter `xml:"Troubleshooter"`
}
type xmlTroubleshooter struct {
	Name        string         `xml:"Name"`
	AnalysisLog xmlAnalysisLog `xml:"AnalysisLog"`
}
type xmlAnalysisLog struct {
	LogEntries []xmlLogEntry `xml:"LogEntry"`
}
type xmlLogEntry struct {
	Name        string     `xml:"Name"`
	Severity    string     `xml:"Severity"`
	Description string     `xml:"Description"`
	Details     xmlDetails `xml:"Details"`
}
type xmlDetails struct {
	Detail []xmlDetail `xml:"Detail"`
}
type xmlDetail struct {
	Name  string `xml:"Name"`
	Value string `xml:"Value"`
}

var processDetailRE = regexp.MustCompile(`(?i)process|device name|requesting`)

func winEnergyAudit() (*EnergyAuditResult, error) {
	reportPath := filepath.Join(os.TempDir(), "smarteff-energy-report.xml")
	defer os.Remove(reportPath)

	// -duration 20: shortened from the 60s default. This runs weekly at
	// most, so the extra precision of a full 60s scan isn't worth 3x the
	// active-measurement cost every time.
	cmd := exec.Command("powercfg", "/energy", "/output", reportPath, "/XML", "/duration", "20")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("powercfg /energy failed (needs admin): %v: %s", err, string(out))
	}

	data, err := os.ReadFile(reportPath)
	if err != nil {
		return nil, fmt.Errorf("powercfg /energy produced no report: %w", err)
	}

	var report xmlEnergyReport
	if err := xml.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("parsing energy report: %w", err)
	}

	type key struct{ category, name string }
	grouped := map[key]*EnergyIssue{}
	var order []key

	for _, ts := range report.Troubleshooters {
		for _, entry := range ts.AnalysisLog.LogEntries {
			if entry.Severity != "Warning" && entry.Severity != "Error" {
				continue
			}
			entryName := entry.Name
			if entryName == "" {
				entryName = ts.Name // a few categories don't populate LogEntry.Name - fall back to the category
			}
			k := key{ts.Name, entryName}
			if issue, exists := grouped[k]; exists {
				issue.Count++
				continue
			}
			var detail string
			for _, d := range entry.Details.Detail {
				if processDetailRE.MatchString(d.Name) {
					if detail != "" {
						detail += ", "
					}
					detail += d.Name + "=" + d.Value
				}
			}
			grouped[k] = &EnergyIssue{
				Severity: entry.Severity,
				Category: ts.Name,
				Name:     entryName,
				Detail:   detail,
				Count:    1,
			}
			order = append(order, k)
		}
	}

	var issues []EnergyIssue
	errorCount, warnCount := 0, 0
	for _, k := range order {
		issue := *grouped[k]
		issues = append(issues, issue)
		if issue.Severity == "Error" {
			errorCount++
		} else {
			warnCount++
		}
	}
	sort.SliceStable(issues, func(i, j int) bool {
		if (issues[i].Severity == "Error") != (issues[j].Severity == "Error") {
			return issues[i].Severity == "Error"
		}
		return issues[i].Count > issues[j].Count
	})
	if len(issues) > 10 {
		issues = issues[:10]
	}

	return &EnergyAuditResult{
		RanAt:      time.Now(),
		ErrorCount: errorCount,
		WarnCount:  warnCount,
		TopIssues:  issues,
	}, nil
}
