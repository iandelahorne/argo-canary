package helpers

import (
	"log"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// Inspired by https://github.com/argoproj/argo-rollouts/blob/master/utils/unstructured/unstructured.go
func ObjectToPod(obj any) *v1.Pod {
	un, ok := obj.(*unstructured.Unstructured)
	if ok {
		var pod v1.Pod
		err := runtime.DefaultUnstructuredConverter.FromUnstructured(un.Object, &pod)
		if err != nil {
			log.Fatalln(err)
			return nil
		}
		return &pod
	}
	pod, ok := obj.(*v1.Pod)
	if !ok {
		log.Printf("Object is neither a rollout or unstructured: %v", obj)
		return nil
	}
	return pod
}
