// Package loader reads Kubernetes manifests from a local directory.
// In a real GitOps system this directory would be a git clone or a
// sparse checkout — keeping the git layer separate makes it easy to
// swap in go-git, a git sidecar, or a CI-driven sync later.
package loader

import (
	"os"
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// LoadManifests walks dir recursively and returns every valid Kubernetes
// object it finds. Multi-document YAML files (--- separator) are supported.
func LoadManifests(dir string) ([]unstructured.Unstructured, error) {
	var objects []unstructured.Unstructured

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		for _, doc := range splitDocuments(data) {
			trimmed := strings.TrimSpace(string(doc))
			if trimmed == "" || trimmed == "---" {
				continue
			}

			var raw map[string]interface{}
			if err := yaml.Unmarshal(doc, &raw); err != nil {
				return err
			}
			if raw == nil {
				continue
			}

			u := unstructured.Unstructured{Object: raw}
			// Skip anything that isn't a real Kubernetes resource
			if u.GetKind() == "" || u.GetName() == "" {
				continue
			}
			objects = append(objects, u)
		}
		return nil
	})

	return objects, err
}

// splitDocuments splits a YAML byte slice on the "---" document separator.
func splitDocuments(data []byte) [][]byte {
	var docs [][]byte
	var current []byte
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "---" {
			if len(current) > 0 {
				docs = append(docs, current)
				current = nil
			}
		} else {
			current = append(current, []byte(line+"\n")...)
		}
	}
	if len(current) > 0 {
		docs = append(docs, current)
	}
	return docs
}
