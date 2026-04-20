package differ_test

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/krunalp/gitops-drift-detector/pkg/differ"
)

// makeObj is a helper that builds an unstructured Kubernetes object from a raw map.
func makeObj(raw map[string]interface{}) unstructured.Unstructured {
	return unstructured.Unstructured{Object: raw}
}

// ── Test 1: clean — no drift ───────────────────────────────────────────────

func TestDiff_Clean(t *testing.T) {
	desired := makeObj(map[string]interface{}{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]interface{}{"name": "web", "namespace": "default"},
		"spec": map[string]interface{}{
			"replicas": int64(3),
			"template": map[string]interface{}{
				"spec": map[string]interface{}{
					"containers": []interface{}{
						map[string]interface{}{
							"name":  "app",
							"image": "nginx:1.25",
						},
					},
				},
			},
		},
	})

	// Live state: same spec + server-added fields that should be ignored
	live := makeObj(map[string]interface{}{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]interface{}{
			"name":            "web",
			"namespace":       "default",
			"resourceVersion": "12345",      // server-managed — must be ignored
			"uid":             "abc-def-ghi", // server-managed — must be ignored
			"generation":      int64(2),      // server-managed — must be ignored
			"managedFields":   []interface{}{map[string]interface{}{"manager": "kubectl"}},
		},
		"spec": map[string]interface{}{
			"replicas": int64(3),
			"template": map[string]interface{}{
				"spec": map[string]interface{}{
					"containers": []interface{}{
						map[string]interface{}{
							"name":            "app",
							"image":           "nginx:1.25",
							"imagePullPolicy": "IfNotPresent", // kubernetes default — not in desired, must not flag
						},
					},
				},
			},
		},
		"status": map[string]interface{}{ // status is always excluded
			"readyReplicas": int64(3),
		},
	})

	drifts, err := differ.Diff(desired, live, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(drifts) != 0 {
		t.Errorf("expected no drift, got %d:\n%+v", len(drifts), drifts)
	}
}

// ── Test 2: image tag changed in cluster ───────────────────────────────────

func TestDiff_ImageDrift(t *testing.T) {
	desired := makeObj(map[string]interface{}{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]interface{}{"name": "web", "namespace": "default"},
		"spec": map[string]interface{}{
			"template": map[string]interface{}{
				"spec": map[string]interface{}{
					"containers": []interface{}{
						map[string]interface{}{
							"name":  "app",
							"image": "nginx:1.25", // desired: 1.25
						},
					},
				},
			},
		},
	})

	live := makeObj(map[string]interface{}{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]interface{}{"name": "web", "namespace": "default", "resourceVersion": "999"},
		"spec": map[string]interface{}{
			"template": map[string]interface{}{
				"spec": map[string]interface{}{
					"containers": []interface{}{
						map[string]interface{}{
							"name":  "app",
							"image": "nginx:1.99", // drifted: someone bumped the image in-cluster
						},
					},
				},
			},
		},
	})

	drifts, err := differ.Diff(desired, live, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(drifts) != 1 {
		t.Fatalf("expected 1 drift, got %d: %+v", len(drifts), drifts)
	}
	d := drifts[0]
	if d.Path != "spec.template.spec.containers[app].image" {
		t.Errorf("wrong path: got %q", d.Path)
	}
	if d.Desired != "nginx:1.25" {
		t.Errorf("wrong desired: got %v", d.Desired)
	}
	if d.Actual != "nginx:1.99" {
		t.Errorf("wrong actual: got %v", d.Actual)
	}
}

// ── Test 3: replica count excluded via global ignore ──────────────────────
// Scenario: HPA manages replicas, so we configure spec.replicas in globalIgnoreFields.

func TestDiff_GlobalIgnore_Replicas(t *testing.T) {
	desired := makeObj(map[string]interface{}{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]interface{}{"name": "web", "namespace": "default"},
		"spec":       map[string]interface{}{"replicas": int64(1)},
	})

	live := makeObj(map[string]interface{}{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]interface{}{"name": "web", "namespace": "default"},
		"spec":       map[string]interface{}{"replicas": int64(5)}, // HPA scaled it
	})

	// Without ignore: drift detected
	drifts, _ := differ.Diff(desired, live, nil)
	if len(drifts) == 0 {
		t.Error("expected drift without globalIgnoreFields, got none")
	}

	// With global ignore: no drift
	drifts, err := differ.Diff(desired, live, []string{"spec.replicas"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(drifts) != 0 {
		t.Errorf("expected no drift with globalIgnoreFields, got %d: %+v", len(drifts), drifts)
	}
}

// ── Test 4: per-resource annotation exclusion ─────────────────────────────

func TestDiff_AnnotationIgnore(t *testing.T) {
	desired := makeObj(map[string]interface{}{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]interface{}{
			"name":      "web",
			"namespace": "default",
			"annotations": map[string]interface{}{
				// This particular resource intentionally lets the HPA control replicas.
				"drift-detector/ignore-fields": "spec.replicas",
			},
		},
		"spec": map[string]interface{}{
			"replicas": int64(2),
			"selector": map[string]interface{}{"matchLabels": map[string]interface{}{"app": "web"}},
		},
	})

	live := makeObj(map[string]interface{}{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]interface{}{
			"name":      "web",
			"namespace": "default",
			// The annotation is present in the cluster too (it was applied from Git).
			// The differ reads exclusions from the DESIRED object, not live,
			// so both having it is realistic and correct.
			"annotations": map[string]interface{}{
				"drift-detector/ignore-fields": "spec.replicas",
			},
		},
		"spec": map[string]interface{}{
			"replicas": int64(10), // HPA-managed, excluded by annotation
			"selector": map[string]interface{}{"matchLabels": map[string]interface{}{"app": "web"}},
		},
	})

	drifts, err := differ.Diff(desired, live, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(drifts) != 0 {
		t.Errorf("expected no drift after annotation exclusion, got %d: %+v", len(drifts), drifts)
	}
}

// ── Test 5: missing field in live ─────────────────────────────────────────

func TestDiff_MissingField(t *testing.T) {
	desired := makeObj(map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]interface{}{"name": "app-config", "namespace": "default"},
		"data": map[string]interface{}{
			"LOG_LEVEL": "info",
			"PORT":      "8080",
		},
	})

	live := makeObj(map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]interface{}{"name": "app-config", "namespace": "default"},
		"data": map[string]interface{}{
			"LOG_LEVEL": "info",
			// PORT was manually deleted from the cluster
		},
	})

	drifts, err := differ.Diff(desired, live, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(drifts) != 1 {
		t.Fatalf("expected 1 drift, got %d: %+v", len(drifts), drifts)
	}
	if drifts[0].Path != "data.PORT" {
		t.Errorf("expected data.PORT drift, got %q", drifts[0].Path)
	}
}

// ── Test 6: label and annotation drift is detected ────────────────────────
// Labels and annotations are user-controlled metadata. Changes to them
// should be flagged as drift, unlike server-managed annotations.

func TestDiff_LabelDrift(t *testing.T) {
	desired := makeObj(map[string]interface{}{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]interface{}{
			"name":      "web",
			"namespace": "default",
			"labels": map[string]interface{}{
				"app":     "web",
				"version": "v1.0", // desired version label
			},
		},
	})

	live := makeObj(map[string]interface{}{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]interface{}{
			"name":      "web",
			"namespace": "default",
			"labels": map[string]interface{}{
				"app":     "web",
				"version": "v2.0", // someone changed this label directly in the cluster
			},
		},
	})

	drifts, err := differ.Diff(desired, live, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(drifts) != 1 {
		t.Fatalf("expected 1 drift for label change, got %d: %+v", len(drifts), drifts)
	}
	if drifts[0].Path != "metadata.labels.version" {
		t.Errorf("expected metadata.labels.version, got %q", drifts[0].Path)
	}
}

// ── Test 7: tooling-injected annotations do not produce false positives ───
// Annotations added by Kubernetes tooling (deployment.kubernetes.io/revision,
// external-dns, etc.) should never be flagged. Because we walk the DESIRED
// object only, any annotation present in live but absent from Git is ignored
// automatically — no explicit stripping needed.

func TestDiff_ToolingAnnotationsIgnored(t *testing.T) {
	desired := makeObj(map[string]interface{}{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]interface{}{
			"name":      "web",
			"namespace": "default",
			"annotations": map[string]interface{}{
				"my-team/owner": "platform", // user-defined annotation
			},
		},
		"spec": map[string]interface{}{"replicas": int64(1)},
	})

	live := makeObj(map[string]interface{}{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]interface{}{
			"name":      "web",
			"namespace": "default",
			"annotations": map[string]interface{}{
				"my-team/owner":                         "platform",
				"deployment.kubernetes.io/revision":     "5",                         // injected by Deployment controller
				"external-dns.alpha.kubernetes.io/ttl":  "60",                        // injected by external-dns
				"kubectl.kubernetes.io/last-applied-configuration": "{...stripped}", // removed by serverManagedPaths
			},
		},
		"spec": map[string]interface{}{"replicas": int64(1)},
	})

	drifts, err := differ.Diff(desired, live, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(drifts) != 0 {
		t.Errorf("tooling annotations caused false positives: %+v", drifts)
	}
}

// ── Test 8: server-managed fields are never flagged ───────────────────────

func TestDiff_ServerFieldsIgnored(t *testing.T) {
	desired := makeObj(map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]interface{}{"name": "cfg", "namespace": "default"},
		"data":       map[string]interface{}{"key": "value"},
	})

	live := makeObj(map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]interface{}{
			"name":            "cfg",
			"namespace":       "default",
			"resourceVersion": "99999",
			"uid":             "dead-beef",
			"generation":      int64(7),
			"managedFields":   []interface{}{},
			"selfLink":        "/api/v1/namespaces/default/configmaps/cfg",
		},
		"data":   map[string]interface{}{"key": "value"},
		"status": map[string]interface{}{"conditions": []interface{}{}},
	})

	drifts, err := differ.Diff(desired, live, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(drifts) != 0 {
		t.Errorf("server-managed fields produced false positives: %+v", drifts)
	}
}
