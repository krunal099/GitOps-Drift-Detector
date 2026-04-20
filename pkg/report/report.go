// Package report defines the structured drift report and its output formats.
package report

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/krunalp/gitops-drift-detector/pkg/differ"
)

// Status describes the drift state of a single Kubernetes resource.
type Status string

const (
	StatusClean   Status = "clean"   // desired == live
	StatusDrifted Status = "drifted" // one or more fields differ
	StatusMissing Status = "missing" // resource exists in Git but not in cluster
	StatusIgnored Status = "ignored" // explicitly opted out via annotation
)

// ResourceReport is the per-resource section of the drift report.
type ResourceReport struct {
	Kind      string              `json:"kind"`
	Name      string              `json:"name"`
	Namespace string              `json:"namespace,omitempty"`
	Status    Status              `json:"status"`
	Fields    []differ.DriftField `json:"fields,omitempty"`
}

// DriftReport is the full output of one reconciliation pass.
type DriftReport struct {
	GeneratedAt    time.Time        `json:"generatedAt"`
	DryRun         bool             `json:"dryRun"`
	TotalResources int              `json:"totalResources"`
	DriftedCount   int              `json:"driftedCount"`
	MissingCount   int              `json:"missingCount"`
	CleanCount     int              `json:"cleanCount"`
	IgnoredCount   int              `json:"ignoredCount"`
	Resources      []ResourceReport `json:"resources"`
}

// New initialises an empty DriftReport.
func New(dryRun bool) *DriftReport {
	return &DriftReport{
		GeneratedAt: time.Now().UTC(),
		DryRun:      dryRun,
	}
}

// Add appends a ResourceReport and updates the aggregate counters.
func (r *DriftReport) Add(rr ResourceReport) {
	r.Resources = append(r.Resources, rr)
	r.TotalResources++
	switch rr.Status {
	case StatusDrifted:
		r.DriftedCount++
	case StatusMissing:
		r.MissingCount++
	case StatusClean:
		r.CleanCount++
	case StatusIgnored:
		r.IgnoredCount++
	}
}

// HasDrift returns true if any resource is drifted or missing.
// Used to set a non-zero exit code in CI/CD pipelines.
func (r *DriftReport) HasDrift() bool {
	return r.DriftedCount > 0 || r.MissingCount > 0
}

// PrintSummary writes a human-readable summary to stdout.
func (r *DriftReport) PrintSummary() {
	fmt.Printf("\n=== DRIFT REPORT (%s) ===\n", r.GeneratedAt.Format(time.RFC3339))
	if r.DryRun {
		fmt.Println("[DRY RUN — no changes applied]")
	}
	fmt.Printf("Total: %d  |  Drifted: %d  |  Missing: %d  |  Clean: %d  |  Ignored: %d\n\n",
		r.TotalResources, r.DriftedCount, r.MissingCount, r.CleanCount, r.IgnoredCount)

	for _, res := range r.Resources {
		switch res.Status {
		case StatusClean, StatusIgnored:
			continue
		case StatusMissing:
			fmt.Printf("[MISSING]  %s %s/%s\n", res.Kind, res.Namespace, res.Name)
		case StatusDrifted:
			fmt.Printf("[DRIFTED]  %s %s/%s  (%d field(s))\n", res.Kind, res.Namespace, res.Name, len(res.Fields))
			for _, f := range res.Fields {
				fmt.Printf("           ~ %s\n", f.Path)
				fmt.Printf("             desired : %s\n", toJSON(f.Desired))
				fmt.Printf("             actual  : %s\n", toJSON(f.Actual))
			}
		}
	}
	fmt.Println()
}

// ToJSON serialises the full report as indented JSON.
func (r *DriftReport) ToJSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

func toJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}
