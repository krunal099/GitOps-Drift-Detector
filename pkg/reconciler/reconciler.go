// Package reconciler implements the control loop: load desired → compare live → report → remediate.
package reconciler

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/krunalp/gitops-drift-detector/pkg/cluster"
	"github.com/krunalp/gitops-drift-detector/pkg/config"
	"github.com/krunalp/gitops-drift-detector/pkg/differ"
	"github.com/krunalp/gitops-drift-detector/pkg/loader"
	"github.com/krunalp/gitops-drift-detector/pkg/report"
)

// annotationIgnore marks a resource as fully excluded from drift checking.
// Useful for resources managed by other controllers (HPAs, operators, etc.)
const annotationIgnore = "drift-detector/ignore"

// annotationNoRemediate opts a resource out of auto-remediation while still
// reporting it as drifted. Use this for drift you want to alert on but not
// automatically fix (e.g., replica counts managed by an HPA).
const annotationNoRemediate = "drift-detector/remediate"

// Reconciler holds the dependencies needed for the control loop.
type Reconciler struct {
	cfg     *config.Config
	cluster *cluster.Client
	mu      sync.RWMutex
	last    *report.DriftReport
}

// New constructs a Reconciler.
func New(cfg *config.Config, client *cluster.Client) *Reconciler {
	return &Reconciler{cfg: cfg, cluster: client}
}

// LatestReport returns the most recent drift report — used by the dashboard server.
func (r *Reconciler) LatestReport() *report.DriftReport {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.last
}

// Run starts the periodic reconciliation loop and blocks until ctx is cancelled.
// The first reconciliation runs immediately so operators get feedback right away.
func (r *Reconciler) Run(ctx context.Context) error {
	log.Printf("drift-detector starting: manifests=%s interval=%ds dryRun=%v remediate=%v",
		r.cfg.ManifestsPath, r.cfg.IntervalSeconds, r.cfg.DryRun, r.cfg.Remediate)

	if err := r.RunOnce(ctx); err != nil {
		log.Printf("reconcile error: %v", err)
	}

	ticker := time.NewTicker(time.Duration(r.cfg.IntervalSeconds) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("drift-detector shutting down")
			return nil
		case <-ticker.C:
			if err := r.RunOnce(ctx); err != nil {
				log.Printf("reconcile error: %v", err)
				// We log and continue — a single error shouldn't crash the loop.
				// In production you'd increment a metric here for alerting.
			}
		}
	}
}

// RunOnce performs a single reconciliation pass and returns an error only for
// unrecoverable problems (e.g. can't read manifests). Per-resource errors are
// logged but don't abort the pass so one bad resource doesn't block all others.
func (r *Reconciler) RunOnce(ctx context.Context) error {
	desired, err := loader.LoadManifests(r.cfg.ManifestsPath)
	if err != nil {
		return fmt.Errorf("loading manifests from %s: %w", r.cfg.ManifestsPath, err)
	}

	rep := report.New(r.cfg.DryRun)

	for _, obj := range desired {
		annotations := obj.GetAnnotations()

		// Warn about Helm-managed resources: remediating these conflicts with
		// Helm's own state tracking (it owns managedFields and release metadata).
		// We still detect drift, but flag it so the operator knows.
		if labels := obj.GetLabels(); labels["app.kubernetes.io/managed-by"] == "Helm" {
			log.Printf("warning: %s/%s is Helm-managed — drift detection is safe but remediation may conflict with Helm", obj.GetKind(), obj.GetName())
		}

		// Resource-level exclusion: skip entirely
		if annotations[annotationIgnore] == "true" {
			rep.Add(report.ResourceReport{
				Kind:      obj.GetKind(),
				Name:      obj.GetName(),
				Namespace: obj.GetNamespace(),
				Status:    report.StatusIgnored,
			})
			continue
		}

		// Fetch live state from cluster
		live, err := r.cluster.Get(ctx, obj)
		if err != nil {
			if k8serrors.IsNotFound(err) {
				rep.Add(report.ResourceReport{
					Kind:      obj.GetKind(),
					Name:      obj.GetName(),
					Namespace: obj.GetNamespace(),
					Status:    report.StatusMissing,
				})
				if r.shouldRemediate(annotations) {
					log.Printf("remediating: creating missing %s/%s", obj.GetKind(), obj.GetName())
					if applyErr := r.cluster.Apply(ctx, obj); applyErr != nil {
						log.Printf("apply failed for %s/%s: %v", obj.GetKind(), obj.GetName(), applyErr)
					}
				}
				continue
			}
			// Unexpected error — log and move on to next resource
			log.Printf("get %s/%s failed: %v", obj.GetKind(), obj.GetName(), err)
			continue
		}

		// Diff desired vs live
		drifts, err := differ.Diff(obj, *live, r.cfg.GlobalIgnoreFields)
		if err != nil {
			log.Printf("diff %s/%s failed: %v", obj.GetKind(), obj.GetName(), err)
			continue
		}

		if len(drifts) == 0 {
			rep.Add(report.ResourceReport{
				Kind:      obj.GetKind(),
				Name:      obj.GetName(),
				Namespace: obj.GetNamespace(),
				Status:    report.StatusClean,
			})
			continue
		}

		rep.Add(report.ResourceReport{
			Kind:      obj.GetKind(),
			Name:      obj.GetName(),
			Namespace: obj.GetNamespace(),
			Status:    report.StatusDrifted,
			Fields:    drifts,
		})

		if r.shouldRemediate(annotations) {
			log.Printf("remediating: %d drifted field(s) in %s/%s", len(drifts), obj.GetKind(), obj.GetName())
			if applyErr := r.cluster.Apply(ctx, obj); applyErr != nil {
				log.Printf("apply failed for %s/%s: %v", obj.GetKind(), obj.GetName(), applyErr)
			}
		}
	}

	rep.PrintSummary()

	r.mu.Lock()
	r.last = rep
	r.mu.Unlock()

	return nil
}

// shouldRemediate returns true when auto-remediation is appropriate for this resource.
// Decision hierarchy (outermost wins):
//  1. DryRun flag — always blocks remediation
//  2. Global Remediate config — the operator's default
//  3. Per-resource annotation drift-detector/remediate: "false" — opt-out override
func (r *Reconciler) shouldRemediate(annotations map[string]string) bool {
	if r.cfg.DryRun {
		return false
	}
	if !r.cfg.Remediate {
		return false
	}
	// Per-resource opt-out: annotation value "false" suppresses remediation
	// even when the global policy enables it.
	if annotations[annotationNoRemediate] == "false" {
		return false
	}
	return true
}
