// Copyright 2019 The Operator-SDK Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package kubemetrics

import (
	"errors"
	"fmt"
	"strings"

	"github.com/operator-framework/operator-sdk/pkg/k8sutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	ksmetric "k8s.io/kube-state-metrics/pkg/metric"
	metricsstore "k8s.io/kube-state-metrics/pkg/metrics_store"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

var log = logf.Log.WithName("kubemetrics")

// GenerateAndServeCRMetrics generates CustomResource specific metrics for each custom resource GVK in operatorGVKs.
// A list of namespaces, ns, can be passed to ServeCRMetrics to scope the generated metrics. Passing nil or
// an empty list of namespaces will result in an error.
// The function also starts serving the generated collections of the metrics on given host and port.
func GenerateAndServeCRMetrics(cfg *rest.Config,
	ns []string,
	operatorGVKs []schema.GroupVersionKind,
	host string, port int32) error {
	// We have to have at least one namespace.
	if len(ns) < 1 {
		return errors.New(
			"namespaces were empty; pass at least one namespace to generate custom resource metrics")
	}
	// Create new unstructured client.
	var allStores [][]*metricsstore.MetricsStore
	log.V(1).Info("Starting collecting operator types")

	apiResourceLists, err := getAPIResourceLists(cfg)
	if err != nil {
		return err
	}

	// Loop through all the possible operator/custom resource specific types.
	for _, gvk := range operatorGVKs {
		apiVersion := gvk.GroupVersion().String()
		kind := gvk.Kind
		// Generate metric based on the kind.
		metricFamilies := generateMetricFamilies(gvk.Kind)
		log.V(1).Info("Generating metric families", "apiVersion", apiVersion, "kind", kind)
		dclient, err := newClientForGVK(cfg, apiResourceLists, apiVersion, kind)
		if err != nil {
			return err
		}
		namespaced, err := isNamespaced(gvk, apiResourceLists)
		if err != nil {
			return err
		}
		var gvkStores []*metricsstore.MetricsStore
		if namespaced {
			gvkStores = NewNamespacedMetricsStores(dclient, ns, apiVersion, kind, metricFamilies)
		} else {
			gvkStores = NewClusterScopedMetricsStores(dclient, apiVersion, kind, metricFamilies)
		}
		// Generate collector based on the group/version, kind and the metric families.

		allStores = append(allStores, gvkStores)
	}
	// Start serving metrics.
	log.V(1).Info("Starting serving custom resource metrics")
	go ServeMetrics(allStores, host, port)

	return nil
}

func generateMetricFamilies(kind string) []ksmetric.FamilyGenerator {
	helpText := fmt.Sprintf("Information about the %s custom resource.", kind)
	kindName := strings.ToLower(kind)
	metricName := fmt.Sprintf("%s_info", kindName)

	return []ksmetric.FamilyGenerator{
		ksmetric.FamilyGenerator{
			Name: metricName,
			Type: ksmetric.Gauge,
			Help: helpText,
			GenerateFunc: func(obj interface{}) *ksmetric.Family {
				crd := obj.(*unstructured.Unstructured)
				return &ksmetric.Family{
					Metrics: []*ksmetric.Metric{
						{
							Value:       1,
							LabelKeys:   []string{"namespace", kindName},
							LabelValues: []string{crd.GetNamespace(), crd.GetName()},
						},
					},
				}
			},
		},
	}
}

// GetNamespacesForMetrics wil return all namespaces which will be used to export the metrics
func GetNamespacesForMetrics(operatorNs string) ([]string, error) {
	ns := []string{operatorNs}

	// Get the value from WATCH_NAMESPACES
	watchNamespace, err := k8sutil.GetWatchNamespace()
	if err != nil {
		return nil, err
	}

	// Generate metrics from the WATCH_NAMESPACES value if it contains multiple namespaces
	if strings.Contains(watchNamespace, ",") {
		ns = strings.Split(watchNamespace, ",")
	}
	return ns, nil
}

func isNamespaced(gvk schema.GroupVersionKind, resourceLists []*metav1.APIResourceList) (bool, error) {
	for _, resourceList := range resourceLists {
		if resourceList.GroupVersion == gvk.GroupVersion().String() {
			for _, apiResource := range resourceList.APIResources {
				if apiResource.Kind == gvk.Kind {
					return apiResource.Namespaced, nil
				}
			}
		}
	}
	return false, errors.New("unable to find type: " + gvk.String() + " in server")
}
