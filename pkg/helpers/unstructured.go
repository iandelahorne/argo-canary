// Package helpers contains various helper functions needed
package helpers

import (
	"log"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// ObjectToPod is inspired by https://github.com/argoproj/argo-rollouts/blob/master/utils/unstructured/unstructured.go
// This takes an interface{} and converts it either from a struct or a pointer into a Pod pointer
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
