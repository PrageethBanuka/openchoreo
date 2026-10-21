// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointreleasebinding

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/controller"
	dpkubernetes "github.com/openchoreo/openchoreo/internal/dataplane/kubernetes"
	"github.com/openchoreo/openchoreo/internal/labels"
	endpointpipeline "github.com/openchoreo/openchoreo/internal/pipeline/endpoint"
	"github.com/openchoreo/openchoreo/internal/template"
)

const ownershipConflictMarker = "RenderedRelease exists but is not owned by this binding"

// Reconciler reconciles an EndpointReleaseBinding object.
type Reconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Pipeline renders EndpointType templates. The instance holds CEL env and
	// program caches; reuse it across reconciles to keep them warm.
	Pipeline *endpointpipeline.Pipeline

	// CELCostLimit bounds the accumulated cost of a single CEL expression.
	// Zero selects the template engine's built-in default.
	CELCostLimit uint64

	// RenderTimeout bounds each render call. Zero disables the deadline.
	RenderTimeout time.Duration
}

// reconcileInputs are the objects a binding renders against.
type reconcileInputs struct {
	release     *openchoreov1alpha1.EndpointRelease
	environment *openchoreov1alpha1.Environment
	dataPlane   *openchoreov1alpha1.DataPlane
	endpoint    *openchoreov1alpha1.Endpoint
	project     *openchoreov1alpha1.Project
}

// +kubebuilder:rbac:groups=openchoreo.dev,resources=endpointreleasebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=openchoreo.dev,resources=endpointreleasebindings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=openchoreo.dev,resources=endpointreleasebindings/finalizers,verbs=update
// +kubebuilder:rbac:groups=openchoreo.dev,resources=endpointreleases,verbs=get;list;watch
// +kubebuilder:rbac:groups=openchoreo.dev,resources=endpoints,verbs=get;list;watch
// +kubebuilder:rbac:groups=openchoreo.dev,resources=releasebindings,verbs=get;list;watch
// +kubebuilder:rbac:groups=openchoreo.dev,resources=projects,verbs=get;list;watch
// +kubebuilder:rbac:groups=openchoreo.dev,resources=environments,verbs=get;list;watch
// +kubebuilder:rbac:groups=openchoreo.dev,resources=dataplanes,verbs=get;list;watch
// +kubebuilder:rbac:groups=openchoreo.dev,resources=clusterdataplanes,verbs=get;list;watch
// +kubebuilder:rbac:groups=openchoreo.dev,resources=renderedreleases,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	binding := &openchoreov1alpha1.EndpointReleaseBinding{}
	if err := r.Get(ctx, req.NamespacedName, binding); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get EndpointReleaseBinding")
		return ctrl.Result{}, err
	}

	old := binding.DeepCopy()

	if !binding.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, old, binding)
	}

	if added, err := r.ensureFinalizer(ctx, binding); err != nil || added {
		return ctrl.Result{}, err
	}

	return r.reconcile(ctx, old, binding)
}

// reconcile is the live-object path: load inputs, resolve backends, render,
// emit the RenderedRelease, then report URLs and readiness.
func (r *Reconciler) reconcile(ctx context.Context, old, binding *openchoreov1alpha1.EndpointReleaseBinding) (result ctrl.Result, rErr error) {
	logger := log.FromContext(ctx)

	// Deferred status write: aggregate Ready so every exit path produces it,
	// skip the API call when nothing changed, aggregate errors.
	defer func() {
		setReadyCondition(binding)
		if apiequality.Semantic.DeepEqual(old.Status, binding.Status) {
			return
		}
		if err := r.Status().Update(ctx, binding); err != nil {
			logger.Error(err, "Failed to update EndpointReleaseBinding status")
			rErr = kerrors.NewAggregate([]error{rErr, err})
		}
	}()

	in, err := r.loadInputs(ctx, binding)
	if err != nil || in == nil {
		// in == nil: a Synced=False condition explains what is missing.
		return ctrl.Result{}, err
	}

	md := buildMetadataContext(binding, in)

	resolution, err := r.resolveBackends(ctx, binding, in.release, md)
	if err != nil {
		return ctrl.Result{}, err
	}
	binding.Status.Backends = resolution.statuses
	if len(resolution.unresolved) > 0 {
		// Leave the existing RenderedRelease (and the URLs it produced) alone:
		// re-rendering without the missing backend would silently shift its
		// share of traffic, and rendering with it would send that share to an
		// error. Watches on ReleaseBinding re-run this once the backend lands.
		controller.MarkFalseCondition(binding, ConditionBackendsResolved, ReasonBackendsUnresolved,
			fmt.Sprintf("Backend(s) not available in environment %q: %s",
				binding.Spec.Environment, strings.Join(resolution.unresolved, ", ")))
		return ctrl.Result{}, nil
	}
	if !slices.ContainsFunc(resolution.backends, func(b endpointpipeline.Backend) bool { return b.Weight > 0 }) {
		controller.MarkFalseCondition(binding, ConditionBackendsResolved, ReasonBackendsUnresolved,
			"No backend with a non-zero weight is available; check the weights in spec.backendOverrides")
		return ctrl.Result{}, nil
	}

	controller.MarkTrueCondition(binding, ConditionBackendsResolved, ReasonBackendsResolved,
		fmt.Sprintf("%d backend(s) resolved", len(resolution.backends)))

	// A Service-addressed type serves the taken-over component's in-cluster
	// Service, not an address of its own, so there is no gateway address to
	// resolve or to claim.
	servesService := in.release.Spec.EndpointType.Spec.ServesService()
	var address endpointpipeline.AddressContext
	if !servesService {
		var problem string
		address, problem = resolveAddress(binding, in)
		if problem != "" {
			markSyncedFalse(binding, ReasonAddressInvalid, "The Endpoint's address can't be served: "+problem)
			return ctrl.Result{}, nil
		}
	}

	// A take-over the component can't serve here yet is not an error: the
	// Endpoint still serves its own address, and the take-over follows once
	// the component is deployed (ReleaseBinding watches re-run this).
	takeOver, pending, err := r.resolveTakeOver(ctx, binding, in.release, md.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	switch {
	case in.release.Spec.TakeOver == nil:
		meta.RemoveStatusCondition(&binding.Status.Conditions, string(ConditionTakeOver))
	case pending != "" && servesService:
		// The taken-over Service is the only address a Service type serves, so
		// there is nothing to route until it exists here.
		controller.MarkFalseCondition(binding, ConditionTakeOver, ReasonTakeOverPending,
			"Nothing to route yet; the taken-over Service follows once it is available: "+pending)
		markSyncedFalse(binding, ReasonTakeOverPending,
			"Waiting for the taken-over component's Service in environment "+binding.Spec.Environment)
		return ctrl.Result{}, nil
	case pending != "":
		controller.MarkFalseCondition(binding, ConditionTakeOver, ReasonTakeOverPending,
			"Serving the Endpoint's own address only; the taken-over URL follows once it is available: "+pending)
	default:
		controller.MarkTrueCondition(binding, ConditionTakeOver, ReasonTakeOverServed,
			fmt.Sprintf("Also serving the URL of %s/%s", takeOver.ComponentName, takeOver.Endpoint))
	}

	var addrs []openchoreov1alpha1.EndpointResolvedAddress
	if !servesService {
		addrs = servedAddresses(address, takeOver)
		conflict, err := r.addressConflict(ctx, binding, in.release, addrs)
		if err != nil {
			return ctrl.Result{}, err
		}
		if conflict != "" {
			markSyncedFalse(binding, ReasonAddressInUse, conflict+"; this binding routes nothing until the address is free")
			return ctrl.Result{}, nil
		}
	}
	binding.Status.Addresses = addrs

	ctx = template.WithReconcileBudget(ctx, r.CELCostLimit)

	output, rr, err := r.renderAndEmit(ctx, binding, in, md, resolution.backends, address, takeOver)
	if err != nil || rr == nil {
		return ctrl.Result{}, err
	}

	resolveURLs(ctx, binding, in, output, resolution.backends)
	evaluateRoutesReady(binding, rr)
	updateServingClaim(binding, in.release, output, takeOver != nil)
	return ctrl.Result{}, nil
}

// loadInputs fetches everything the binding renders against. It returns nil
// inputs (and no error) after recording Synced=False when an input is missing
// or inconsistent; errors are reserved for transient API failures.
func (r *Reconciler) loadInputs(ctx context.Context, binding *openchoreov1alpha1.EndpointReleaseBinding) (*reconcileInputs, error) {
	if binding.Spec.EndpointRelease == "" {
		markSyncedFalse(binding, ReasonEndpointReleaseNotSet,
			"spec.endpointRelease is unset; pin an EndpointRelease to deploy this binding")
		return nil, nil
	}

	in := &reconcileInputs{}
	ns := binding.Namespace

	in.release = &openchoreov1alpha1.EndpointRelease{}
	if found, err := r.getOrMark(ctx, binding, types.NamespacedName{Name: binding.Spec.EndpointRelease, Namespace: ns},
		in.release, ReasonEndpointReleaseNotFound, "EndpointRelease"); !found || err != nil {
		return nil, err
	}
	if err := validateReleaseOwner(in.release, binding); err != nil {
		markSyncedFalse(binding, ReasonInvalidReleaseConfiguration, err.Error())
		return nil, nil
	}
	if err := validateBackendOverrides(in.release, binding); err != nil {
		markSyncedFalse(binding, ReasonBackendOverrideInvalid, err.Error())
		return nil, nil
	}

	in.environment = &openchoreov1alpha1.Environment{}
	if found, err := r.getOrMark(ctx, binding, types.NamespacedName{Name: binding.Spec.Environment, Namespace: ns},
		in.environment, ReasonEnvironmentNotFound, "Environment"); !found || err != nil {
		return nil, err
	}

	dataPlaneResult, err := controller.GetDataPlaneFromRef(ctx, r.Client, in.environment.Namespace, in.environment.Spec.DataPlaneRef)
	if err != nil {
		if apierrors.IsNotFound(err) {
			markSyncedFalse(binding, ReasonDataPlaneNotFound,
				fmt.Sprintf("DataPlane not found for environment %q", in.environment.Name))
			return nil, nil
		}
		return nil, fmt.Errorf("resolve DataPlane for environment %q: %w", in.environment.Name, err)
	}
	in.dataPlane = dataPlaneResult.ToDataPlane()

	in.endpoint = &openchoreov1alpha1.Endpoint{}
	if found, err := r.getOrMark(ctx, binding, types.NamespacedName{Name: binding.Spec.Owner.EndpointName, Namespace: ns},
		in.endpoint, ReasonEndpointNotFound, "Endpoint"); !found || err != nil {
		return nil, err
	}

	in.project = &openchoreov1alpha1.Project{}
	if found, err := r.getOrMark(ctx, binding, types.NamespacedName{Name: binding.Spec.Owner.ProjectName, Namespace: ns},
		in.project, ReasonProjectNotFound, "Project"); !found || err != nil {
		return nil, err
	}

	return in, nil
}

// getOrMark gets obj by key. On NotFound it records Synced=False with reason and
// returns found=false with no error.
func (r *Reconciler) getOrMark(
	ctx context.Context,
	binding *openchoreov1alpha1.EndpointReleaseBinding,
	key types.NamespacedName,
	obj client.Object,
	reason controller.ConditionReason,
	kind string,
) (bool, error) {
	if err := r.Get(ctx, key, obj); err != nil {
		if apierrors.IsNotFound(err) {
			markSyncedFalse(binding, reason, fmt.Sprintf("%s %q not found", kind, key.Name))
			return false, nil
		}
		return false, fmt.Errorf("get %s %q: %w", kind, key.Name, err)
	}
	return true, nil
}

// renderAndEmit drives the pipeline against the snapshot and writes the
// resulting RenderedRelease. Returns the render output and the live
// RenderedRelease on success, or a nil RenderedRelease when no further
// evaluation should run (render error, ownership conflict).
func (r *Reconciler) renderAndEmit(
	ctx context.Context,
	binding *openchoreov1alpha1.EndpointReleaseBinding,
	in *reconcileInputs,
	md endpointpipeline.MetadataContext,
	backends []endpointpipeline.Backend,
	address endpointpipeline.AddressContext,
	takeOver *endpointpipeline.URLContext,
) (*endpointpipeline.RenderOutput, *openchoreov1alpha1.RenderedRelease, error) {
	logger := log.FromContext(ctx)

	output, err := r.Pipeline.Render(ctx, &endpointpipeline.RenderInput{
		EndpointTypeSpec:   &in.release.Spec.EndpointType.Spec,
		Parameters:         in.release.Spec.Parameters,
		EnvironmentConfigs: binding.Spec.EndpointTypeEnvironmentConfigs,
		Backends:           backends,
		Address:            address,
		TakeOver:           takeOver,
		Metadata:           md,
		DataPlane:          endpointpipeline.BuildDataPlaneContext(in.dataPlane),
		Environment:        endpointpipeline.BuildEnvironmentContext(in.environment, in.dataPlane),
	})
	if err != nil {
		markSyncedFalse(binding, ReasonRenderingFailed, fmt.Sprintf("Failed to render routing resources: %v", err))
		logger.Info("Pipeline render failed", "error", err)
		return nil, nil, nil
	}

	manifests, err := convertEntriesToManifests(output.Entries)
	if err != nil {
		markSyncedFalse(binding, ReasonRenderingFailed, fmt.Sprintf("Failed to encode rendered manifests: %v", err))
		return nil, nil, err
	}

	rrName := makeRenderedReleaseName(binding)
	rr := &openchoreov1alpha1.RenderedRelease{
		ObjectMeta: metav1.ObjectMeta{Name: rrName, Namespace: binding.Namespace},
	}

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, rr, func() error {
		if rr.UID != "" {
			hasOwner, ownerErr := controllerutil.HasOwnerReference(rr.GetOwnerReferences(), binding, r.Scheme)
			if ownerErr != nil {
				return fmt.Errorf("check owner reference: %w", ownerErr)
			}
			if !hasOwner {
				return fmt.Errorf("%s: %q", ownershipConflictMarker, rrName)
			}
		}

		rr.Labels = map[string]string{
			labels.LabelKeyNamespaceName:        binding.Namespace,
			labels.LabelKeyProjectName:          binding.Spec.Owner.ProjectName,
			labels.LabelKeyEndpointResourceName: binding.Spec.Owner.EndpointName,
			labels.LabelKeyEnvironmentName:      binding.Spec.Environment,
		}
		rr.Spec = openchoreov1alpha1.RenderedReleaseSpec{
			Owner: openchoreov1alpha1.RenderedReleaseOwner{
				ProjectName:  binding.Spec.Owner.ProjectName,
				EndpointName: binding.Spec.Owner.EndpointName,
			},
			EnvironmentName: binding.Spec.Environment,
			TargetPlane:     openchoreov1alpha1.TargetPlaneDataPlane,
			Resources:       manifests,
		}
		return controllerutil.SetControllerReference(binding, rr, r.Scheme)
	})
	if err != nil {
		if strings.Contains(err.Error(), ownershipConflictMarker) {
			markSyncedFalse(binding, ReasonReleaseOwnershipConflict, err.Error())
			return nil, nil, nil
		}
		markSyncedFalse(binding, ReasonReleaseUpdateFailed,
			fmt.Sprintf("Failed to reconcile RenderedRelease %q: %v", rrName, err))
		return nil, nil, err
	}

	switch op {
	case controllerutil.OperationResultCreated, controllerutil.OperationResultUpdated:
		controller.MarkTrueCondition(binding, ConditionSynced, ReasonReleaseCreated,
			fmt.Sprintf("RenderedRelease %q %s with %d resource(s)", rrName, op, len(manifests)))
	case controllerutil.OperationResultNone:
		controller.MarkTrueCondition(binding, ConditionSynced, ReasonReleaseSynced,
			fmt.Sprintf("RenderedRelease %q is up to date", rrName))
	}

	return output, rr, nil
}

// makeRenderedReleaseName returns the RenderedRelease name for a binding. The
// "e_" discriminator keeps it collision-safe against component ("") and
// resource ("r_") RenderedReleases: "_" is sanitized to "-" in the visible name
// but drives the hash, and K8s names cannot contain "_".
func makeRenderedReleaseName(binding *openchoreov1alpha1.EndpointReleaseBinding) string {
	return dpkubernetes.GenerateK8sName("e_"+binding.Spec.Owner.EndpointName, binding.Spec.Environment)
}

// convertEntriesToManifests marshals each pipeline-rendered map into a
// RenderedManifest with its ID preserved.
func convertEntriesToManifests(entries []endpointpipeline.RenderedEntry) ([]openchoreov1alpha1.RenderedManifest, error) {
	manifests := make([]openchoreov1alpha1.RenderedManifest, 0, len(entries))
	for i := range entries {
		raw, err := json.Marshal(entries[i].Object)
		if err != nil {
			return nil, fmt.Errorf("marshal rendered manifest %q: %w", entries[i].ID, err)
		}
		manifests = append(manifests, openchoreov1alpha1.RenderedManifest{
			ID:     entries[i].ID,
			Object: &runtime.RawExtension{Raw: raw},
		})
	}
	return manifests, nil
}

// buildMetadataContext computes the platform-injected metadata surface exposed
// to CEL templates. The data-plane namespace matches the one component
// ReleaseBindings render into for the same (namespace, project, environment),
// so routes reference backend Services in their own namespace.
func buildMetadataContext(binding *openchoreov1alpha1.EndpointReleaseBinding, in *reconcileInputs) endpointpipeline.MetadataContext {
	endpointName := binding.Spec.Owner.EndpointName
	projectName := binding.Spec.Owner.ProjectName
	envName := binding.Spec.Environment

	dpNamespace := dpkubernetes.GenerateK8sNameWithLengthLimit(
		dpkubernetes.MaxNamespaceNameLength,
		"dp", binding.Namespace, projectName, envName,
	)

	return endpointpipeline.MetadataContext{
		Name:              dpkubernetes.GenerateK8sName("e_"+endpointName, envName),
		Namespace:         dpNamespace,
		EndpointNamespace: binding.Namespace,
		EndpointName:      endpointName,
		EndpointUID:       string(in.endpoint.UID),
		ProjectName:       projectName,
		ProjectUID:        string(in.project.UID),
		EnvironmentName:   envName,
		EnvironmentUID:    string(in.environment.UID),
		DataPlaneName:     in.dataPlane.Name,
		DataPlaneUID:      string(in.dataPlane.UID),
		Labels: map[string]string{
			labels.LabelKeyNamespaceName:        binding.Namespace,
			labels.LabelKeyProjectName:          projectName,
			labels.LabelKeyProjectUID:           string(in.project.UID),
			labels.LabelKeyEndpointResourceName: endpointName,
			labels.LabelKeyEndpointResourceUID:  string(in.endpoint.UID),
			labels.LabelKeyEnvironmentName:      envName,
			labels.LabelKeyEnvironmentUID:       string(in.environment.UID),
		},
		Annotations: binding.Annotations,
	}
}

// validateReleaseOwner enforces that the binding and the pinned EndpointRelease
// belong to the same Endpoint.
func validateReleaseOwner(release *openchoreov1alpha1.EndpointRelease, binding *openchoreov1alpha1.EndpointReleaseBinding) error {
	if release.Spec.Owner.ProjectName != binding.Spec.Owner.ProjectName ||
		release.Spec.Owner.EndpointName != binding.Spec.Owner.EndpointName {
		return fmt.Errorf("binding owner (project: %q, endpoint: %q) does not match EndpointRelease owner (project: %q, endpoint: %q)",
			binding.Spec.Owner.ProjectName, binding.Spec.Owner.EndpointName,
			release.Spec.Owner.ProjectName, release.Spec.Owner.EndpointName)
	}
	return nil
}

// validateBackendOverrides checks that every backendOverrides entry names a
// backend of the pinned release, so a typo can't silently leave a weight or URL
// unchanged. A binding can be applied before its release (GitOps), so this is
// checked here rather than at admission.
func validateBackendOverrides(release *openchoreov1alpha1.EndpointRelease, binding *openchoreov1alpha1.EndpointReleaseBinding) error {
	known := make(map[string]struct{}, len(release.Spec.BackendRefs))
	for _, ref := range release.Spec.BackendRefs {
		known[ref.Name] = struct{}{}
	}
	var unknown []string
	for _, o := range binding.Spec.BackendOverrides {
		if _, ok := known[o.Name]; !ok {
			unknown = append(unknown, o.Name)
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("spec.backendOverrides names backend(s) not in EndpointRelease %q: %s",
			release.Name, strings.Join(unknown, ", "))
	}
	return nil
}

// markSyncedFalse records Synced=False with the given reason and message.
func markSyncedFalse(binding *openchoreov1alpha1.EndpointReleaseBinding, reason controller.ConditionReason, msg string) {
	controller.MarkFalseCondition(binding, ConditionSynced, reason, msg)
}

// SetupWithManager sets up the controller with the Manager. The watch surface:
//   - For: EndpointReleaseBinding (primary).
//   - Owns: RenderedRelease — data-plane status updates flow back into readiness.
//   - Watches: EndpointRelease — wakes a binding authored before its pinned
//     release existed, and surfaces a deleted pin.
//   - Watches: ReleaseBinding — when a backend component's endpoints change in
//     an environment, re-resolve the Endpoints that route to it there.
//
// (Cluster)EndpointType is intentionally not watched: rendering reads the
// immutable EndpointRelease snapshot.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Pipeline == nil {
		r.Pipeline = endpointpipeline.NewPipeline(
			endpointpipeline.WithCostLimit(r.CELCostLimit),
			endpointpipeline.WithRenderTimeout(r.RenderTimeout),
		)
	}

	if err := r.setupEndpointReleaseRefIndex(context.Background(), mgr); err != nil {
		return fmt.Errorf("setup EndpointRelease ref index: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&openchoreov1alpha1.EndpointReleaseBinding{}).
		Owns(&openchoreov1alpha1.RenderedRelease{}).
		Watches(&openchoreov1alpha1.EndpointRelease{},
			handler.EnqueueRequestsFromMapFunc(r.listBindingsForEndpointRelease)).
		Watches(&openchoreov1alpha1.ReleaseBinding{},
			handler.EnqueueRequestsFromMapFunc(r.listBindingsForBackendReleaseBinding)).
		Named("endpointreleasebinding").
		Complete(r)
}
