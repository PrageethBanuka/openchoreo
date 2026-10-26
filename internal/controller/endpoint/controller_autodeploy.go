// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpoint

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/controller"
)

// autoDeploy binds the Endpoint's latest release to the first environment of
// its project's deployment pipeline, as Component.spec.autoDeploy does for
// components: it creates the EndpointReleaseBinding for that environment, or
// advances it to the latest release. Later environments are promoted
// explicitly. It returns false, after recording Ready=False, when the
// pipeline can't be resolved.
func (r *Reconciler) autoDeploy(ctx context.Context, ep *openchoreov1alpha1.Endpoint) (bool, error) {
	env, problem, err := r.firstEnvironment(ctx, ep)
	if err != nil {
		return false, err
	}
	if problem != "" {
		controller.MarkFalseCondition(ep, ConditionReady, ReasonAutoDeployFailed, "autoDeploy: "+problem)
		return false, nil
	}

	var bindings openchoreov1alpha1.EndpointReleaseBindingList
	if err := r.List(ctx, &bindings, client.InNamespace(ep.Namespace)); err != nil {
		return false, fmt.Errorf("list EndpointReleaseBindings: %w", err)
	}
	for i := range bindings.Items {
		b := &bindings.Items[i]
		if b.Spec.Owner.ProjectName != ep.Spec.Owner.ProjectName || b.Spec.Owner.EndpointName != ep.Name ||
			b.Spec.Environment != env {
			continue
		}
		if b.Spec.EndpointRelease == ep.Status.LatestRelease.Name {
			return true, nil
		}
		patch := client.MergeFrom(b.DeepCopy())
		b.Spec.EndpointRelease = ep.Status.LatestRelease.Name
		b.Spec.BackendOverrides = pruneOverrides(b.Spec.BackendOverrides, ep.Spec.BackendRefs)
		if err := r.Patch(ctx, b, patch); err != nil {
			return false, fmt.Errorf("advance EndpointReleaseBinding %q: %w", b.Name, err)
		}
		return true, nil
	}

	binding := &openchoreov1alpha1.EndpointReleaseBinding{
		ObjectMeta: metav1.ObjectMeta{Name: ep.Name + "-" + env, Namespace: ep.Namespace},
		Spec: openchoreov1alpha1.EndpointReleaseBindingSpec{
			Owner: openchoreov1alpha1.EndpointReleaseBindingOwner{
				ProjectName: ep.Spec.Owner.ProjectName, EndpointName: ep.Name,
			},
			Environment:     env,
			EndpointRelease: ep.Status.LatestRelease.Name,
		},
	}
	if err := controllerutil.SetControllerReference(ep, binding, r.Scheme); err != nil {
		return false, fmt.Errorf("set owner on EndpointReleaseBinding: %w", err)
	}
	if err := r.Create(ctx, binding); err != nil && !apierrors.IsAlreadyExists(err) {
		return false, fmt.Errorf("create EndpointReleaseBinding %q: %w", binding.Name, err)
	}
	return true, nil
}

// firstEnvironment returns the root environment of the Endpoint's project's
// deployment pipeline: the source environment that is never a target. A
// non-empty problem explains a missing or invalid pipeline.
func (r *Reconciler) firstEnvironment(ctx context.Context, ep *openchoreov1alpha1.Endpoint) (string, string, error) {
	project := &openchoreov1alpha1.Project{}
	if err := r.Get(ctx, types.NamespacedName{Name: ep.Spec.Owner.ProjectName, Namespace: ep.Namespace}, project); err != nil {
		if apierrors.IsNotFound(err) {
			return "", fmt.Sprintf("project %q not found", ep.Spec.Owner.ProjectName), nil
		}
		return "", "", fmt.Errorf("get Project: %w", err)
	}
	if project.Spec.DeploymentPipelineRef.Name == "" {
		return "", fmt.Sprintf("project %q has no deployment pipeline", project.Name), nil
	}
	pipeline := &openchoreov1alpha1.DeploymentPipeline{}
	if err := r.Get(ctx, types.NamespacedName{Name: project.Spec.DeploymentPipelineRef.Name, Namespace: ep.Namespace}, pipeline); err != nil {
		if apierrors.IsNotFound(err) {
			return "", fmt.Sprintf("deployment pipeline %q not found", project.Spec.DeploymentPipelineRef.Name), nil
		}
		return "", "", fmt.Errorf("get DeploymentPipeline: %w", err)
	}
	targets := map[string]bool{}
	for _, path := range pipeline.Spec.PromotionPaths {
		for _, t := range path.TargetEnvironmentRefs {
			targets[t.Name] = true
		}
	}
	for _, path := range pipeline.Spec.PromotionPaths {
		if name := path.SourceEnvironmentRef.Name; name != "" && !targets[name] {
			return name, "", nil
		}
	}
	return "", fmt.Sprintf("deployment pipeline %q has no root environment", pipeline.Name), nil
}

// pruneOverrides drops the binding's weight overrides for backends the new
// release no longer has, such as a retired blue or a finished canary. An
// override must name a backend of the pinned release, so keeping them would
// hold the binding back; the remaining ones keep the environment's weights.
func pruneOverrides(
	overrides []openchoreov1alpha1.EndpointBackendOverride,
	refs []openchoreov1alpha1.EndpointBackendRef,
) []openchoreov1alpha1.EndpointBackendOverride {
	if len(overrides) == 0 {
		return overrides
	}
	known := make(map[string]bool, len(refs))
	for _, ref := range refs {
		known[ref.Name] = true
	}
	kept := make([]openchoreov1alpha1.EndpointBackendOverride, 0, len(overrides))
	for _, o := range overrides {
		if known[o.Name] {
			kept = append(kept, o)
		}
	}
	return kept
}
