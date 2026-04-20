// Package cluster wraps the Kubernetes dynamic client.
// We use the dynamic client (rather than typed clients) so we can handle
// any resource kind without generating typed stubs for each one.
package cluster

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

// Client wraps the dynamic Kubernetes client with helpers for drift detection.
type Client struct {
	dyn dynamic.Interface
}

// New builds a Client. kubeconfig path is optional — if empty it tries
// in-cluster config first, then ~/.kube/config.
func New(kubeconfig string) (*Client, error) {
	cfg, err := buildRestConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("building kubeconfig: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{dyn: dyn}, nil
}

// Get fetches the live cluster state for the given object.
func (c *Client) Get(ctx context.Context, obj unstructured.Unstructured) (*unstructured.Unstructured, error) {
	ri, err := c.resourceInterface(obj)
	if err != nil {
		return nil, err
	}
	return ri.Get(ctx, obj.GetName(), metav1.GetOptions{})
}

// Apply uses server-side apply to write the desired state back to the cluster.
// FieldManager "drift-detector" means Kubernetes tracks which fields we own,
// so future applies don't stomp on fields owned by other controllers (e.g. HPA).
//
// Immutable fields: some fields cannot be patched after creation (e.g.
// spec.selector on Deployments, spec.claimRef on PVs). If the drift involves
// one of these fields, Kubernetes will reject our apply with a 422 Unprocessable
// Entity. The caller logs the error and moves on — we never crash-loop.
// Fixing immutable field drift requires a delete+recreate, which this tool
// deliberately does not do automatically (too destructive without human review).
func (c *Client) Apply(ctx context.Context, obj unstructured.Unstructured) error {
	ri, err := c.resourceInterface(obj)
	if err != nil {
		return err
	}
	data, err := obj.MarshalJSON()
	if err != nil {
		return err
	}
	force := true
	_, err = ri.Patch(ctx, obj.GetName(), types.ApplyPatchType, data,
		metav1.PatchOptions{FieldManager: "drift-detector", Force: &force})
	return err
}

func (c *Client) resourceInterface(obj unstructured.Unstructured) (dynamic.ResourceInterface, error) {
	gvr, err := kindToGVR(obj.GetKind(), obj.GetAPIVersion())
	if err != nil {
		return nil, err
	}
	ns := obj.GetNamespace()
	if ns != "" {
		return c.dyn.Resource(gvr).Namespace(ns), nil
	}
	return c.dyn.Resource(gvr), nil
}

func buildRestConfig(kubeconfig string) (*rest.Config, error) {
	// 1. Explicit path
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	// 2. In-cluster (when running as a pod)
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	// 3. Default ~/.kube/config
	home := homedir.HomeDir()
	return clientcmd.BuildConfigFromFlags("", filepath.Join(home, ".kube", "config"))
}

// kindToGVR maps a Kubernetes Kind + apiVersion string to a GroupVersionResource
// that the dynamic client needs. In production you'd call the discovery API
// at startup to build this map automatically; here we maintain it statically
// for the common resource types we scope to.
func kindToGVR(kind, apiVersion string) (schema.GroupVersionResource, error) {
	var group, version string
	parts := strings.SplitN(apiVersion, "/", 2)
	if len(parts) == 1 {
		group, version = "", parts[0]
	} else {
		group, version = parts[0], parts[1]
	}

	resource, ok := kindResourceMap[kind]
	if !ok {
		return schema.GroupVersionResource{}, fmt.Errorf("unsupported kind %q — add it to kindResourceMap", kind)
	}

	return schema.GroupVersionResource{Group: group, Version: version, Resource: resource}, nil
}

// kindResourceMap is the static Kind → plural-resource-name table.
// Limitation: cluster-scoped resources (Node, PersistentVolume) work here only
// if queried without a namespace, which resourceInterface handles correctly.
var kindResourceMap = map[string]string{
	"Deployment":             "deployments",
	"StatefulSet":            "statefulsets",
	"DaemonSet":              "daemonsets",
	"ReplicaSet":             "replicasets",
	"Job":                    "jobs",
	"CronJob":                "cronjobs",
	"Service":                "services",
	"ConfigMap":              "configmaps",
	"Secret":                 "secrets",
	"Ingress":                "ingresses",
	"ServiceAccount":         "serviceaccounts",
	"PersistentVolumeClaim":  "persistentvolumeclaims",
	"HorizontalPodAutoscaler": "horizontalpodautoscalers",
	"Namespace":              "namespaces",
}
