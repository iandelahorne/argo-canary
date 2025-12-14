// Package workers contains the workers that read from the workqueues
// and process rollout changes and pod creation/updates
package workers

import (
	"context"
	"fmt"
	"log"

	rov1 "github.com/argoproj/argo-rollouts/pkg/apis/rollouts/v1alpha1"
	"github.com/argoproj/argo-rollouts/utils/unstructured"
	"github.com/iandelahorne/argo-canary/pkg/constants"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	corev1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

const (
	updateLabelPatch = `{
    "metadata": {
		"labels": {
			"` + rov1.DefaultRolloutUniqueLabelKey + `": "%s"
		}
	}
}`
)

type PodWorker struct {
	Client        kubernetes.Interface
	PodLister     corev1.PodLister
	Queue         workqueue.TypedRateLimitingInterface[types.NamespacedName]
	RolloutLister cache.GenericLister
}

// checkForPodUpdate returns true if the pod doesn't have a label on it, or if the label's value
// doesn't match the rollout's stableRS
func checkForPodUpdate(pod *v1.Pod, podTemplateHash string) bool {
	// check if pod is lacking label
	val, ok := pod.Labels[rov1.DefaultRolloutUniqueLabelKey]
	if !ok {
		return true
	}
	return val != podTemplateHash
}

// patchPod creates a StrategicMergePatch updating (or creating) the rollout-pod-template-hash label on the pod
func patchPod(ctx context.Context, client kubernetes.Interface, namespace string, podName string, rolloutStableRS string) error {
	patch := fmt.Sprintf(updateLabelPatch, rolloutStableRS)
	_, err := client.CoreV1().Pods(namespace).Patch(ctx, podName, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}

// processPod fetches the pod object from the lister. If it has a rollout name in the `ian.delahorne.com/argo-canary` label,
// fetch the rollout (if it exists) and get the rollout's stableRS value.
// Check if the pod requires updating - if so, patch the pod to set the label.
func (w *PodWorker) processPod(ctx context.Context, namespace string, podName string) error {
	// Fetch pod object from lister
	pod, err := w.PodLister.Pods(namespace).Get(podName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Printf("Pod %s/%s not found, likely was deleted, skipping", namespace, podName)
			return nil
		}
		log.Printf("Failed to get pod %s/%s due to error %v", namespace, podName, err)
		return err
	}

	// Fetch rollout name from pod label
	rolloutName, ok := pod.Labels[constants.PodRolloutLabel]
	if !ok {
		log.Printf("pod %s/%s does not have rollout label", namespace, podName)
		return nil
	}

	if rolloutName == "" {
		log.Printf("pod %s/%s rollout label empty", namespace, podName)
		return nil
	}
	// fetch Rollout from lister
	obj, err := w.RolloutLister.Get(fmt.Sprintf("%s/%s", namespace, rolloutName))
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Printf("Rollout %s/%s specfied by Pod %s/%s not found, skipping", namespace, rolloutName, namespace, podName)
			return nil
		}
		log.Printf("Failed to fetch rollout %s/%s: %v", namespace, rolloutName, err)
		return err
	}

	rollout := unstructured.ObjectToRollout(obj)
	if rollout == nil {
		return fmt.Errorf("rollout %s not found", rolloutName)
	}

	// Check if we need to update pod label
	if checkForPodUpdate(pod, rollout.Status.StableRS) {
		log.Printf("Attempting to update pod %s/%s to %s", namespace, podName, rollout.Status.StableRS)
		err := patchPod(ctx, w.Client, namespace, podName, rollout.Status.StableRS)
		if err != nil {
			log.Printf("Error patching pod %s/%s to %s: %v", namespace, podName, rollout.Status.StableRS, err)
			return err
		}
	}
	return nil
}

// processNextWorkItem fetches a new item (pod namespace/name pair) off the queue and passes it to processPod
func (w *PodWorker) processNextWorkItem(ctx context.Context) bool {
	key, shutdown := w.Queue.Get()
	if shutdown {
		log.Println("Shutting down pod worker")
		return false
	}
	// wrap the splitting and processing in a func so we can defer w.Queue.Done()
	err := func(key types.NamespacedName) error {
		defer w.Queue.Done(key)
		log.Println("Processing pod: " + key.String())
		return w.processPod(ctx, key.Namespace, key.Name)
	}(key)

	if err != nil {
		runtime.HandleError(err)
		return true
	}

	return true
}

func (w *PodWorker) Start(ctx context.Context) {
	log.Println("Starting Pod worker")
	for w.processNextWorkItem(ctx) {

	}
}
