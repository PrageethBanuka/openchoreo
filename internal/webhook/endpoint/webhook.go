// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpoint

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	openchoreodevv1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/controller"
	"github.com/openchoreo/openchoreo/internal/schema"
)

// SetupEndpointWebhookWithManager registers the validating webhook for Endpoint in the manager.
func SetupEndpointWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &openchoreodevv1alpha1.Endpoint{}).
		WithCustomValidator(&Validator{Client: mgr.GetClient()}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-openchoreo-dev-v1alpha1-endpoint,mutating=false,failurePolicy=fail,sideEffects=None,groups=openchoreo.dev,resources=endpoints,verbs=create;update,versions=v1alpha1,name=vendpoint-v1alpha1.kb.io,admissionReviewVersions=v1

// Validator keeps one route per slot: it refuses an Endpoint that routes to a
// component workload endpoint in a visibility another Endpoint of the project
// already routes it in. It also holds the EndpointType's limits (allowed
// hostnames and backend kinds, the parameters schema) and refuses a split with
// no weighted backend. Each refusal says what to do instead. The Endpoint
// controller enforces the slot rule when this webhook is bypassed.
type Validator struct {
	Client client.Reader
}

var _ webhook.CustomValidator = &Validator{}

// ValidateCreate implements webhook.CustomValidator.
func (v *Validator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	ep, ok := obj.(*openchoreodevv1alpha1.Endpoint)
	if !ok {
		return nil, fmt.Errorf("expected an Endpoint object but got %T", obj)
	}
	return nil, v.validate(ctx, ep, true, true)
}

// ValidateUpdate implements webhook.CustomValidator. Claims are checked only
// when they change, so an Endpoint the controller holds back can still be
// updated and deleted.
func (v *Validator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	ep, ok := newObj.(*openchoreodevv1alpha1.Endpoint)
	if !ok {
		return nil, fmt.Errorf("expected an Endpoint object but got %T", newObj)
	}
	oldEp, ok := oldObj.(*openchoreodevv1alpha1.Endpoint)
	if !ok {
		return nil, fmt.Errorf("expected an Endpoint object but got %T", oldObj)
	}
	if !ep.DeletionTimestamp.IsZero() {
		return nil, nil
	}
	backendsChanged := !equality.Semantic.DeepEqual(ep.Spec.BackendRefs, oldEp.Spec.BackendRefs) ||
		!equality.Semantic.DeepEqual(ep.Spec.Parameters, oldEp.Spec.Parameters)
	claimsChanged := !equality.Semantic.DeepEqual(ep.Spec.BackendRefs, oldEp.Spec.BackendRefs) ||
		!equality.Semantic.DeepEqual(ep.Spec.Visibility, oldEp.Spec.Visibility) ||
		ep.Spec.Owner != oldEp.Spec.Owner
	if !claimsChanged && !backendsChanged {
		return nil, nil
	}
	return nil, v.validate(ctx, ep, claimsChanged, backendsChanged)
}

// ValidateDelete implements webhook.CustomValidator.
func (v *Validator) ValidateDelete(context.Context, runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (v *Validator) validate(ctx context.Context, ep *openchoreodevv1alpha1.Endpoint, claims, backends bool) error {
	var errs field.ErrorList
	spec := field.NewPath("spec")

	if backends && len(ep.Spec.BackendRefs) > 0 && !hasWeightedBackend(ep.Spec.BackendRefs) {
		errs = append(errs, field.Invalid(spec.Child("backendRefs"), "",
			"every backend has weight 0, so the Endpoint would serve nothing; give at least one backend a weight above 0"))
	}

	if backends {
		errs = append(errs, validateMatches(ep, spec)...)
	}

	typeErrs, err := v.validateTypeLimits(ctx, ep, spec)
	if err != nil {
		return err
	}
	errs = append(errs, typeErrs...)

	if claims {
		claimErrs, err := v.validateClaims(ctx, ep, spec)
		if err != nil {
			return err
		}
		errs = append(errs, claimErrs...)
	}

	if len(errs) == 0 {
		return nil
	}
	sort.Slice(errs, func(i, j int) bool { return errs[i].Field < errs[j].Field })
	return apierrors.NewInvalid(openchoreodevv1alpha1.GroupVersion.WithKind("Endpoint").GroupKind(), ep.Name, errs)
}

// validateMatches rejects backend matches the data planes cannot express the
// same way. Gateway API keeps only the first condition on a header, Istio keys
// conditions by header name and Envoy ANDs them, so a header (names are
// case-insensitive) may appear once per match, and a cookie, which renders on
// the Cookie header, cannot sit next to a Cookie header condition. Header
// patterns must compile as RE2, the engine Envoy, Istio and Cilium all use;
// Go's regexp implements it. A path must mean the same on every proxy once
// joined to the Endpoint's own path, so it has no empty, . or .. segments.
// Two backends with the same match are refused: which of them gets the
// request would depend on the data plane.
func validateMatches(ep *openchoreodevv1alpha1.Endpoint, spec *field.Path) field.ErrorList {
	var errs field.ErrorList
	owner := map[string]string{}
	for i, ref := range ep.Spec.BackendRefs {
		m := ref.Match
		if m == nil {
			continue
		}
		path := spec.Child("backendRefs").Index(i).Child("match")
		if m.Path != nil {
			if msg := pathProblem(m.Path.Value); msg != "" {
				errs = append(errs, field.Invalid(path.Child("path", "value"), m.Path.Value, msg))
			}
		}
		seen := map[string]bool{}
		for k, h := range m.Headers {
			name := strings.ToLower(h.Name)
			if seen[name] {
				errs = append(errs, field.Duplicate(path.Child("headers").Index(k).Child("name"), h.Name))
			}
			seen[name] = true
			if name == "cookie" && len(m.Cookies) > 0 {
				errs = append(errs, field.Invalid(path.Child("headers").Index(k).Child("name"), h.Name,
					"match the cookie with cookies, or the Cookie header with headers, not both"))
			}
			if h.Type != openchoreodevv1alpha1.EndpointHeaderMatchRegularExpression {
				continue
			}
			if _, err := regexp.Compile(h.Value); err != nil {
				errs = append(errs, field.Invalid(path.Child("headers").Index(k).Child("value"), h.Value,
					fmt.Sprintf("not a regular expression the data plane can run (RE2): %v", err)))
			}
		}
		key := matchKey(m)
		if first, dup := owner[key]; dup {
			errs = append(errs, field.Invalid(path, "",
				fmt.Sprintf("backend %q has the same match; give each backend a different match", first)))
			continue
		}
		owner[key] = ref.Name
	}
	return errs
}

// pathProblem explains why a match path is not accepted, or returns "".
func pathProblem(p string) string {
	for _, seg := range strings.Split(p, "/")[1:] {
		switch seg {
		case ".", "..":
			return "must not contain . or .. segments"
		}
	}
	if strings.Contains(p, "//") {
		return "must not contain //"
	}
	return ""
}

// matchKey identifies a match by its conditions, whatever their order and
// the case of header names.
func matchKey(m *openchoreodevv1alpha1.EndpointMatch) string {
	var parts []string
	if m.Path != nil {
		t := m.Path.Type
		if t == "" {
			t = openchoreodevv1alpha1.EndpointPathMatchPathPrefix
		}
		parts = append(parts, "path\x00"+string(t)+"\x00"+m.Path.Value)
	}
	for _, h := range m.Headers {
		t := h.Type
		if t == "" {
			t = openchoreodevv1alpha1.EndpointHeaderMatchExact
		}
		parts = append(parts, "header\x00"+strings.ToLower(h.Name)+"\x00"+string(t)+"\x00"+h.Value)
	}
	for _, c := range m.Cookies {
		parts = append(parts, "cookie\x00"+c.Name+"\x00"+c.Value)
	}
	sort.Strings(parts)
	return strings.Join(parts, "\n")
}

// validateTypeLimits checks the hostname, backend kinds and parameters against
// the EndpointType. A missing type is left to the controller, which reports it.
func (v *Validator) validateTypeLimits(ctx context.Context, ep *openchoreodevv1alpha1.Endpoint, spec *field.Path) (field.ErrorList, error) {
	typeSpec, err := v.typeSpec(ctx, ep)
	if err != nil || typeSpec == nil {
		return nil, err
	}
	hostnames, kinds := typeSpec.AllowedHostnames, typeSpec.AllowedBackendKinds
	var errs field.ErrorList
	errs = append(errs, validateParameters(ep, typeSpec, spec)...)
	if h := parameterHostname(ep); h != "" && !controller.HostMatches(h, hostnames) {
		msg := fmt.Sprintf("hostname %q is not allowed by %s %q", h, typeKind(ep), ep.Spec.Type.Name)
		if len(hostnames) == 0 {
			msg += "; this type serves only the environment's gateway host, so leave hostname empty"
		} else {
			msg += "; allowed: " + strings.Join(hostnames, ", ")
		}
		errs = append(errs, field.Forbidden(spec.Child("parameters", "hostname"), msg))
	}
	if len(kinds) == 0 {
		kinds = []openchoreodevv1alpha1.EndpointBackendKind{openchoreodevv1alpha1.EndpointBackendKindComponent}
	}
	for i, ref := range ep.Spec.BackendRefs {
		kind := ref.Kind
		if kind == "" {
			kind = openchoreodevv1alpha1.EndpointBackendKindComponent
		}
		if !slices.Contains(kinds, kind) {
			errs = append(errs, field.Forbidden(spec.Child("backendRefs").Index(i),
				fmt.Sprintf("%s %q does not allow %s backends; ask a platform engineer to add %s to allowedBackendKinds, or use another type",
					typeKind(ep), ep.Spec.Type.Name, kind, kind)))
		}
	}
	return errs, nil
}

// validateClaims checks the Endpoint's slots against the other Endpoints: two
// Endpoints of a project may route to the same component workload endpoint,
// but not in the same visibility, since each would render a route for it.
func (v *Validator) validateClaims(ctx context.Context, ep *openchoreodevv1alpha1.Endpoint, spec *field.Path) (field.ErrorList, error) {
	var others openchoreodevv1alpha1.EndpointList
	if err := v.Client.List(ctx, &others, client.InNamespace(ep.Namespace)); err != nil {
		return nil, apierrors.NewInternalError(fmt.Errorf("list endpoints: %w", err))
	}
	var errs field.ErrorList
	for i := range others.Items {
		other := &others.Items[i]
		if other.Name == ep.Name || !other.DeletionTimestamp.IsZero() {
			continue
		}
		if slot, ok := controller.SharedEndpointSlot(ep, other); ok {
			errs = append(errs, field.Forbidden(spec.Child("backendRefs"),
				fmt.Sprintf("Endpoint %q already routes %s/%s with %s visibility; add your backends to that Endpoint, or use another visibility",
					other.Name, slot.Component, slot.Endpoint, slot.Visibility)))
		}
	}
	return errs, nil
}

// typeSpec reads the Endpoint's (Cluster)EndpointType spec; nil when the type
// does not exist.
func (v *Validator) typeSpec(ctx context.Context, ep *openchoreodevv1alpha1.Endpoint) (*openchoreodevv1alpha1.EndpointTypeSpec, error) {
	if ep.Spec.Type.Kind == openchoreodevv1alpha1.EndpointTypeRefKindClusterEndpointType {
		cet := &openchoreodevv1alpha1.ClusterEndpointType{}
		if err := v.Client.Get(ctx, types.NamespacedName{Name: ep.Spec.Type.Name}, cet); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, nil
			}
			return nil, apierrors.NewInternalError(err)
		}
		s := openchoreodevv1alpha1.EndpointTypeSpec(cet.Spec)
		return &s, nil
	}
	et := &openchoreodevv1alpha1.EndpointType{}
	if err := v.Client.Get(ctx, types.NamespacedName{Namespace: ep.Namespace, Name: ep.Spec.Type.Name}, et); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, apierrors.NewInternalError(err)
	}
	return &et.Spec, nil
}

// validateParameters checks spec.parameters against the type's parameters
// schema. The schema is also the type's list of supported traffic features:
// a type that cannot render, for example, mirroring leaves it out, so an
// Endpoint that asks for it is refused here rather than silently ignored.
func validateParameters(ep *openchoreodevv1alpha1.Endpoint, ts *openchoreodevv1alpha1.EndpointTypeSpec, spec *field.Path) field.ErrorList {
	section := ts.Parameters
	if section == nil || section.GetRaw() == nil || len(section.GetRaw().Raw) == 0 {
		return nil
	}
	path := spec.Child("parameters")
	jsonSchema, err := schema.SectionToJSONSchema(section)
	if err != nil {
		return field.ErrorList{field.Invalid(path, "", fmt.Sprintf("%s %q has an invalid parameters schema: %v", typeKind(ep), ep.Spec.Type.Name, err))}
	}
	params := map[string]any{}
	if ep.Spec.Parameters != nil && len(ep.Spec.Parameters.Raw) > 0 {
		if err := json.Unmarshal(ep.Spec.Parameters.Raw, &params); err != nil {
			return field.ErrorList{field.Invalid(path, "", fmt.Sprintf("parameters are not a JSON object: %v", err))}
		}
	}
	if err := schema.ValidateWithJSONSchema(params, jsonSchema); err != nil {
		return field.ErrorList{field.Invalid(path, "",
			fmt.Sprintf("parameters do not match the schema of %s %q: %v", typeKind(ep), ep.Spec.Type.Name, err))}
	}
	return nil
}

func hasWeightedBackend(refs []openchoreodevv1alpha1.EndpointBackendRef) bool {
	for _, ref := range refs {
		if controller.EndpointBackendWeight(ref) > 0 {
			return true
		}
	}
	return false
}

func typeKind(ep *openchoreodevv1alpha1.Endpoint) openchoreodevv1alpha1.EndpointTypeRefKind {
	if ep.Spec.Type.Kind == "" {
		return openchoreodevv1alpha1.EndpointTypeRefKindEndpointType
	}
	return ep.Spec.Type.Kind
}

// parameterHostname is the hostname the Endpoint asks its type to serve, by
// the convention of a top-level "hostname" parameter; empty when it has none.
func parameterHostname(ep *openchoreodevv1alpha1.Endpoint) string {
	if ep.Spec.Parameters == nil || len(ep.Spec.Parameters.Raw) == 0 {
		return ""
	}
	var params struct {
		Hostname string `json:"hostname"`
	}
	if err := json.Unmarshal(ep.Spec.Parameters.Raw, &params); err != nil {
		return ""
	}
	return params.Hostname
}
