// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package component

import (
	"context"
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/controller"
	ocLabels "github.com/openchoreo/openchoreo/internal/labels"
)

// reconcileExposedEndpoints creates the Endpoint for each Workload endpoint that
// names an EndpointType, if it is missing. An existing Endpoint is never
// changed: once created it has its own lifecycle and is edited by people.
// Problems are reported on the EndpointsExposed condition and never block the
// rest of the Component's reconcile.
func (r *Reconciler) reconcileExposedEndpoints(ctx context.Context, comp *openchoreov1alpha1.Component,
	workload *openchoreov1alpha1.Workload) error {
	keys := make([]string, 0, len(workload.Spec.Endpoints))
	for key, ep := range workload.Spec.Endpoints {
		if ep.EndpointType != nil {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		meta.RemoveStatusCondition(&comp.Status.Conditions, string(ConditionEndpointsExposed))
		return nil
	}
	sort.Strings(keys)

	var problems []string
	reason := ReasonEndpointsExposed
	for _, key := range keys {
		wep := workload.Spec.Endpoints[key]
		name := controller.ExposedEndpointName(comp.Name, key, wep)

		found, err := r.endpointTypeExists(ctx, comp.Namespace, *wep.EndpointType)
		if err != nil {
			return err
		}
		if !found {
			reason = ReasonEndpointTypeNotFound
			problems = append(problems, fmt.Sprintf("%s: %s %q not found", key, endpointTypeKind(*wep.EndpointType), wep.EndpointType.Name))
			continue
		}

		existing := &openchoreov1alpha1.Endpoint{}
		err = r.Get(ctx, types.NamespacedName{Namespace: comp.Namespace, Name: name}, existing)
		switch {
		case apierrors.IsNotFound(err):
			if err := r.createExposedEndpoint(ctx, comp, key, name, wep); err != nil {
				return err
			}
		case err != nil:
			return fmt.Errorf("get endpoint %s: %w", name, err)
		case existing.Spec.Owner.ProjectName != comp.Spec.Owner.ProjectName ||
			!controller.ServesEndpointURL(existing, comp.Name, key):
			reason = ReasonExposeConflict
			problems = append(problems, fmt.Sprintf("%s: Endpoint %q exists and does not serve this endpoint's URL", key, name))
		case !controller.EndpointTypeRefEqual(existing.Spec.Type, *wep.EndpointType):
			reason = ReasonEndpointTypeMismatch
			problems = append(problems, fmt.Sprintf("%s: Endpoint %q has type %s %q; an Endpoint's type cannot change",
				key, name, endpointTypeKind(existing.Spec.Type), existing.Spec.Type.Name))
		}
	}

	if len(problems) > 0 {
		controller.MarkFalseCondition(comp, ConditionEndpointsExposed, reason, strings.Join(problems, "; "))
		return nil
	}
	controller.MarkTrueCondition(comp, ConditionEndpointsExposed, ReasonEndpointsExposed,
		fmt.Sprintf("%d workload endpoint(s) served through Endpoints", len(keys)))
	return nil
}

func (r *Reconciler) createExposedEndpoint(ctx context.Context, comp *openchoreov1alpha1.Component,
	key, name string, wep openchoreov1alpha1.WorkloadEndpoint) error {
	ep := &openchoreov1alpha1.Endpoint{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: comp.Namespace,
			Labels: map[string]string{
				ocLabels.LabelKeyProjectName: comp.Spec.Owner.ProjectName,
			},
			Annotations: map[string]string{
				ocLabels.AnnotationKeyEndpointCreatedFrom: fmt.Sprintf("workload/%s/%s", comp.Name, key),
			},
		},
		Spec: openchoreov1alpha1.EndpointSpec{
			Owner: openchoreov1alpha1.EndpointOwner{ProjectName: comp.Spec.Owner.ProjectName},
			Type:  *wep.EndpointType,
			// The address is the URL the component already serves for this
			// endpoint, and the take-over moves that URL to the Endpoint, so
			// clients keep calling the same URL.
			Address: &openchoreov1alpha1.EndpointAddress{Path: "/" + comp.Name + "-" + key},
			TakeOver: &openchoreov1alpha1.EndpointTakeOver{
				Component: openchoreov1alpha1.EndpointComponentRef{Name: comp.Name, Endpoint: key},
			},
			BackendRefs: []openchoreov1alpha1.EndpointBackendRef{{
				Name:      comp.Name,
				Component: &openchoreov1alpha1.EndpointComponentRef{Name: comp.Name, Endpoint: key},
				Weight:    ptr.To[int32](100),
			}},
			ReclaimPolicy: openchoreov1alpha1.EndpointReclaimPolicyDelete,
		},
	}
	// No ownerReference: the Endpoint is not owned by the component.
	if err := r.Create(ctx, ep); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create endpoint %s: %w", name, err)
	}
	log.FromContext(ctx).Info("Created Endpoint for workload endpoint", "endpoint", name, "workloadEndpoint", key)
	return nil
}

func (r *Reconciler) endpointTypeExists(ctx context.Context, namespace string, ref openchoreov1alpha1.EndpointTypeRef) (bool, error) {
	var err error
	if ref.Kind == openchoreov1alpha1.EndpointTypeRefKindClusterEndpointType {
		err = r.Get(ctx, types.NamespacedName{Name: ref.Name}, &openchoreov1alpha1.ClusterEndpointType{})
	} else {
		err = r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ref.Name}, &openchoreov1alpha1.EndpointType{})
	}
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get %s %s: %w", endpointTypeKind(ref), ref.Name, err)
	}
	return true, nil
}

func endpointTypeKind(ref openchoreov1alpha1.EndpointTypeRef) openchoreov1alpha1.EndpointTypeRefKind {
	if ref.Kind == "" {
		return openchoreov1alpha1.EndpointTypeRefKindEndpointType
	}
	return ref.Kind
}

// updateEndpointIndex refreshes status.endpoints, the reverse index of the
// Endpoints that route to this component.
func (r *Reconciler) updateEndpointIndex(ctx context.Context, comp *openchoreov1alpha1.Component) error {
	usages, err := controller.ComponentEndpointUsage(ctx, r.Client, comp.Namespace, comp.Spec.Owner.ProjectName, comp.Name)
	if err != nil {
		return err
	}
	comp.Status.Endpoints = controller.ComponentEndpointRefs(usages)
	return nil
}

// stillRouted returns a message naming where the component still receives
// traffic through an Endpoint that has other backends to move it to, or has
// its URL served to other backends, or "" when deleting it breaks no route
// that could be kept.
func (r *Reconciler) stillRouted(ctx context.Context, comp *openchoreov1alpha1.Component) (string, error) {
	usages, err := controller.ComponentEndpointUsage(ctx, r.Client, comp.Namespace, comp.Spec.Owner.ProjectName, comp.Name)
	if err != nil {
		return "", err
	}
	routed := controller.RoutedEndpointUsage(usages)
	if len(routed) == 0 {
		return "", nil
	}
	return "Still " + controller.StillRoutedReason(routed), nil
}

// pruneFromEndpoints removes the component from the backends of every Endpoint
// in its project, and drops a take-over of one of its URLs. It runs during
// deletion, once stillRouted has confirmed no Endpoint still needs the
// component, so the Endpoints keep serving their own addresses through their
// other backends. An Endpoint left with no backends is handled by its
// reclaimPolicy in the Endpoint controller.
func (r *Reconciler) pruneFromEndpoints(ctx context.Context, comp *openchoreov1alpha1.Component) error {
	var endpoints openchoreov1alpha1.EndpointList
	if err := r.List(ctx, &endpoints, client.InNamespace(comp.Namespace)); err != nil {
		return fmt.Errorf("list endpoints: %w", err)
	}
	for i := range endpoints.Items {
		ep := &endpoints.Items[i]
		if ep.Spec.Owner.ProjectName != comp.Spec.Owner.ProjectName {
			continue
		}
		kept := make([]openchoreov1alpha1.EndpointBackendRef, 0, len(ep.Spec.BackendRefs))
		for _, ref := range ep.Spec.BackendRefs {
			if name, _, ok := controller.BackendComponent(ref); !ok || name != comp.Name {
				kept = append(kept, ref)
			}
		}
		takenOver := controller.TakenOver(ep)
		dropTakeOver := takenOver != nil && takenOver.Name == comp.Name
		if len(kept) == len(ep.Spec.BackendRefs) && !dropTakeOver {
			continue
		}
		patch := client.MergeFrom(ep.DeepCopy())
		ep.Spec.BackendRefs = kept
		if dropTakeOver {
			ep.Spec.TakeOver = nil
		}
		if err := r.Patch(ctx, ep, patch); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("remove %s from endpoint %s: %w", comp.Name, ep.Name, err)
		}
		log.FromContext(ctx).Info("Removed deleted component from Endpoint", "endpoint", ep.Name,
			"droppedTakeOver", dropTakeOver)
	}
	return nil
}

// listComponentsForEndpoint maps an Endpoint to the Components it references:
// its component backends and the component whose URL it takes over.
func (r *Reconciler) listComponentsForEndpoint(_ context.Context, obj client.Object) []ctrl.Request {
	ep := obj.(*openchoreov1alpha1.Endpoint)
	return componentRequests(ep)
}

// listComponentsForEndpointReleaseBinding maps an EndpointReleaseBinding to the
// Components its Endpoint references, whose reverse index carries its weights.
func (r *Reconciler) listComponentsForEndpointReleaseBinding(ctx context.Context, obj client.Object) []ctrl.Request {
	binding := obj.(*openchoreov1alpha1.EndpointReleaseBinding)
	ep := &openchoreov1alpha1.Endpoint{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: binding.Namespace, Name: binding.Spec.Owner.EndpointName}, ep); err != nil {
		return nil
	}
	return componentRequests(ep)
}

func componentRequests(ep *openchoreov1alpha1.Endpoint) []ctrl.Request {
	names := controller.EndpointComponentNames(ep)
	reqs := make([]ctrl.Request, 0, len(names))
	for _, name := range names {
		reqs = append(reqs, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ep.Namespace, Name: name}})
	}
	return reqs
}
