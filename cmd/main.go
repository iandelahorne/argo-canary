package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/argoproj/argo-rollouts/utils/unstructured"
	"github.com/iandelahorne/argo-canary/pkg/constants"
	"github.com/iandelahorne/argo-canary/pkg/helpers"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"

	//"github.com/iandelahorne/argo-canary/pkg/informers"
	"github.com/iandelahorne/argo-canary/pkg/workers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	kubeConfig := os.Getenv("KUBECONFIG")

	var clusterConfig *rest.Config
	var err error
	if kubeConfig != "" {
		clusterConfig, err = clientcmd.BuildConfigFromFlags("", kubeConfig)
	} else {
		clusterConfig, err = rest.InClusterConfig()
	}
	if err != nil {
		log.Fatalln(err)
	}

	// We need both a kubernetes client for the pods, and a clusterClient for the rollouts
	client, err := kubernetes.NewForConfig(clusterConfig)
	if err != nil {
		log.Fatalln(err)
	}

	clusterClient, err := dynamic.NewForConfig(clusterConfig)
	if err != nil {
		log.Fatalln(err)
	}
	log.Println("argo-canary is up and running")

	// set up a context that will be marked Done when the process receives SIGINT (Ctrl-C)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Create worker queues for the workers that handle update to pods and rollouts.
	// We're just passing strings in here for now on the format `{namespace}/{name}` - in the future these could be a types.NamespacedName to not require splitting
	podQueue := workqueue.NewTypedRateLimitingQueue[string](workqueue.NewTypedItemExponentialFailureRateLimiter[string](time.Millisecond, 10*time.Second))
	rolloutQueue := workqueue.NewTypedRateLimitingQueue[string](workqueue.NewTypedItemExponentialFailureRateLimiter[string](time.Millisecond, 10*time.Second))

	// Set up an informer for pods. We'll pass a label selector to the ListOptions so it only filters
	// pods that have the canary rollout label on them
	sharedInformerFactory := informers.NewSharedInformerFactoryWithOptions(client, 30*time.Minute,
		informers.WithTweakListOptions(func(opts *metav1.ListOptions) {
			opts.LabelSelector = constants.PodRolloutLabel
		}),
	)

	// The informer listens for all pods matching the label selector
	podInformer := sharedInformerFactory.Core().V1().Pods().Informer()

	// The lister is used for fetching Pods out of the cache in the worker (instead of needing to fetch with a client)
	podLister := sharedInformerFactory.Core().V1().Pods().Lister()

	// Add an event handler that will simply add the pod to the queue if it is created or updated.
	_, err = podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			pod := helpers.ObjectToPod(obj)
			podQueue.AddRateLimited(fmt.Sprintf("%s/%s", pod.GetNamespace(), pod.GetName()))
		},
		UpdateFunc: func(oldObj, newObj any) {
			pod := helpers.ObjectToPod(newObj)
			podQueue.AddRateLimited(fmt.Sprintf("%s/%s", pod.GetNamespace(), pod.GetName()))
		},
		DeleteFunc: func(obj any) {
			pod := helpers.ObjectToPod(obj)
			log.Printf("Pod Deleted: %s/%s", pod.GetNamespace(), pod.GetName())
		},
	})
	if err != nil {
		log.Fatalln(err)
	}

	// Since the rollout CRD isn't a pod or other base object, we need to create a dynamic informer from a resource
	resource := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}

	// Set up an informer for rollouts. Since this is not a base object, we need to have a DynamicInformerFactory
	rolloutInformerFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(clusterClient, 30*time.Minute, metav1.NamespaceAll, nil)
	rolloutInformer := rolloutInformerFactory.ForResource(resource).Informer()

	// The lister is used for fetching rollouts out of the cache in the worker (instead of needing to fetch with a client)
	rolloutLister := rolloutInformerFactory.ForResource(resource).Lister()

	// Add an event handler that will simply add the rollout to the queue if it is createed or updated.
	_, err = rolloutInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			rollout := unstructured.ObjectToRollout(obj)
			rolloutQueue.AddRateLimited(fmt.Sprintf("%s/%s", rollout.GetNamespace(), rollout.GetName()))
		},
		UpdateFunc: func(oldObj, newObj any) {
			rollout := unstructured.ObjectToRollout(newObj)
			rolloutQueue.AddRateLimited(fmt.Sprintf("%s/%s", rollout.GetNamespace(), rollout.GetName()))
		},
		DeleteFunc: func(obj any) {
			rollout := unstructured.ObjectToRollout(obj)
			log.Printf("Rollout Deleted: %s/%s: %v\n", rollout.GetNamespace(), rollout.GetName(), rollout.Status.StableRS)
		},
	})
	if err != nil {
		log.Fatalln(err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		// Start pod worker
		defer wg.Done()
		podWorker := &workers.PodWorker{
			Client:        client,
			PodLister:     podLister,
			RolloutLister: rolloutLister,
			Queue:         podQueue,
		}
		podWorker.Start(ctx)
	}()

	wg.Add(1)
	go func() {
		// Start rollout worker
		defer wg.Done()
		rolloutWorker := &workers.RolloutWorker{
			Client:        client,
			PodLister:     podLister,
			RolloutLister: rolloutLister,
			PodQueue:      podQueue,
			Queue:         rolloutQueue,
		}
		rolloutWorker.Start(ctx)
	}()

	wg.Add(1)
	go func() {
		// Start rollout informer and wait for cache to sync
		defer wg.Done()
		rolloutInformerFactory.Start(ctx.Done())
		rolloutInformerFactory.WaitForCacheSync(ctx.Done())
	}()

	wg.Add(1)
	go func() {
		// start pod informer and wait for cache to sync
		defer wg.Done()
		sharedInformerFactory.Start(ctx.Done())
		sharedInformerFactory.WaitForCacheSync(ctx.Done())
	}()

	// Shutdown queues when context is cancelled when the process receives SIGINT
	go func() {
		<-ctx.Done()
		log.Println("Shutting down queues...")
		podQueue.ShutDownWithDrain()
		rolloutQueue.ShutDownWithDrain()
	}()

	wg.Wait()
	log.Println("argo-canary shutdown complete")
}
