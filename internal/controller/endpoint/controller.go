// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpoint

import (
	"context"
	"fmt"
	"strings"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/controller"
)

// Reconciler reconciles an Endpoint object
type Reconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=openchoreo.dev,resources=endpoints,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=openchoreo.dev,resources=endpoints/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=openchoreo.dev,resources=endpoints/finalizers,verbs=update
// +kubebuilder:rbac:groups=openchoreo.dev,resources=endpointtypes,verbs=get;list;watch
// +kubebuilder:rbac:groups=openchoreo.dev,resources=clusterendpointtypes,verbs=get;list;watch
// +kubebuilder:rbac:groups=openchoreo.dev,resources=endpointreleases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=openchoreo.dev,resources=endpointreleasebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=openchoreo.dev,resources=components,verbs=get;list;watch
// +kubebuilder:rbac:groups=openchoreo.dev,resources=projects,verbs=get;list;watch
// +kubebuilder:rbac:groups=openchoreo.dev,resources=deploymentpipelines,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	ep := &openchoreov1alpha1.Endpoint{}
	if err := r.Get(ctx, req.NamespacedName, ep); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Endpoint not found. Ignoring since it must be deleted.")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get Endpoint")
		return ctrl.Result{}, err
	}

	old := ep.DeepCopy()

	if !ep.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, old, ep)
	}

	if added, err := r.ensureFinalizer(ctx, ep); err != nil || added {
		return ctrl.Result{}, err
	}

	// An Endpoint with no backends left (its last backend component was
	// deleted) is deleted too. Its finalizer releases the claims, which hands
	// the route slots back.
	if len(ep.Spec.BackendRefs) == 0 {
		logger.Info("Endpoint has no backends left; deleting it")
		if err := r.Delete(ctx, ep); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("delete Endpoint with no backends: %w", err)
		}
		return ctrl.Result{}, nil
	}

	return r.reconcile(ctx, old, ep)
}

func (r *Reconciler) reconcile(ctx context.Context, old, ep *openchoreov1alpha1.Endpoint) (result ctrl.Result, rErr error) {
	logger := log.FromContext(ctx)

	// Deferred status write: skip when nothing changed, aggregate errors with
	// any returned by the body. Mirrors resource/controller.go.
	defer func() {
		ep.Status.ObservedGeneration = ep.Generation
		if apiequality.Semantic.DeepEqual(old.Status, ep.Status) {
			return
		}
		if err := r.Status().Update(ctx, ep); err != nil {
			logger.Error(err, "Failed to update Endpoint status")
			rErr = kerrors.NewAggregate([]error{rErr, err})
		}
	}()

	// One route per (component, endpoint, visibility) slot. The webhook refuses
	// a second claim; this holds the line when it is bypassed or two creates
	// race, so a newer Endpoint never silently shadows the route of the one
	// already serving.
	reason, msg, err := r.conflict(ctx, ep)
	if err != nil {
		return ctrl.Result{}, err
	}
	if reason != "" {
		controller.MarkFalseCondition(ep, ConditionReady, reason, msg)
		// Without a latest release no binding can be created for it; one that
		// served before, while it held the URL, stops routing.
		if ep.Status.LatestRelease == nil {
			return ctrl.Result{}, nil
		}
		if _, err := r.deleteOwnedEndpointReleaseBindings(ctx, ep); err != nil {
			return ctrl.Result{}, err
		}
		ep.Status.LatestRelease = nil
		return ctrl.Result{}, nil
	}

	snapshot, err := r.resolveType(ctx, ep)
	if err != nil {
		if apierrors.IsNotFound(err) {
			msg := fmt.Sprintf("%s %q not found", resolvedKind(ep.Spec.Type.Kind), ep.Spec.Type.Name)
			controller.MarkFalseCondition(ep, ConditionReady, ReasonEndpointTypeNotFound, msg)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// A catch-all rule carries its own weights for unmatched requests.
	if n := len(ep.Spec.Rules); (n == 0 || len(ep.Spec.Rules[n-1].Matches) > 0) && !hasWeightedBackend(ep.Spec.BackendRefs) {
		controller.MarkFalseCondition(ep, ConditionReady, ReasonNoWeightedBackend,
			"Every backend has weight 0, so the Endpoint would serve nothing; give at least one backend a weight above 0")
		return ctrl.Result{}, nil
	}

	missing, err := r.missingBackendComponents(ctx, ep)
	if err != nil {
		return ctrl.Result{}, err
	}
	if len(missing) > 0 {
		msg := fmt.Sprintf("Component(s) %s not found in project %q",
			strings.Join(missing, ", "), ep.Spec.Owner.ProjectName)
		controller.MarkFalseCondition(ep, ConditionReady, ReasonBackendNotFound, msg)
		return ctrl.Result{}, nil
	}

	releaseHash := computeReleaseHash(ReleaseSpec{
		EndpointType: snapshot,
		Visibility:   ep.Spec.Visibility,
		BackendRefs:  ep.Spec.BackendRefs,
		Rules:        ep.Spec.Rules,
		Schema:       ep.Spec.Schema,
		Parameters:   ep.Spec.Parameters,
	})

	if ep.Status.LatestRelease == nil || ep.Status.LatestRelease.Hash != releaseHash {
		erName := fmt.Sprintf("%s-%s", ep.Name, releaseHash)
		if err := r.ensureEndpointRelease(ctx, ep, snapshot, erName); err != nil {
			return ctrl.Result{}, err
		}
		ep.Status.LatestRelease = &openchoreov1alpha1.LatestEndpointRelease{
			Name: erName,
			Hash: releaseHash,
		}
	}

	if ep.Spec.AutoDeploy {
		if done, err := r.autoDeploy(ctx, ep); err != nil || !done {
			// !done: a Ready=False condition explains what is missing.
			return ctrl.Result{}, err
		}
	}

	controller.MarkTrueCondition(ep, ConditionReady, ReasonReconciled,
		fmt.Sprintf("EndpointRelease %s in place", ep.Status.LatestRelease.Name))

	return ctrl.Result{}, nil
}

// resolveType fetches the (Cluster)EndpointType referenced by ep.Spec.Type and
// returns the snapshot to embed in an EndpointRelease. Returns an
// apierrors.IsNotFound error when the referenced template is missing.
func (r *Reconciler) resolveType(ctx context.Context, ep *openchoreov1alpha1.Endpoint) (
	openchoreov1alpha1.EndpointReleaseEndpointType, error,
) {
	kind := resolvedKind(ep.Spec.Type.Kind)
	name := ep.Spec.Type.Name

	switch kind {
	case openchoreov1alpha1.EndpointTypeRefKindClusterEndpointType:
		cet := &openchoreov1alpha1.ClusterEndpointType{}
		if err := r.Get(ctx, types.NamespacedName{Name: name}, cet); err != nil {
			return openchoreov1alpha1.EndpointReleaseEndpointType{}, err
		}
		// ClusterEndpointTypeSpec is structurally identical to EndpointTypeSpec
		// today; if it ever diverges, this cast breaks at compile time and
		// EndpointReleaseEndpointType.Spec needs a kind discriminator.
		return openchoreov1alpha1.EndpointReleaseEndpointType{
			Kind: kind,
			Name: name,
			Spec: openchoreov1alpha1.EndpointTypeSpec(cet.Spec),
		}, nil
	default:
		et := &openchoreov1alpha1.EndpointType{}
		if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: ep.Namespace}, et); err != nil {
			return openchoreov1alpha1.EndpointReleaseEndpointType{}, err
		}
		return openchoreov1alpha1.EndpointReleaseEndpointType{
			Kind: kind,
			Name: name,
			Spec: et.Spec,
		}, nil
	}
}

// missingBackendComponents returns the distinct component backends that do
// not exist as Components of the Endpoint's project. Backends at weight 0 are skipped: they
// receive no traffic, so a canary can be staged before its component exists.
// External backends are not checked; OpenChoreo does not run them. Whether the
// referenced workload endpoints exist is checked per environment by the
// binding controller.
func (r *Reconciler) missingBackendComponents(ctx context.Context, ep *openchoreov1alpha1.Endpoint) ([]string, error) {
	var names []string
	for _, ref := range ep.Spec.BackendRefs {
		if name, _, ok := controller.BackendComponent(ref); ok && controller.EndpointBackendWeight(ref) > 0 {
			names = append(names, name)
		}
	}

	var missing []string
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}

		comp := &openchoreov1alpha1.Component{}
		err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: ep.Namespace}, comp)
		switch {
		case apierrors.IsNotFound(err):
			missing = append(missing, name)
		case err != nil:
			return nil, fmt.Errorf("get Component %q: %w", name, err)
		case comp.Spec.Owner.ProjectName != ep.Spec.Owner.ProjectName:
			missing = append(missing, name)
		}
	}
	return missing, nil
}

// hasWeightedBackend reports whether any backend has a weight above 0.
func hasWeightedBackend(refs []openchoreov1alpha1.EndpointBackendRef) bool {
	for _, ref := range refs {
		if controller.EndpointBackendWeight(ref) > 0 {
			return true
		}
	}
	return false
}

// conflict reports why ep must not route, if another Endpoint already claims
// one of its route slots, the same workload endpoint of a component in the
// same visibility: the oldest Endpoint that is not being deleted keeps it,
// ties broken by name. It lists and filters in memory, so it works with cached
// and uncached clients alike.
func (r *Reconciler) conflict(ctx context.Context, ep *openchoreov1alpha1.Endpoint) (controller.ConditionReason, string, error) {
	var endpoints openchoreov1alpha1.EndpointList
	if err := r.List(ctx, &endpoints, client.InNamespace(ep.Namespace)); err != nil {
		return "", "", fmt.Errorf("list Endpoints: %w", err)
	}
	var holder *openchoreov1alpha1.Endpoint
	var slot controller.EndpointSlot
	for i := range endpoints.Items {
		other := &endpoints.Items[i]
		if other.Name == ep.Name || !other.DeletionTimestamp.IsZero() || !olderEndpoint(other, ep) {
			continue
		}
		if s, ok := controller.SharedEndpointSlot(ep, other); ok && (holder == nil || olderEndpoint(other, holder)) {
			holder, slot = other, s
		}
	}
	if holder == nil {
		return "", "", nil
	}
	return ReasonSlotInUse, fmt.Sprintf(
		"Endpoint %q already routes %s/%s with %s visibility; this Endpoint routes nothing until it is freed. Add the backend to %q instead, or use another visibility",
		holder.Name, slot.Component, slot.Endpoint, slot.Visibility, holder.Name), nil
}

// olderEndpoint reports whether a was created before b, ties broken by name.
func olderEndpoint(a, b *openchoreov1alpha1.Endpoint) bool {
	if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
		return a.CreationTimestamp.Before(&b.CreationTimestamp)
	}
	return a.Name < b.Name
}

// ensureEndpointRelease creates an EndpointRelease with the given name if it
// doesn't already exist. On AlreadyExists, verify the existing release belongs
// to this Endpoint before claiming it; refuse to take over a release owned by
// a different Endpoint (defends against hash collisions and external creates
// landing on the same name).
func (r *Reconciler) ensureEndpointRelease(
	ctx context.Context,
	ep *openchoreov1alpha1.Endpoint,
	snapshot openchoreov1alpha1.EndpointReleaseEndpointType,
	name string,
) error {
	er := &openchoreov1alpha1.EndpointRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ep.Namespace,
		},
		Spec: openchoreov1alpha1.EndpointReleaseSpec{
			Owner: openchoreov1alpha1.EndpointReleaseOwner{
				ProjectName:  ep.Spec.Owner.ProjectName,
				EndpointName: ep.Name,
			},
			EndpointType: snapshot,
			Visibility:   ep.Spec.Visibility,
			BackendRefs:  ep.Spec.BackendRefs,
			Rules:        ep.Spec.Rules,
			Schema:       ep.Spec.Schema,
			Parameters:   ep.Spec.Parameters,
		},
	}
	err := r.Create(ctx, er)
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create EndpointRelease %q: %w", name, err)
	}

	existing := &openchoreov1alpha1.EndpointRelease{}
	if getErr := r.Get(ctx, types.NamespacedName{Name: name, Namespace: ep.Namespace}, existing); getErr != nil {
		return fmt.Errorf("verify existing EndpointRelease %q: %w", name, getErr)
	}
	if existing.Spec.Owner.EndpointName != ep.Name {
		return fmt.Errorf("EndpointRelease %q already exists with owner %q, refusing to claim it for %q",
			name, existing.Spec.Owner.EndpointName, ep.Name)
	}
	return nil
}

// resolvedKind returns the Kind to use for type resolution, defaulting an empty
// Kind to EndpointType (namespaced) per the Endpoint CRD's stated default.
func resolvedKind(k openchoreov1alpha1.EndpointTypeRefKind) openchoreov1alpha1.EndpointTypeRefKind {
	if k == "" {
		return openchoreov1alpha1.EndpointTypeRefKindEndpointType
	}
	return k
}

// SetupWithManager sets up the controller with the Manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := r.setupEndpointTypeRefIndex(context.Background(), mgr); err != nil {
		return fmt.Errorf("setup endpoint type reference index: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&openchoreov1alpha1.Endpoint{}).
		Watches(&openchoreov1alpha1.EndpointType{},
			handler.EnqueueRequestsFromMapFunc(r.listEndpointsForEndpointType)).
		Watches(&openchoreov1alpha1.ClusterEndpointType{},
			handler.EnqueueRequestsFromMapFunc(r.listEndpointsForClusterEndpointType)).
		Watches(&openchoreov1alpha1.Component{},
			handler.EnqueueRequestsFromMapFunc(r.listEndpointsForComponent)).
		Watches(&openchoreov1alpha1.Endpoint{},
			handler.EnqueueRequestsFromMapFunc(r.listEndpointsSharingSlot)).
		Named("endpoint").
		Complete(r)
}
