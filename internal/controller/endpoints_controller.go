/*
Copyright 2023.

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

package controller

import (
	"context"
	"fmt"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"strings"
)

var (
	// Name is the name of the operator
	Name = "endpoint-copier-operator"
)

// EndpointsReconciler reconciles a Endpoints object
type EndpointsReconciler struct {
	client.Client
	Scheme                   *runtime.Scheme
	DefaultEndpointName      string
	DefaultEndpointNamespace string
	ManagedEndpointName      string
	ManagedEndpointNamespace string
	ApiserverPort            int
	ApiserverProtocol        string
}

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Endpoints object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.15.0/pkg/reconcile
func (r *EndpointsReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := ctrl.Log.WithName("endpoints")
	// Check if managed service is created
	managedService := &corev1.Service{}
	err := r.Get(ctx, client.ObjectKey{Namespace: r.ManagedEndpointNamespace, Name: r.ManagedEndpointName}, managedService)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Service is missing", "name", r.ManagedEndpointName, "namespace", r.ManagedEndpointNamespace)
		} else {
			logger.Error(err, "Could not get service", "name", r.ManagedEndpointName, "namespace", r.ManagedEndpointNamespace)
			return reconcile.Result{}, err
		}

		return reconcile.Result{}, nil
	}

	// Fetch default Endpoints object
	endpoints := &corev1.Endpoints{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: r.DefaultEndpointNamespace, Name: r.DefaultEndpointName}, endpoints); err != nil {
		return reconcile.Result{}, err
	}

	// update the endpoints
	if err := r.syncEndpoints(ctx, logger, endpoints, managedService); err != nil {
		logger.Error(err, "error syncing endpoint")
		return reconcile.Result{}, err
	}

	logger.Info("Successfully updated endpoint", "name", r.ManagedEndpointName, "namespace", r.ManagedEndpointNamespace)

	return reconcile.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *EndpointsReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Endpoints{}, builder.WithPredicates(predicate.Funcs{
			CreateFunc: func(e event.CreateEvent) bool {
				return e.Object.GetNamespace() == r.DefaultEndpointNamespace && e.Object.GetName() == r.DefaultEndpointName
			},
			UpdateFunc: func(e event.UpdateEvent) bool {
				return e.ObjectOld.GetNamespace() == r.DefaultEndpointNamespace && e.ObjectOld.GetName() == r.DefaultEndpointName
			},
			DeleteFunc: func(e event.DeleteEvent) bool {
				return e.Object.GetNamespace() == r.DefaultEndpointNamespace && e.Object.GetName() == r.DefaultEndpointName
			},
		})).
		Watches(&corev1.Service{}, &handler.EnqueueRequestForObject{}, builder.WithPredicates(predicate.Funcs{
			CreateFunc: func(e event.CreateEvent) bool {
				return e.Object.GetNamespace() == r.ManagedEndpointNamespace && e.Object.GetName() == r.ManagedEndpointName
			},
			UpdateFunc: func(e event.UpdateEvent) bool {
				return e.ObjectOld.GetNamespace() == r.ManagedEndpointNamespace && e.ObjectOld.GetName() == r.ManagedEndpointName
			},
			DeleteFunc: func(e event.DeleteEvent) bool {
				return e.Object.GetNamespace() == r.ManagedEndpointNamespace && e.Object.GetName() == r.ManagedEndpointName
			},
		})).Complete(r)
}

// syncEndpoint updates the Endpoint resource with the current node IPs.
func (r *EndpointsReconciler) syncEndpoints(ctx context.Context, logger logr.Logger, defaultEndpoints *corev1.Endpoints, managedService *corev1.Service) error {
	managedEndpoints := &corev1.Endpoints{}
	managedEndpoints.ObjectMeta.Name = r.ManagedEndpointName
	managedEndpoints.ObjectMeta.Namespace = r.ManagedEndpointNamespace
	managedEndpoints.ObjectMeta.Labels = map[string]string{"endpointslice.kubernetes.io/managed-by": Name}

	managedEndpoints.Subsets = []corev1.EndpointSubset{}
	for _, subset := range defaultEndpoints.Subsets {
		var copiedPorts []corev1.EndpointPort
		for _, port := range managedService.Spec.Ports {
			endpointPort := corev1.EndpointPort{
				Name:     port.Name,
				Port:     port.Port,
				Protocol: port.Protocol,
			}
			copiedPorts = append(copiedPorts, endpointPort)
		}

		// Copy the addresses
		copiedAddresses := make([]corev1.EndpointAddress, len(subset.Addresses))
		copy(copiedAddresses, subset.Addresses)

		newSubset := corev1.EndpointSubset{
			Addresses: copiedAddresses,
			Ports:     copiedPorts,
		}

		managedEndpoints.Subsets = append(managedEndpoints.Subsets, newSubset)
	}

	if err := r.Update(ctx, managedEndpoints); err != nil {
		if apierrors.IsNotFound(err) {
			if err := r.Create(ctx, managedEndpoints); err != nil {
				logger.Error(err, "error creating endpoints")
				return err
			}
		} else {
			return err
		}
	}

	if len(managedService.Spec.IPFamilies) <= 1 {
		return nil // Not dual-stack, we're done
	}

	secondaryIPFamily := managedService.Spec.IPFamilies[1]
	if err := r.handleSecondaryEndpoints(ctx, logger, managedService, secondaryIPFamily); err != nil {
		return err
	}

	return nil
}

// The primary IP family endpoints are automatically handled with the previous function
// but the secondary IP family endpoints must be configured
func (r *EndpointsReconciler) handleSecondaryEndpoints(ctx context.Context, logger logr.Logger,
	managedService *corev1.Service, ipFamily corev1.IPFamily) error {

	nodeList := &corev1.NodeList{}
	if err := r.List(ctx, nodeList, client.MatchingLabels{"node-role.kubernetes.io/control-plane": "true"}); err != nil {
		logger.Error(err, "error listing control plane nodes")
		return err
	}

	var addressType discoveryv1.AddressType
	var familyName string

	if ipFamily == corev1.IPv6Protocol {
		addressType = discoveryv1.AddressTypeIPv6
		familyName = "ipv6"
	} else {
		addressType = discoveryv1.AddressTypeIPv4
		familyName = "ipv4"
	}

	endpointSlice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s", r.ManagedEndpointName, familyName),
			Namespace: r.ManagedEndpointNamespace,
			Labels: map[string]string{
				"kubernetes.io/service-name":             r.ManagedEndpointName,
				"endpointslice.kubernetes.io/managed-by": Name,
			},
		},
		AddressType: addressType,
		Ports:       []discoveryv1.EndpointPort{},
		Endpoints:   []discoveryv1.Endpoint{},
	}

	for _, port := range managedService.Spec.Ports {
		portNum := int32(port.Port)
		portProto := port.Protocol
		portName := port.Name
		endpointSlice.Ports = append(endpointSlice.Ports, discoveryv1.EndpointPort{
			Name:     &portName,
			Port:     &portNum,
			Protocol: &portProto,
		})
	}

	for _, node := range nodeList.Items {
		for _, addr := range node.Status.Addresses {
			if addr.Type == corev1.NodeInternalIP {
				isIPv6 := strings.Contains(addr.Address, ":")
				if (ipFamily == corev1.IPv6Protocol && isIPv6) ||
					(ipFamily == corev1.IPv4Protocol && !isIPv6) {

					ready := true
					nodeName := node.Name
					endpointSlice.Endpoints = append(endpointSlice.Endpoints, discoveryv1.Endpoint{
						Addresses: []string{addr.Address},
						Conditions: discoveryv1.EndpointConditions{
							Ready: &ready,
						},
						NodeName: &nodeName,
					})
					break
				}
			}
		}
	}

	existingSlice := &discoveryv1.EndpointSlice{}
	err := r.Get(ctx, client.ObjectKey{
		Namespace: r.ManagedEndpointNamespace,
		Name:      endpointSlice.Name,
	}, existingSlice)

	if err != nil {
		if apierrors.IsNotFound(err) {
			if err := r.Create(ctx, endpointSlice); err != nil {
				logger.Error(err, "error creating EndpointSlice",
					"name", endpointSlice.Name,
					"addressType", addressType)
				return err
			}
			logger.Info("Created EndpointSlice",
				"name", endpointSlice.Name,
				"addressType", addressType)
		} else {
			logger.Error(err, "error getting EndpointSlice")
			return err
		}
	} else {
		existingSlice.Ports = endpointSlice.Ports
		existingSlice.Endpoints = endpointSlice.Endpoints

		if err := r.Update(ctx, existingSlice); err != nil {
			logger.Error(err, "error updating EndpointSlice")
			return err
		}
		logger.Info("Updated EndpointSlice",
			"name", existingSlice.Name,
			"addressType", addressType)
	}

	return nil
}
