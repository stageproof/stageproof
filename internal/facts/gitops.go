package facts

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func chartsFromApplication(obj map[string]any) []Chart {
	if obj == nil {
		return nil
	}
	release := nestedText(obj, "metadata", "name")
	namespace := nestedText(obj, "spec", "destination", "namespace")
	if namespace == "" {
		namespace = nestedText(obj, "metadata", "namespace")
	}
	var out []Chart
	if chart := chartFromSource(obj, release, namespace, "spec", "source"); chart.Name != "" {
		out = append(out, chart)
	}
	sources, found, err := unstructured.NestedSlice(obj, "spec", "sources")
	if err != nil || !found {
		return out
	}
	for _, item := range sources {
		src, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := nestedText(src, "chart")
		if name == "" {
			continue
		}
		out = append(out, Chart{
			Source:    sourceArgo,
			Name:      name,
			Version:   nestedText(src, "targetRevision"),
			Release:   release,
			Namespace: namespace,
		})
	}
	return out
}

func chartFromSource(obj map[string]any, release, namespace string, fields ...string) Chart {
	name := nestedText(obj, append(append([]string{}, fields...), "chart")...)
	if name == "" {
		return Chart{}
	}
	return Chart{
		Source:    sourceArgo,
		Name:      name,
		Version:   nestedText(obj, append(append([]string{}, fields...), "targetRevision")...),
		Release:   release,
		Namespace: namespace,
	}
}

func chartsFromHelmRelease(obj map[string]any) []Chart {
	if obj == nil {
		return nil
	}
	release := nestedText(obj, "spec", "releaseName")
	if release == "" {
		release = nestedText(obj, "metadata", "name")
	}
	namespace := nestedText(obj, "spec", "targetNamespace")
	if namespace == "" {
		namespace = nestedText(obj, "metadata", "namespace")
	}
	name := nestedText(obj, "spec", "chart", "spec", "chart")
	version := nestedText(obj, "spec", "chart", "spec", "version")
	if name == "" {
		name = nestedText(obj, "spec", "chartRef", "name")
	}
	if name == "" {
		return nil
	}
	return []Chart{{
		Source:    sourceFlux,
		Name:      name,
		Version:   version,
		Release:   release,
		Namespace: namespace,
	}}
}

func nestedText(obj map[string]any, fields ...string) string {
	v, found, err := unstructured.NestedFieldNoCopy(obj, fields...)
	if !found || err != nil || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case int, int32, int64, float32, float64, bool:
		return fmt.Sprint(t)
	default:
		return ""
	}
}
