# argo-canary

argo-canary is a Kubernetes operator that manages long-lived canary deployments for Argo rollouts. 

# Long-lived canaries

Canary deployments can take several meanings in the context of deploying services. 

One interpretation of a canary is when deploying a new version of an application - the new version is deployed to take a small amount of traffic, canary analysis is performed to verify that the new version performs as expected, and upon success deployment progresses to roll out the new version fully. The canary phase is usually short - 5-10 minutes - to not prolong the deployment phase. The short lived canaries are used typically for validating the next version of the application. 

Another is the concept of a long-lived canary. This is meant instead to validate bigger changes such as different database driver, language version upgrades or different garbage collection strategies. These are deployed alongside an existing deployment and are managed outside the continuous delivery lifecycle. Typically an engineer will decide to deploy a canary, run it for a few hours or days and gather data, and then terminate it.

Doordash has a good blog post about this on their engineering blog - [Gradual Code Releases Using an In-House Kubernetes Canary Controller](https://careersatdoordash.com/blog/gradual-code-releases-using-an-in-house-kubernetes-canary-controller/)

## EC2 long lived canaries

For long-lived canaries in an EC2 environment, this is fairly easy - launch an instance with the canary branch and attach it to the load balancer alongside the normal deployment-managed autoscaling groups. If the main deployment has n instances, the canary instance will take 1/n+1 amount of the traffic.

## Normal kubernetes deployment

In the case of a normal kubernetes deployment, this is similar to the EC2 instance. If we have a `Service` for the application with label selectors for matching the pods and a `Deployment` with the correct labels for the services label selector, we can easily launch a `Pod` with the same labels. This will have the same effect as in EC2 - if the `Deployment` has n pods, the canary pod will take 1/n+1 amount of traffic.

## Argo Rollout blue/green deployments and canaries 

If we have the application deployed with Argo Rollouts using the `blueGreen` strategy, this presents a bit of an issue. Argo Rollouts manages both the underlying `ReplicaSets` for each version, and also the `Service` object exposing the application (`.spec.strategy.blueGreen.activeService`). Argo Rollouts specifically adds a `rollouts-pod-template-hash` label selector to the `Service`. As the Rollout progresses when deploying a new version, the new version's `ReplicaSet` has a `rollouts-pod-template-hash` label added to it, and the Services label selector value is updated to the latest production version.

To ensure that a long-lived canary Pod continues to take traffic as the Rollout progresses, we need to copy the `rollouts-pod-template-hash` to the Pod. That is what this controller does.

For example, say we have a Service:

```yaml
apiVersion: v1
kind: Service
metadata:
  labels:
    app: bluegreen-demo
  name: bluegreen-demo
  namespace: rollouts-demo
spec:
  ports:
    - name: http
      port: 80
      protocol: TCP
      targetPort: http
  selector:
    app: bluegreen-demo
```

When we deploy a new active version, the Rollout will have `.status.stableRS` field, for example, set to `fbc7b7f55` and will update service's `spec.selector.rollouts-pod-template-hash` to match this value

```yaml
apiVersion: v1
kind: Service
metadata:
  annotations:
    argo-rollouts.argoproj.io/managed-by-rollouts: bluegreen-demo
  labels:
    app: bluegreen-demo
  name: bluegreen-demo
  namespace: rollouts-demo
spec:
  ports:
    - name: http
      port: 80
      protocol: TCP
      targetPort: http
  selector:
    app: bluegreen-demo
    rollouts-pod-template-hash: fbc7b7f55
```

# Implementation

We have 2 Informers that watch for changes in the cluster - a Pod informer and a Rollouts informer.

The Pod informer watches for pods being created that have an `ian.delahorne.com/argo-canary` label. This label contains the name of the rollout they wish to attach to.
If there's a Rollout in the same namespace with a name matching that value, we query the rollout for it's current `.status.stableRS` value and queue up a patch operation to add the `rollouts-pod-template-hash` label to the pod with that value.  

The Rollouts informer watches for updates to Rollouts. When their `.status.stableRS` changes, it will loop over all Pods in the namespace with the `ian.delahorne.com/argo-canary` label matching the name of the rollout, and update the `rollouts-pod-template-hash` label on the pod spec to the new current stableRS value. 

# Building the operator

## Building container 
To build the operator as a container, run:

```bash
docker build -t argo-canary:latest .
```
## Building locally

```bash
go mod download
go build -o argo-canary cmd/main.go
```

# Running

To run the operator locally against the context targeted in your current ~/.kube/config, run:

```bash
KUBECONFIG=~/.kube/config go run cmd/main.go
```

# Example setup

Requirements:
- Minikube with ingress-nginx installed
- Kubectl locally installed
- Go toolchain

## Install argo rollouts
See instructions at https://argo-rollouts.readthedocs.io/en/stable/installation:

```bash
kubectl create namespace argo-rollouts
kubectl apply -n argo-rollouts -f https://github.com/argoproj/argo-rollouts/releases/latest/download/install.yaml
```

## Install argo rollouts kubectl plugin

See instructions at https://argo-rollouts.readthedocs.io/en/stable/installation/#kubectl-plugin-installation

## Create the blue-green demo rollout

```bash
$ kubectl create namespace rollouts-demo
$ kubens rollouts-demo
$ kubectl apply -f examples/blue-green.yaml
```

## Start the operator

```bash
KUBECONFIG=~/.kube/config go run cmd/main.go
```

## Create the long lived canary pod

```bash
$ kubectl apply -f examples/canary.yaml
```

## Verify that the long lived canary, service  and the rollout have the same pod template hash versions:

```bash
kubectl get rollouts.argoproj.io bluegreen-demo -o jsonpath='{.status.stableRS}'
kubectl get svc bluegreen-demo -o jsonpath='{.spec.selector.rollouts-pod-template-hash}'
kubectl get pod long-lived-canary -o jsonpath='{.metadata.labels.rollouts-pod-template-hash}'
```

## Progress the rollout to a new version

```bash
$ kubectl argo rollouts set image bluegreen-demo bluegreen-demo=argoproj/rollouts-demo:yellow
$ kubectl argo rollouts promote bluegreen-demo --full
```

## Verify that the long lived canary and the rollout have the same pod template hash versions after the promotion:

```bash
kubectl get rollouts.argoproj.io bluegreen-demo -o jsonpath='{.status.stableRS}'
kubectl get svc bluegreen-demo -o jsonpath='{.spec.selector.rollouts-pod-template-hash}'
kubectl get pod long-lived-canary -o jsonpath='{.metadata.labels.rollouts-pod-template-hash}'
```

# Next steps:
- Clean up main function
- Improve error handling and logging
- Unit tests
- End to end tests
- Healthcheck endpoint
- Prometheus metrics endpoint exporting success and error rates for updating Pods
- OpenTelemetry tracing for pod operations
- Leader election to be able to run with high availability, yet only have one active instance updating pods
- Create manifest for running argo-canary in cluster
