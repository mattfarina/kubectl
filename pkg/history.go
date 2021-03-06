/*
Copyright 2016 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package pkg

import (
	"bytes"
	"fmt"
	"io"
	"text/tabwriter"

	appsv1beta1 "k8s.io/api/apps/v1beta1"
	"k8s.io/api/core/v1"
	extensionsv1beta1 "k8s.io/api/extensions/v1beta1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/client-go/kubernetes"
	clientappsv1beta1 "k8s.io/client-go/kubernetes/typed/apps/v1beta1"
	clientextv1beta1 "k8s.io/client-go/kubernetes/typed/extensions/v1beta1"
	"k8s.io/kubernetes/pkg/api"
	apiv1 "k8s.io/kubernetes/pkg/api/v1"
	"k8s.io/kubernetes/pkg/apis/apps"
	"k8s.io/kubernetes/pkg/apis/extensions"
	deploymentutil "k8s.io/kubernetes/pkg/controller/deployment/util"
	sliceutil "k8s.io/kubectl/pkg/util/slice"
	printersinternal "k8s.io/kubernetes/pkg/printers/internalversion"
)

const (
	ChangeCauseAnnotation = "kubernetes.io/change-cause"
)

// HistoryViewer provides an interface for resources have historical information.
type HistoryViewer interface {
	ViewHistory(namespace, name string, revision int64) (string, error)
}

func HistoryViewerFor(kind schema.GroupKind, c kubernetes.Interface) (HistoryViewer, error) {
	switch kind {
	case extensions.Kind("Deployment"), apps.Kind("Deployment"):
		return &DeploymentHistoryViewer{c}, nil
	case apps.Kind("StatefulSet"):
		return &StatefulSetHistoryViewer{c}, nil
	case extensions.Kind("DaemonSet"), apps.Kind("DaemonSet"):
		return &DaemonSetHistoryViewer{c}, nil
	}
	return nil, fmt.Errorf("no history viewer has been implemented for %q", kind)
}

type DeploymentHistoryViewer struct {
	c kubernetes.Interface
}

// ViewHistory returns a revision-to-replicaset map as the revision history of a deployment
// TODO: this should be a describer
func (h *DeploymentHistoryViewer) ViewHistory(namespace, name string, revision int64) (string, error) {
	versionedExtensionsClient := h.c.ExtensionsV1beta1()
	deployment, err := versionedExtensionsClient.Deployments(namespace).Get(name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to retrieve deployment %s: %v", name, err)
	}
	_, allOldRSs, newRS, err := deploymentutil.GetAllReplicaSets(deployment, versionedExtensionsClient)
	if err != nil {
		return "", fmt.Errorf("failed to retrieve replica sets from deployment %s: %v", name, err)
	}
	allRSs := allOldRSs
	if newRS != nil {
		allRSs = append(allRSs, newRS)
	}

	historyInfo := make(map[int64]*v1.PodTemplateSpec)
	for _, rs := range allRSs {
		v, err := deploymentutil.Revision(rs)
		if err != nil {
			continue
		}
		historyInfo[v] = &rs.Spec.Template
		changeCause := getChangeCause(rs)
		if historyInfo[v].Annotations == nil {
			historyInfo[v].Annotations = make(map[string]string)
		}
		if len(changeCause) > 0 {
			historyInfo[v].Annotations[ChangeCauseAnnotation] = changeCause
		}
	}

	if len(historyInfo) == 0 {
		return "No rollout history found.", nil
	}

	if revision > 0 {
		// Print details of a specific revision
		template, ok := historyInfo[revision]
		if !ok {
			return "", fmt.Errorf("unable to find the specified revision")
		}
		return printTemplate(template)
	}

	// Sort the revisionToChangeCause map by revision
	revisions := make([]int64, 0, len(historyInfo))
	for r := range historyInfo {
		revisions = append(revisions, r)
	}
	sliceutil.SortInts64(revisions)

	return tabbedString(func(out io.Writer) error {
		fmt.Fprintf(out, "REVISION\tCHANGE-CAUSE\n")
		for _, r := range revisions {
			// Find the change-cause of revision r
			changeCause := historyInfo[r].Annotations[ChangeCauseAnnotation]
			if len(changeCause) == 0 {
				changeCause = "<none>"
			}
			fmt.Fprintf(out, "%d\t%s\n", r, changeCause)
		}
		return nil
	})
}

func printTemplate(template *v1.PodTemplateSpec) (string, error) {
	buf := bytes.NewBuffer([]byte{})
	internalTemplate := &api.PodTemplateSpec{}
	if err := apiv1.Convert_v1_PodTemplateSpec_To_api_PodTemplateSpec(template, internalTemplate, nil); err != nil {
		return "", fmt.Errorf("failed to convert podtemplate, %v", err)
	}
	w := printersinternal.NewPrefixWriter(buf)
	printersinternal.DescribePodTemplate(internalTemplate, w)
	return buf.String(), nil
}

type DaemonSetHistoryViewer struct {
	c kubernetes.Interface
}

// ViewHistory returns a revision-to-history map as the revision history of a deployment
// TODO: this should be a describer
func (h *DaemonSetHistoryViewer) ViewHistory(namespace, name string, revision int64) (string, error) {
	ds, history, err := daemonSetHistory(h.c.ExtensionsV1beta1(), h.c.AppsV1beta1(), namespace, name)
	if err != nil {
		return "", err
	}
	historyInfo := make(map[int64]*appsv1beta1.ControllerRevision)
	for _, history := range history {
		// TODO: for now we assume revisions don't overlap, we may need to handle it
		historyInfo[history.Revision] = history
	}
	if len(historyInfo) == 0 {
		return "No rollout history found.", nil
	}

	// Print details of a specific revision
	if revision > 0 {
		history, ok := historyInfo[revision]
		if !ok {
			return "", fmt.Errorf("unable to find the specified revision")
		}
		dsOfHistory, err := applyDaemonSetHistory(ds, history)
		if err != nil {
			return "", fmt.Errorf("unable to parse history %s", history.Name)
		}
		return printTemplate(&dsOfHistory.Spec.Template)
	}

	// Print an overview of all Revisions
	// Sort the revisionToChangeCause map by revision
	revisions := make([]int64, 0, len(historyInfo))
	for r := range historyInfo {
		revisions = append(revisions, r)
	}
	sliceutil.SortInts64(revisions)

	return tabbedString(func(out io.Writer) error {
		fmt.Fprintf(out, "REVISION\tCHANGE-CAUSE\n")
		for _, r := range revisions {
			// Find the change-cause of revision r
			changeCause := historyInfo[r].Annotations[ChangeCauseAnnotation]
			if len(changeCause) == 0 {
				changeCause = "<none>"
			}
			fmt.Fprintf(out, "%d\t%s\n", r, changeCause)
		}
		return nil
	})
}

type StatefulSetHistoryViewer struct {
	c kubernetes.Interface
}

// ViewHistory returns a list of the revision history of a statefulset
// TODO: this should be a describer
// TODO: needs to implement detailed revision view
func (h *StatefulSetHistoryViewer) ViewHistory(namespace, name string, revision int64) (string, error) {
	_, history, err := statefulSetHistory(h.c.AppsV1beta1(), namespace, name)
	if err != nil {
		return "", err
	}

	if len(history) <= 0 {
		return "No rollout history found.", nil
	}
	revisions := make([]int64, len(history))
	for _, revision := range history {
		revisions = append(revisions, revision.Revision)
	}
	sliceutil.SortInts64(revisions)

	return tabbedString(func(out io.Writer) error {
		fmt.Fprintf(out, "REVISION\n")
		for _, r := range revisions {
			fmt.Fprintf(out, "%d\n", r)
		}
		return nil
	})
}

// controlledHistories returns all ControllerRevisions in namespace that selected by selector and owned by accessor
func controlledHistory(
	apps clientappsv1beta1.AppsV1beta1Interface,
	namespace string,
	selector labels.Selector,
	accessor metav1.Object) ([]*appsv1beta1.ControllerRevision, error) {
	var result []*appsv1beta1.ControllerRevision
	historyList, err := apps.ControllerRevisions(namespace).List(metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return nil, err
	}
	for i := range historyList.Items {
		history := historyList.Items[i]
		// Only add history that belongs to the API object
		if metav1.IsControlledBy(&history, accessor) {
			result = append(result, &history)
		}
	}
	return result, nil
}

// daemonSetHistory returns the DaemonSet named name in namespace and all ControllerRevisions in its history.
func daemonSetHistory(
	ext clientextv1beta1.ExtensionsV1beta1Interface,
	apps clientappsv1beta1.AppsV1beta1Interface,
	namespace, name string) (*extensionsv1beta1.DaemonSet, []*appsv1beta1.ControllerRevision, error) {
	ds, err := ext.DaemonSets(namespace).Get(name, metav1.GetOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to retrieve DaemonSet %s: %v", name, err)
	}
	selector, err := metav1.LabelSelectorAsSelector(ds.Spec.Selector)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create selector for DaemonSet %s: %v", ds.Name, err)
	}
	accessor, err := meta.Accessor(ds)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create accessor for DaemonSet %s: %v", ds.Name, err)
	}
	history, err := controlledHistory(apps, ds.Namespace, selector, accessor)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to find history controlled by DaemonSet %s: %v", ds.Name, err)
	}
	return ds, history, nil
}

// statefulSetHistory returns the StatefulSet named name in namespace and all ControllerRevisions in its history.
func statefulSetHistory(
	apps clientappsv1beta1.AppsV1beta1Interface,
	namespace, name string) (*appsv1beta1.StatefulSet, []*appsv1beta1.ControllerRevision, error) {
	sts, err := apps.StatefulSets(namespace).Get(name, metav1.GetOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to retrieve Statefulset %s: %s", name, err.Error())
	}
	selector, err := metav1.LabelSelectorAsSelector(sts.Spec.Selector)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create selector for StatefulSet %s: %s", name, err.Error())
	}
	accessor, err := meta.Accessor(sts)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to obtain accessor for StatefulSet %s: %s", name, err.Error())
	}
	history, err := controlledHistory(apps, namespace, selector, accessor)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to find history controlled by StatefulSet %s: %v", name, err)
	}
	return sts, history, nil
}

// applyDaemonSetHistory returns a specific revision of DaemonSet by applying the given history to a copy of the given DaemonSet
func applyDaemonSetHistory(ds *extensionsv1beta1.DaemonSet, history *appsv1beta1.ControllerRevision) (*extensionsv1beta1.DaemonSet, error) {
	clone := ds.DeepCopy()
	cloneBytes, err := json.Marshal(clone)
	if err != nil {
		return nil, err
	}
	patched, err := strategicpatch.StrategicMergePatch(cloneBytes, history.Data.Raw, clone)
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(patched, clone)
	if err != nil {
		return nil, err
	}
	return clone, nil
}

// TODO: copied here until this becomes a describer
func tabbedString(f func(io.Writer) error) (string, error) {
	out := new(tabwriter.Writer)
	buf := &bytes.Buffer{}
	out.Init(buf, 0, 8, 2, ' ', 0)

	err := f(out)
	if err != nil {
		return "", err
	}

	out.Flush()
	str := string(buf.String())
	return str, nil
}

// getChangeCause returns the change-cause annotation of the input object
func getChangeCause(obj runtime.Object) string {
	accessor, err := meta.Accessor(obj)
	if err != nil {
		return ""
	}
	return accessor.GetAnnotations()[ChangeCauseAnnotation]
}
