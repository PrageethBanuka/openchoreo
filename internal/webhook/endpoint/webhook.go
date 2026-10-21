// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpoint

import (
	"context"
	"encoding/json"
	"fmt"
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

// Validator keeps one route per address. It refuses an Endpoint whose address
// another Endpoint already serves, whose path is a component's own route path
// without a take-over of it, or whose take-over another Endpoint already
// holds. It also holds the EndpointType's limits (address kind, allowed
// hostnames and backend kinds, the parameters schema) and refuses a split with
// no weighted backend. Each refusal
// says what to do instead. The Endpoint and binding controllers enforce the
// same rules when this webhook is bypassed.
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
	claimsChanged := !equality.Semantic.DeepEqual(ep.Spec.Address, oldEp.Spec.Address) ||
		!equality.Semantic.DeepEqual(ep.Spec.TakeOver, oldEp.Spec.TakeOver)
	backendsChanged := !equality.Semantic.DeepEqual(ep.Spec.BackendRefs, oldEp.Spec.BackendRefs) ||
		!equality.Semantic.DeepEqual(ep.Spec.Parameters, oldEp.Spec.Parameters)
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

// validateTypeLimits checks the address, hostname, backend kinds and
// parameters against the EndpointType. A missing type is left to the
// controller, which reports it.
func (v *Validator) validateTypeLimits(ctx context.Context, ep *openchoreodevv1alpha1.Endpoint, spec *field.Path) (field.ErrorList, error) {
	typeSpec, err := v.typeSpec(ctx, ep)
	if err != nil || typeSpec == nil {
		return nil, err
	}
	hostnames, kinds := typeSpec.AllowedHostnames, typeSpec.AllowedBackendKinds
	var errs field.ErrorList
	errs = append(errs, validateAddressKind(ep, typeSpec, spec)...)
	errs = append(errs, validateParameters(ep, typeSpec, spec)...)
	if h := addressHostname(ep); h != "" && !controller.HostMatches(h, hostnames) {
		msg := fmt.Sprintf("hostname %q is not allowed by %s %q", h, typeKind(ep), ep.Spec.Type.Name)
		if len(hostnames) == 0 {
			msg += "; this type serves only the environment's gateway host, so leave hostname empty"
		} else {
			msg += "; allowed: " + strings.Join(hostnames, ", ")
		}
		errs = append(errs, field.Forbidden(spec.Child("address", "hostname"), msg))
	}
	if len(kinds) == 0 {
		kinds = []openchoreodevv1alpha1.EndpointBackendKind{openchoreodevv1alpha1.EndpointBackendKindComponent}
	}
	for i, ref := range ep.Spec.BackendRefs {
		allowed := false
		for _, k := range kinds {
			if k == ref.Kind() {
				allowed = true
			}
		}
		if !allowed {
			errs = append(errs, field.Forbidden(spec.Child("backendRefs").Index(i),
				fmt.Sprintf("%s %q does not allow %s backends; ask a platform engineer to add %s to allowedBackendKinds, or use another type",
					typeKind(ep), ep.Spec.Type.Name, ref.Kind(), ref.Kind())))
		}
	}
	return errs, nil
}

// validateClaims checks the address and the take-over against the other
// Endpoints and against component routes.
func (v *Validator) validateClaims(ctx context.Context, ep *openchoreodevv1alpha1.Endpoint, spec *field.Path) (field.ErrorList, error) {
	var others openchoreodevv1alpha1.EndpointList
	if err := v.Client.List(ctx, &others); err != nil {
		return nil, apierrors.NewInternalError(fmt.Errorf("list endpoints: %w", err))
	}
	var errs field.ErrorList
	for i := range others.Items {
		other := &others.Items[i]
		if (other.Name == ep.Name && other.Namespace == ep.Namespace) || !other.DeletionTimestamp.IsZero() {
			continue
		}
		if controller.SameEndpointAddress(ep, other) {
			errs = append(errs, field.Forbidden(spec.Child("address"),
				fmt.Sprintf("%s is already served by Endpoint %s/%s; pick another path or hostname",
					describeAddress(ep.Spec.Address), other.Namespace, other.Name)))
		}
		if controller.SameEndpointTakeOver(ep, other) {
			t := controller.TakenOver(ep)
			errs = append(errs, field.Forbidden(spec.Child("takeOver"),
				fmt.Sprintf("the URL of %s/%s is already taken over by Endpoint %q; one Endpoint takes a URL over",
					t.Name, t.Endpoint, other.Name)))
		}
	}

	// An address on the environment host must not be a component's own route
	// path, unless this Endpoint takes that route over.
	if ep.Spec.Address != nil && ep.Spec.Address.Hostname == "" {
		owner, err := v.componentServingPath(ctx, ep)
		if err != nil {
			return nil, err
		}
		if owner != "" {
			errs = append(errs, field.Forbidden(spec.Child("address", "path"),
				fmt.Sprintf("%s is the URL path of component endpoint %s; take it over (spec.takeOver) to move it to this Endpoint, or pick another path",
					ep.Spec.Address.Path, owner)))
		}
	}
	return errs, nil
}

// componentServingPath returns "component/endpoint" when a component of the
// namespace serves the Endpoint's path on an environment gateway host and the
// Endpoint doesn't take that endpoint over.
func (v *Validator) componentServingPath(ctx context.Context, ep *openchoreodevv1alpha1.Endpoint) (string, error) {
	var rbs openchoreodevv1alpha1.ReleaseBindingList
	if err := v.Client.List(ctx, &rbs, client.InNamespace(ep.Namespace)); err != nil {
		return "", apierrors.NewInternalError(fmt.Errorf("list release bindings: %w", err))
	}
	t := controller.TakenOver(ep)
	for i := range rbs.Items {
		rb := &rbs.Items[i]
		for _, st := range rb.Status.Endpoints {
			if t != nil && t.Name == rb.Spec.Owner.ComponentName && t.Endpoint == st.Name {
				continue
			}
			for _, urls := range []*openchoreodevv1alpha1.EndpointGatewayURLs{st.ExternalURLs, st.InternalURLs} {
				if urls == nil {
					continue
				}
				for _, u := range []*openchoreodevv1alpha1.EndpointURL{urls.HTTPS, urls.HTTP, urls.TLS} {
					if u != nil && u.Path == ep.Spec.Address.Path {
						return rb.Spec.Owner.ComponentName + "/" + st.Name, nil
					}
				}
			}
		}
	}
	return "", nil
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

// validateAddressKind checks the Endpoint against the type's address kind: a
// Gateway type serves the Endpoint's own address, so it needs one; a Service
// type serves the taken-over component's in-cluster Service, so it needs a
// take-over and no address.
func validateAddressKind(ep *openchoreodevv1alpha1.Endpoint, ts *openchoreodevv1alpha1.EndpointTypeSpec, spec *field.Path) field.ErrorList {
	var errs field.ErrorList
	if ts.ServesService() {
		if ep.Spec.Address != nil {
			errs = append(errs, field.Forbidden(spec.Child("address"),
				fmt.Sprintf("%s %q serves the taken-over component's in-cluster Service, not an address of its own; remove spec.address",
					typeKind(ep), ep.Spec.Type.Name)))
		}
		if ep.Spec.TakeOver == nil {
			errs = append(errs, field.Required(spec.Child("takeOver"),
				fmt.Sprintf("%s %q serves the in-cluster Service of a component endpoint; set spec.takeOver to the component endpoint whose callers it routes",
					typeKind(ep), ep.Spec.Type.Name)))
		}
		return errs
	}
	if ep.Spec.Address == nil {
		errs = append(errs, field.Required(spec.Child("address"),
			fmt.Sprintf("%s %q serves the Endpoint's own address on a gateway; set spec.address.path", typeKind(ep), ep.Spec.Type.Name)))
	}
	return errs
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

// addressHostname is the Endpoint's own hostname, empty when it has none or no
// address at all.
func addressHostname(ep *openchoreodevv1alpha1.Endpoint) string {
	if ep.Spec.Address == nil {
		return ""
	}
	return ep.Spec.Address.Hostname
}

func describeAddress(a *openchoreodevv1alpha1.EndpointAddress) string {
	if a == nil {
		return "(none)"
	}
	if a.Hostname == "" {
		return a.Path + " on the environment host"
	}
	return a.Hostname + a.Path
}
