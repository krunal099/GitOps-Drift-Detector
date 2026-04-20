// Package differ implements the core drift comparison logic.
//
// Design decisions encoded here:
//
//  1. We only walk the DESIRED object's fields. Extra fields Kubernetes adds
//     to live state (defaults, injected sidecars) are not flagged as drift
//     because they don't represent user intent diverging from reality.
//
//  2. We strip server-managed metadata from live state before comparing.
//     Fields like resourceVersion, uid, and managedFields are cluster
//     bookkeeping — they're always different and carry no signal.
//
//  3. For named-list fields (containers, volumes, env) we match elements
//     by their "name" sub-field rather than by index, so reordering doesn't
//     produce false positives.
//
//  4. The `status` sub-object is always excluded. Status is written by
//     controllers; a user's Git manifest never intentionally sets it.
package differ

import (
	"encoding/json"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// DriftField describes a single field where live state differs from desired.
type DriftField struct {
	// Path is the dot-separated field path, e.g. "spec.replicas" or
	// "spec.template.spec.containers[nginx].image"
	Path    string      `json:"path"`
	Desired interface{} `json:"desired"`
	Actual  interface{} `json:"actual"`
}

// serverManagedPaths are fields Kubernetes writes itself. They are stripped
// from the live object before diffing so they never produce false positives.
// Rule of thumb: if it isn't in a `kubectl apply` manifest, it goes here.
//
// Note: tooling-injected annotations (deployment.kubernetes.io/revision,
// external-dns.alpha.kubernetes.io/*, etc.) do NOT need to be listed here.
// Because we walk the DESIRED object only, any annotation present in live
// state but absent from the Git manifest is automatically ignored.
// We only need to strip fields that would appear in desired if the user
// copied a live object into Git (e.g. last-applied-configuration).
var serverManagedPaths = []string{
	"metadata.resourceVersion",
	"metadata.uid",
	"metadata.creationTimestamp",
	"metadata.generation",
	"metadata.managedFields",
	"metadata.selfLink",
	"metadata.annotations.kubectl.kubernetes.io/last-applied-configuration",
	"status", // controllers own status; users don't express intent there
}

// annotationIgnoreFields is the per-resource annotation key whose value is a
// comma-separated list of dot-path fields to exclude from drift checking.
// Example:
//
//	drift-detector/ignore-fields: "spec.replicas,spec.template.spec.containers[app].resources"
const annotationIgnoreFields = "drift-detector/ignore-fields"

// Diff compares desired (from Git) against live (from cluster) and returns
// the list of fields that have drifted.  globalIgnoreFields comes from the
// global config; per-resource exclusions come from the resource annotation.
//
// Design choice — why we use Approach B (walk desired) rather than 3-way merge:
//
//   Approach A (3-way merge): compare last-applied-config vs desired vs live.
//     This is what `kubectl apply` does internally. It requires storing the
//     previous desired state (in the last-applied-configuration annotation),
//     which means our tool needs to have performed the initial apply.
//     We rejected this because the drift detector should be read-only by
//     default and shouldn't depend on being the tool that created the resources.
//
//   Approach B (walk desired): iterate every field in the desired manifest
//     and check whether the cluster has the same value. Fields only present
//     in the live state (Kubernetes defaults, injected sidecars) are ignored.
//     Simple, read-only, no prior state needed. This is what we implement.
//
//   Approach C (server-side dry-run): send the manifest as a dry-run apply
//     and diff the result against live. The API server handles defaulting for
//     us. Downside: requires write permissions even for read-only checks.
//     Mentioned as a future enhancement for higher accuracy.
func Diff(desired, live unstructured.Unstructured, globalIgnoreFields []string) ([]DriftField, error) {
	// Deep-copy live so we can mutate it without side effects
	liveCopy := deepCopy(live.Object)

	// Strip server-managed fields from the live copy
	for _, p := range serverManagedPaths {
		deleteAtPath(liveCopy, strings.Split(p, "."))
	}

	// Build the ignore set — global config + per-resource annotation
	ignore := make(map[string]bool, len(globalIgnoreFields))
	for _, f := range globalIgnoreFields {
		ignore[strings.TrimSpace(f)] = true
	}
	if ann := desired.GetAnnotations()[annotationIgnoreFields]; ann != "" {
		for _, f := range strings.Split(ann, ",") {
			ignore[strings.TrimSpace(f)] = true
		}
	}

	var drifts []DriftField
	walkDesired(desired.Object, liveCopy, "", ignore, &drifts)
	return drifts, nil
}

// walkDesired recursively walks every field in the desired map and checks
// whether the same field in actual matches. We intentionally do NOT check
// fields that are only in actual — those are Kubernetes defaults and are
// expected to be present in live state even when absent from the manifest.
func walkDesired(desired, actual map[string]interface{}, path string, ignore map[string]bool, out *[]DriftField) {
	for key, dVal := range desired {
		fp := joinPath(path, key)

		if ignore[fp] {
			continue
		}

		aVal, exists := actual[key]
		if !exists {
			*out = append(*out, DriftField{Path: fp, Desired: dVal, Actual: nil})
			continue
		}

		dMap, dIsMap := dVal.(map[string]interface{})
		aMap, aIsMap := aVal.(map[string]interface{})
		if dIsMap && aIsMap {
			walkDesired(dMap, aMap, fp, ignore, out)
			continue
		}

		dSlice, dIsSlice := dVal.([]interface{})
		aSlice, aIsSlice := aVal.([]interface{})
		if dIsSlice && aIsSlice {
			diffSlices(dSlice, aSlice, fp, ignore, out)
			continue
		}

		if !jsonEqual(dVal, aVal) {
			*out = append(*out, DriftField{Path: fp, Desired: dVal, Actual: aVal})
		}
	}
}

// diffSlices compares two slices. If the elements are maps that have a "name"
// key (containers, volumes, env vars, ports…) we match by name so reordering
// isn't treated as drift. Otherwise we fall back to whole-slice JSON equality.
func diffSlices(desired, actual []interface{}, path string, ignore map[string]bool, out *[]DriftField) {
	// Try named-element matching
	if isNamedSlice(desired) && isNamedSlice(actual) {
		actualByName := indexByName(actual)
		for _, di := range desired {
			dm := di.(map[string]interface{})
			name, _ := dm["name"].(string)
			fp := path + "[" + name + "]"

			if ignore[fp] {
				continue
			}

			am, found := actualByName[name]
			if !found {
				*out = append(*out, DriftField{Path: fp, Desired: dm, Actual: nil})
				continue
			}
			walkDesired(dm, am, fp, ignore, out)
		}
		return
	}

	// Fall back: compare entire slice as a JSON value
	if !jsonEqual(desired, actual) {
		*out = append(*out, DriftField{Path: path, Desired: desired, Actual: actual})
	}
}

// isNamedSlice returns true if every element is a map containing a "name" key.
func isNamedSlice(s []interface{}) bool {
	if len(s) == 0 {
		return false
	}
	for _, v := range s {
		m, ok := v.(map[string]interface{})
		if !ok {
			return false
		}
		if _, hasName := m["name"]; !hasName {
			return false
		}
	}
	return true
}

// indexByName builds a map[name]element for quick lookup in named slices.
func indexByName(s []interface{}) map[string]map[string]interface{} {
	idx := make(map[string]map[string]interface{}, len(s))
	for _, v := range s {
		if m, ok := v.(map[string]interface{}); ok {
			if name, ok := m["name"].(string); ok {
				idx[name] = m
			}
		}
	}
	return idx
}

func joinPath(base, key string) string {
	if base == "" {
		return key
	}
	return base + "." + key
}

func jsonEqual(a, b interface{}) bool {
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return string(aj) == string(bj)
}

// deepCopy round-trips through JSON to produce a completely independent copy.
func deepCopy(m map[string]interface{}) map[string]interface{} {
	data, _ := json.Marshal(m)
	var result map[string]interface{}
	_ = json.Unmarshal(data, &result)
	return result
}

// deleteAtPath removes a nested field identified by its path segments.
// Handles the root-level case (len==1) and recurses for nested paths.
func deleteAtPath(m map[string]interface{}, parts []string) {
	if len(parts) == 0 || m == nil {
		return
	}
	if len(parts) == 1 {
		delete(m, parts[0])
		return
	}
	if next, ok := m[parts[0]].(map[string]interface{}); ok {
		deleteAtPath(next, parts[1:])
	}
}
