/*
Copyright 2018 The Shared LoadBalancer Authors.

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

package sharedlb

import (
	"context"
	"strings"
	"time"

	kubeconv1alpha1 "github.com/Huang-Wei/shared-loadbalancer/pkg/apis/kubecon/v1alpha1"
	"github.com/Huang-Wei/shared-loadbalancer/pkg/providers"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	logf "sigs.k8s.io/controller-runtime/pkg/runtime/log"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var log logr.Logger

func init() {
	log = logf.Log.WithName("slb_controller")
}

// Add creates a new SharedLB Controller and adds it to the Manager with default RBAC. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	return add(mgr, newReconciler(mgr))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) reconcile.Reconciler {
	return &ReconcileSharedLB{
		Client:   mgr.GetClient(),
		scheme:   mgr.GetScheme(),
		provider: providers.NewProvider(),
		pendingQ: &pendingQ{
			pendingLB:  nil,
			pendingCRs: make(map[types.NamespacedName]struct{}),
		},
	}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("sharedlb-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch LB Service
	mapFn := handler.ToRequestsFunc(
		func(o handler.MapObject) []reconcile.Request {
			_, ok := o.Meta.GetLabels()["lb-template"]
			if !ok {
				return nil
			}
			return []reconcile.Request{
				{NamespacedName: types.NamespacedName{
					Name:      o.Meta.GetName(),
					Namespace: o.Meta.GetNamespace(),
				}},
			}
		})
	err = c.Watch(
		&source.Kind{Type: &corev1.Service{}},
		&handler.EnqueueRequestsFromMapFunc{
			ToRequests: mapFn,
		},
	)
	if err != nil {
		return err
	}

	// Watch for changes to SharedLB
	err = c.Watch(
		&source.Kind{Type: &kubeconv1alpha1.SharedLB{}},
		&handler.EnqueueRequestForObject{},
	)
	if err != nil {
		return err
	}

	// Watch a Service created by SharedLB
	err = c.Watch(
		&source.Kind{Type: &corev1.Service{}},
		&handler.EnqueueRequestForOwner{
			IsController: true,
			OwnerType:    &kubeconv1alpha1.SharedLB{},
		},
	)
	if err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcileSharedLB{}

// ReconcileSharedLB reconciles a SharedLB object
type ReconcileSharedLB struct {
	client.Client
	scheme   *runtime.Scheme
	provider providers.LBProvider
	pendingQ *pendingQ
}

type pendingQ struct {
	pendingLB  *types.NamespacedName
	pendingCRs map[types.NamespacedName]struct{}
}

// Reconcile reads that state of the cluster for a SharedLB object and makes changes based on the state read
// and what is in the SharedLB.Spec
// The scaffolding writes a Service as an example
// Automatically generate RBAC rules to allow the Controller to read and write Services
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubecon.k8s.io,resources=sharedlbs,verbs=get;list;watch;create;update;patch;delete
func (r *ReconcileSharedLB) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	// 1) fetch and deal with the LoadBalancer Service object
	lbSvc := &corev1.Service{}
	err := r.Get(context.TODO(), request.NamespacedName, lbSvc)
	if err == nil {
		log.Info("LB is created/updated. Updating LB cache.", "name", request.Name)
		r.provider.UpdateCache(request.NamespacedName, lbSvc)
		if r.pendingQ.hasLB(request.NamespacedName) && len(lbSvc.Status.LoadBalancer.Ingress) > 0 {
			log.Info("LB has completed setup. Removing lb entry in pendingQ.")
			r.pendingQ.remove(request.NamespacedName)
		}
		return reconcile.Result{}, nil
	} else if errors.IsNotFound(err) && strings.Index(request.Name, "lb-") == 0 {
		// TODO(Huang-Wei): improve logic here to check if it's a LB svc deletion
		log.Info("LB is deleted. Updating lb cache.", "name", request.Name)
		r.provider.UpdateCache(request.NamespacedName, nil)
		return reconcile.Result{}, nil
	}

	// in some cases, esp. when CR obj is created but LoadBalancer service is pending
	// we rely on an internal struct "pendingQ" to tell whether incoming CR obj should
	// a) be put back to queue or b) be processed instantly
	if r.pendingQ.hasCR(request.NamespacedName) {
		log.Info("It's likely a dependent IaaS LoadBalancer is pending. Requeue to wait for its completion and retry.", "request", request.NamespacedName)
		// TODO(Huang-Wei): implement exponential backoff
		return reconcile.Result{Requeue: true, RequeueAfter: time.Second * 2}, nil
	}

	// 2) fetch and deal with the SharedLB CR object
	crObj := &kubeconv1alpha1.SharedLB{}
	err = r.Get(context.TODO(), request.NamespacedName, crObj)
	if err != nil {
		if errors.IsNotFound(err) {
			// if cr obj is not found, means it's been deleted. we can simply return b/c:
			// (1) dependent objects will be automatically garbage collected, and
			// (2) additional cleanup logic are handled by finalizers.
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	// add or remove finalizer
	if crObj.ObjectMeta.DeletionTimestamp.IsZero() {
		// The CR object is not being deleted, so if it does not have desired finalizer,
		// add the finalizer and update the object.
		if !containsString(crObj.ObjectMeta.Finalizers, providers.FinalizerName) {
			crObj.ObjectMeta.Finalizers = append(crObj.ObjectMeta.Finalizers, providers.FinalizerName)
			err := r.Update(context.Background(), crObj)
			// note: the code here is slightly different with kubebuilder sample code
			// where requeue everytime, but here we only requeue when err != nil
			return reconcile.Result{Requeue: err != nil}, nil
		}
	} else {
		if containsString(crObj.ObjectMeta.Finalizers, providers.FinalizerName) {
			// clusterSvc := r.provider.NewService(crObj)
			clusterSvc := &corev1.Service{}
			clusterSvcNsName := types.NamespacedName{Name: crObj.Name + providers.SvcPostfix, Namespace: crObj.Namespace}
			if err = r.Get(context.TODO(), clusterSvcNsName, clusterSvc); err != nil {
				log.Error(err, "fail to get clusterSvc when trying DeassociateLB")
				return reconcile.Result{}, err
			}
			if err := r.provider.DeassociateLB(request.NamespacedName, clusterSvc); err != nil {
				// fail to delete external dependencies here, return with error
				// so that it can be retried
				log.Error(err, "fail to delete external dependencies when trying DeassociateLB")
				return reconcile.Result{}, err
			}
			// remove our finalizer from the list and update it.
			crObj.ObjectMeta.Finalizers = removeString(crObj.ObjectMeta.Finalizers, providers.FinalizerName)
			err = r.Update(context.Background(), crObj)
			// note: the code here is slightly different with kubebuilder sample code
			// where requeue everytime, but here we only requeue when err != nil
			return reconcile.Result{Requeue: err != nil}, nil
		}
	}

	// 3) deal with the Cluster Service object
	// Define the desired cluster Service object
	clusterSvc := r.provider.NewService(crObj)
	if err := controllerutil.SetControllerReference(crObj, clusterSvc, r.scheme); err != nil {
		return reconcile.Result{}, err
	}

	// Check if the cluster Service already exists
	found := &corev1.Service{}
	err = r.Get(context.TODO(), types.NamespacedName{Name: clusterSvc.Name, Namespace: clusterSvc.Namespace}, found)

	if err == nil && crObj.Status.Ref != "" {
		strs := strings.Split(crObj.Status.Ref, "/")
		if err := r.provider.AssociateLB(request.NamespacedName, types.NamespacedName{Namespace: strs[0], Name: strs[1]}, clusterSvc); err != nil {
			// this err means corresponding IaaS Obj not exist yet
			// so we requeue with a bit backoff
			// this is possible in 2 cases:
			// i)  program start phase: CR obj Add event comes before LB obj Add event
			// ii) LB/IaaS obj created too slow
			log.Info(err.Error())
			return reconcile.Result{Requeue: true, RequeueAfter: time.Millisecond * 100}, nil
		}
	}

	if err != nil && errors.IsNotFound(err) {
		// fetch an available LoadBalancer Service that can be reused
		availableLB := r.provider.GetAvailabelLB(clusterSvc)
		if availableLB == nil {
			if !r.pendingQ.isEmpty() {
				r.pendingQ.add(request.NamespacedName, types.NamespacedName{})
				return reconcile.Result{Requeue: true, RequeueAfter: time.Second * 2}, nil
			}
			log.Info("Creating a real LoadBalancer Service")
			availableLB = r.provider.NewLBService()
			err = r.Create(context.TODO(), availableLB)
			if err != nil {
				log.Error(err, "Creating LoadBalancer Service Failed", "name", availableLB.Name)
				// backoff a bit
				return reconcile.Result{RequeueAfter: time.Second * 1}, err
			}
			log.Info("A real LB is created", "name", availableLB.Name, "lbinfo", availableLB.Status.LoadBalancer)
			// NOTE: here we directly return to start a new reconcile
			r.pendingQ.add(request.NamespacedName, types.NamespacedName{Name: availableLB.Name, Namespace: availableLB.Namespace})
			return reconcile.Result{Requeue: true, RequeueAfter: time.Millisecond * 500}, nil
		}

		log.Info("Reusing a LoadBalancer Service", "name", availableLB.Name)
		lbNamespacedName := types.NamespacedName{Name: availableLB.Name, Namespace: availableLB.Namespace}
		// at this point, we can reuse a LoadBalancer
		// i.e. availableLB is expected to carry loadbalancer info
		// check if this cr carries a port; if not, assign a random port
		portUpdated, _ := r.provider.UpdateService(clusterSvc, availableLB)
		err = r.Create(context.TODO(), clusterSvc)
		if err != nil {
			return reconcile.Result{}, err
		}

		// for EKS/GKE, need to get the NodePort from clusterSvc
		// then it's able to proceed to add listener and handle firewall rules, etc.
		if err = r.provider.AssociateLB(request.NamespacedName, lbNamespacedName, clusterSvc); err != nil {
			// backoff a bit
			return reconcile.Result{RequeueAfter: time.Second * 1}, err
		}

		// it's reusing a LB, so availableLB is expected to carry loadbalancer info
		crObj.Status.Ref = lbNamespacedName.String()
		crObj.Status.LoadBalancer = availableLB.Status.LoadBalancer
		if portUpdated {
			// seems don't need a DeepCopy
			crObj.Spec.Ports = clusterSvc.Spec.Ports
		}
		err = r.Update(context.TODO(), crObj)
		if err != nil {
			return reconcile.Result{}, err
		}
	} else if err != nil {
		return reconcile.Result{}, err
	}

	// We don't care about update of cluster Service, so do nothing here
	return reconcile.Result{}, nil
}

func (pq *pendingQ) add(crName, lbName types.NamespacedName) {
	if pq.pendingLB == nil {
		pq.pendingLB = &lbName
	}
	pq.pendingCRs[crName] = struct{}{}
}

func (pq *pendingQ) remove(lbName types.NamespacedName) {
	pq.pendingLB = nil
	pq.pendingCRs = make(map[types.NamespacedName]struct{})
}

func (pq *pendingQ) hasCR(crName types.NamespacedName) bool {
	_, ok := pq.pendingCRs[crName]
	return ok
}

func (pq *pendingQ) hasLB(lbName types.NamespacedName) bool {
	return pq.pendingLB != nil && *pq.pendingLB == lbName
}

func (pq *pendingQ) isEmpty() bool {
	return pq.pendingLB == nil
}

func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

func removeString(slice []string, s string) (result []string) {
	for _, item := range slice {
		if item == s {
			continue
		}
		result = append(result, item)
	}
	return
}
