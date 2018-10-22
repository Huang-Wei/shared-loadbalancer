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

/**
* USER ACTION REQUIRED: This is a scaffold file intended for the user to modify with their own Controller
* business logic.  Delete these comments after modifying this file.*
 */

// Add creates a new SharedLB Controller and adds it to the Manager with default RBAC. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
// USER ACTION REQUIRED: update cmd/manager/main.go to call this kubecon.Add(mgr) to install this Controller
func Add(mgr manager.Manager) error {
	return add(mgr, newReconciler(mgr))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) reconcile.Reconciler {
	var provider providers.LBProvider
	log.Info("building Reconciler", "provider", providers.Provider)
	switch providers.Provider {
	case "iks":
		provider = providers.NewIKSProvider()
	case "local":
		provider = providers.NewLocalProvider()
	}
	return &ReconcileSharedLB{
		Client:   mgr.GetClient(),
		scheme:   mgr.GetScheme(),
		provider: provider,
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
}

// Reconcile reads that state of the cluster for a SharedLB object and makes changes based on the state read
// and what is in the SharedLB.Spec
// TODO(user): Modify this Reconcile function to implement your Controller logic.
// The scaffolding writes a Service as an example
// Automatically generate RBAC rules to allow the Controller to read and write Services
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubecon.k8s.io,resources=sharedlbs,verbs=get;list;watch;create;update;patch;delete
func (r *ReconcileSharedLB) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	// log.Printf("[DEBUG] request: %v", request.NamespacedName)
	lbPlaceholder := &corev1.Service{}
	err := r.Get(context.TODO(), request.NamespacedName, lbPlaceholder)
	if err == nil {
		log.Info("Updating cache", "lbPlaceHolder", lbPlaceholder.Name)
		r.provider.UpdateCache(request.NamespacedName, lbPlaceholder)
		return reconcile.Result{}, nil
	}

	// Fetch the SharedLB CRD object
	crdObj := &kubeconv1alpha1.SharedLB{}
	err = r.Get(context.TODO(), request.NamespacedName, crdObj)
	if err != nil {
		if errors.IsNotFound(err) {
			// Object not found, return.  Created objects are automatically garbage collected.
			// For additional cleanup logic use finalizers.
			// TODO(Huang-Wei): use finalizers to do this
			// need to deassociate with the LoadBalancer service
			r.provider.DeassociateLB(request.NamespacedName)
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	} else if crdObj.Status.Ref != "" {
		strs := strings.Split(crdObj.Status.Ref, "/")
		r.provider.AssociateLB(request.NamespacedName, types.NamespacedName{Namespace: strs[0], Name: strs[1]})
	}

	// Define the desired Service object
	service := r.provider.NewService(crdObj)
	if err := controllerutil.SetControllerReference(crdObj, service, r.scheme); err != nil {
		return reconcile.Result{}, err
	}

	// Check if the Service already exists
	found := &corev1.Service{}
	err = r.Get(context.TODO(), types.NamespacedName{Name: service.Name, Namespace: service.Namespace}, found)
	if err != nil && errors.IsNotFound(err) {
		// fetch an available LoadBalancer Service
		availableLB := r.provider.GetAvailabelLB()
		if availableLB == nil {
			log.Info("Creating a real LoadBalancer Service")
			availableLB = r.provider.NewLBService()
			err = r.Create(context.TODO(), availableLB)
			if err != nil {
				log.Error(err, "Creating LoadBalancer Service Failed", "name", availableLB.Name)
				// backoff a bit
				return reconcile.Result{RequeueAfter: time.Second * 1}, err
			}
			r.provider.UpdateCache(types.NamespacedName{Name: availableLB.Name, Namespace: availableLB.Namespace}, availableLB)
		} else {
			log.Info("Reusing a LoadBalancer Service", "name", availableLB.Name)
		}

		lbNamespacedName := types.NamespacedName{Name: availableLB.Name, Namespace: availableLB.Namespace}
		if err = r.provider.AssociateLB(request.NamespacedName, lbNamespacedName); err != nil {
			// log.Printf("[ERROR] Associate Service with LoadBalancer Service Failed")
			// backoff a bit
			return reconcile.Result{RequeueAfter: time.Second * 1}, err
		}

		// log.Printf("Creating Service %s/%s\n", service.Namespace, service.Name)
		err = r.Create(context.TODO(), service)
		if err != nil {
			return reconcile.Result{}, err
		}

		// update CRD obj again
		crdObj.Status.Ref = lbNamespacedName.String()
		crdObj.Status.LoadBalancer = availableLB.Status.LoadBalancer
		err = r.Update(context.TODO(), crdObj)
		if err != nil {
			return reconcile.Result{}, err
		}
	} else if err != nil {
		return reconcile.Result{}, err
	}

	// We don't care about update of dependents
	// And even we care, don't use reflect.DeepEqual() on spec
	// TODO(user): Change this for the object type created by your controller
	// Update the found object and write the result back if there are any changes
	// if !reflect.DeepEqual(deploy.Spec, found.Spec) {
	// 	found.Spec = deploy.Spec
	// 	log.Printf("Updating Deployment %s/%s\n", deploy.Namespace, deploy.Name)
	// 	err = r.Update(context.TODO(), found)
	// 	if err != nil {
	// 		return reconcile.Result{}, err
	// 	}
	// }
	return reconcile.Result{}, nil
}
