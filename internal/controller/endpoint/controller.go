// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpoint

import (
	"context"
	"fmt"
	"strings"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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
// +kubebuilder:rbac:groups=openchoreo.dev,resources=endpointreleasebindings,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=openchoreo.dev,resources=components,verbs=get;list;watch

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

	// With no backends left and reclaimPolicy Delete, the Endpoint is deleted.
	// Its finalizer releases the claims, which hands the URL back.
	if len(ep.Spec.BackendRefs) == 0 && ep.Spec.ReclaimPolicy != openchoreov1alpha1.EndpointReclaimPolicyRetain {
		logger.Info("Endpoint has no backends left; deleting it (reclaimPolicy Delete)")
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

	if len(ep.Spec.BackendRefs) == 0 {
		return ctrl.Result{}, r.keepOrphaned(ctx, ep)
	}
	meta.RemoveStatusCondition(&ep.Status.Conditions, string(ConditionOrphaned))

	snapshot, err := r.resolveType(ctx, ep)
	if err != nil {
		if apierrors.IsNotFound(err) {
			msg := fmt.Sprintf("%s %q not found", resolvedKind(ep.Spec.Type.Kind), ep.Spec.Type.Name)
			controller.MarkFalseCondition(ep, ConditionReady, ReasonEndpointTypeNotFound, msg)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
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
		BackendRefs:  ep.Spec.BackendRefs,
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

// keepOrphaned handles an Endpoint with no backends left and reclaimPolicy
// Retain: it stays as Orphaned, its bindings are deleted so it routes nothing,
// and adding a backend brings it back.
func (r *Reconciler) keepOrphaned(ctx context.Context, ep *openchoreov1alpha1.Endpoint) error {
	controller.MarkTrueCondition(ep, ConditionOrphaned, ReasonNoBackends,
		"No backends left; kept without routes until a backend is added (reclaimPolicy Retain)")
	controller.MarkFalseCondition(ep, ConditionReady, ReasonNoBackends, "Endpoint has no backends")
	_, err := r.deleteOwnedEndpointReleaseBindings(ctx, ep)
	return err
}

// missingBackendComponents returns the distinct backend component names that do
// not exist as Components of the Endpoint's project. Backends at weight 0 are
// skipped: they receive no traffic, so a canary can be staged before its
// component exists. Whether the referenced workload endpoint exists is checked
// per environment by the binding controller.
func (r *Reconciler) missingBackendComponents(ctx context.Context, ep *openchoreov1alpha1.Endpoint) ([]string, error) {
	var missing []string
	seen := make(map[string]struct{}, len(ep.Spec.BackendRefs))
	for _, ref := range ep.Spec.BackendRefs {
		if controller.EndpointBackendWeight(ref) == 0 {
			continue
		}
		if _, ok := seen[ref.ComponentName]; ok {
			continue
		}
		seen[ref.ComponentName] = struct{}{}

		comp := &openchoreov1alpha1.Component{}
		err := r.Get(ctx, types.NamespacedName{Name: ref.ComponentName, Namespace: ep.Namespace}, comp)
		switch {
		case apierrors.IsNotFound(err):
			missing = append(missing, ref.ComponentName)
		case err != nil:
			return nil, fmt.Errorf("get Component %q: %w", ref.ComponentName, err)
		case comp.Spec.Owner.ProjectName != ep.Spec.Owner.ProjectName:
			missing = append(missing, ref.ComponentName)
		}
	}
	return missing, nil
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
			BackendRefs:  ep.Spec.BackendRefs,
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
		Named("endpoint").
		Complete(r)
}
