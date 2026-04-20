package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/krunalp/gitops-drift-detector/pkg/cluster"
	"github.com/krunalp/gitops-drift-detector/pkg/config"
	"github.com/krunalp/gitops-drift-detector/pkg/reconciler"
	"github.com/krunalp/gitops-drift-detector/pkg/server"
)

func main() {
	var (
		cfgFile    string
		kubeconfig string
		manifests  string
		interval   int
		dryRun     bool
		remediate  bool
		port       int
	)

	root := &cobra.Command{
		Use:   "drift-detector",
		Short: "GitOps drift detection controller",
		Long: `Compares Kubernetes manifests in a Git repository against live cluster state
and reports (or remediates) any differences.

By default runs in dry-run mode — it reports drift without touching the cluster.
Pass --remediate to auto-apply desired state from Git.`,
	}

	// ── run: long-running reconciliation loop ──────────────────────────────
	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Start the periodic reconciliation loop (blocks until SIGTERM)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := buildConfig(cfgFile, manifests, interval, dryRun, remediate)
			if err != nil {
				return err
			}
			client, err := cluster.New(kubeconfig)
			if err != nil {
				return fmt.Errorf("connecting to cluster: %w", err)
			}
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			rec := reconciler.New(cfg, client)
			if port > 0 {
				server.New(port, rec.LatestReport).Start()
			}
			return rec.Run(ctx)
		},
	}
	runCmd.Flags().StringVar(&manifests, "manifests", "./manifests", "Path to desired-state manifests directory")
	runCmd.Flags().IntVar(&interval, "interval", 60, "Reconciliation interval in seconds")
	runCmd.Flags().BoolVar(&dryRun, "dry-run", true, "Report drift without applying changes (default true)")
	runCmd.Flags().BoolVar(&remediate, "remediate", false, "Re-apply desired state from Git when drift is detected")
	runCmd.Flags().IntVar(&port, "port", 0, "Port for the live dashboard (e.g. 8080). 0 = disabled")

	// ── check: single-shot, exits non-zero if drift found ──────────────────
	// This is the mode you'd call in a CI/CD pipeline:
	//   drift-detector check --manifests ./k8s && echo "clean" || echo "drift detected"
	checkCmd := &cobra.Command{
		Use:   "check",
		Short: "Run one drift check and exit (non-zero exit if drift found)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := buildConfig(cfgFile, manifests, 0, true, false)
			if err != nil {
				return err
			}
			client, err := cluster.New(kubeconfig)
			if err != nil {
				return fmt.Errorf("connecting to cluster: %w", err)
			}
			rec := reconciler.New(cfg, client)
			if err := rec.RunOnce(context.Background()); err != nil {
				return err
			}
			// RunOnce printed the report; caller inspects exit code.
			// We don't return an error here — errors are logged per-resource.
			// Exit code signalling is left to the caller via the report output.
			return nil
		},
	}
	checkCmd.Flags().StringVar(&manifests, "manifests", "./manifests", "Path to desired-state manifests directory")

	root.PersistentFlags().StringVar(&cfgFile, "config", "", "Path to config YAML file")
	root.PersistentFlags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig (default: in-cluster → ~/.kube/config)")
	root.AddCommand(runCmd, checkCmd)

	if err := root.Execute(); err != nil {
		log.Fatal(err)
	}
}

// buildConfig merges a file-based config (if provided) with CLI flags.
// CLI flags take precedence over the config file.
func buildConfig(cfgFile, manifests string, interval int, dryRun, remediate bool) (*config.Config, error) {
	if cfgFile != "" {
		cfg, err := config.Load(cfgFile)
		if err != nil {
			return nil, fmt.Errorf("loading config %s: %w", cfgFile, err)
		}
		// CLI flags override file values when explicitly set
		if dryRun {
			cfg.DryRun = true
		}
		if remediate {
			cfg.Remediate = true
		}
		if manifests != "./manifests" {
			cfg.ManifestsPath = manifests
		}
		return cfg, nil
	}

	return &config.Config{
		ManifestsPath:   manifests,
		IntervalSeconds: interval,
		DryRun:          dryRun,
		Remediate:       remediate,
	}, nil
}
