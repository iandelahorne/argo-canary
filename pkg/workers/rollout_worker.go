package workers

import (
	"context"
	"log"

	"github.com/iandelahorne/argo-canary/pkg/constants"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	corev1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

type RolloutWorker struct {
	Client        kubernetes.Interface
	PodLister     corev1.PodLister
	Queue         workqueue.TypedRateLimitingInterface[types.NamespacedName]
	PodQueue      workqueue.TypedRateLimitingInterface[types.NamespacedName]
	RolloutLister cache.GenericLister
}

// processRollout fetches Pods that match the rollout's name by using a label selector passed to the pod Lister
// ith then add
func (w *RolloutWorker) processRollout(ctx context.Context, namespace, rolloutName string) error {
	// fetch Pods with the label `ian.delahorne.com/argo-canary` that contain this rollout's name
	labelSelector := labels.NewSelector()
	req, err := labels.NewRequirement(constants.PodRolloutLabel, selection.Equals, []string{rolloutName})
	if err != nil {

		return err
	}
	labelSelector = labelSelector.Add(*req)

	// fetch all pods matching the label selector
	pods, err := w.PodLister.Pods(namespace).List(labelSelector)
	if err != nil {
		return err
	}

	// add all pods found to the pod update queue
	for _, pod := range pods {
		key := types.NamespacedName{
			Namespace: pod.GetNamespace(),
			Name:      pod.GetName(),
		}
		w.PodQueue.AddRateLimited(key)
	}

	return nil
}

// processNextWorkItem fetches a new item (rollout namespace/name pair) off the queue and passes it to processRollout
// If it receives the shutdown function, it exits.
func (w *RolloutWorker) processNextWorkItem(ctx context.Context) bool {
	key, shutdown := w.Queue.Get()
	if shutdown {
		log.Println("Shutting down Rollout worker")
		return false
	}
	// wrap the splitting and processing in a func so we can defer w.Queue.Done()
	err := func(key types.NamespacedName) error {
		defer w.Queue.Done(key)
		log.Println("Processing rollout: " + key.String())
		return w.processRollout(ctx, key.Namespace, key.Name)
	}(key)

	if err != nil {
		runtime.HandleError(err)
		return true
	}

	return true
}

func (w *RolloutWorker) Start(ctx context.Context) {
	log.Println("Starting Rollout worker")
	for w.processNextWorkItem(ctx) {
	}
}
