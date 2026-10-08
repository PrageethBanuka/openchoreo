// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpoint

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
)

var _ = Describe("Endpoint Controller", func() {
	const (
		ns      = "default"
		project = "shop"
	)

	var reconciler *Reconciler

	BeforeEach(func() {
		reconciler = &Reconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	})

	routeTemplates := func() []openchoreov1alpha1.ResourceTemplate {
		return []openchoreov1alpha1.ResourceTemplate{
			{
				ID: "route",
				Template: &runtime.RawExtension{
					Raw: []byte(`{"apiVersion":"gateway.networking.k8s.io/v1","kind":"HTTPRoute","metadata":{"name":"${metadata.name}"}}`),
				},
			},
		}
	}

	createClusterEndpointType := func(name string) *openchoreov1alpha1.ClusterEndpointType {
		cet := &openchoreov1alpha1.ClusterEndpointType{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       openchoreov1alpha1.ClusterEndpointTypeSpec{Resources: routeTemplates()},
		}
		Expect(k8sClient.Create(ctx, cet)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, cet) })
		return cet
	}

	createComponent := func(name, projectName string) {
		comp := &openchoreov1alpha1.Component{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: openchoreov1alpha1.ComponentSpec{
				Owner:         openchoreov1alpha1.ComponentOwner{ProjectName: projectName},
				ComponentType: openchoreov1alpha1.ComponentTypeRef{Name: "deployment/service"},
			},
		}
		Expect(k8sClient.Create(ctx, comp)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, comp) })
	}

	// createEndpoint creates an Endpoint with the finalizer pre-set so a single
	// Reconcile call runs the main reconcile path.
	createEndpoint := func(name string, typeRef openchoreov1alpha1.EndpointTypeRef,
		refs ...openchoreov1alpha1.EndpointBackendRef) *openchoreov1alpha1.Endpoint {
		ep := &openchoreov1alpha1.Endpoint{
			ObjectMeta: metav1.ObjectMeta{
				Name:       name,
				Namespace:  ns,
				Finalizers: []string{EndpointFinalizer},
			},
			Spec: openchoreov1alpha1.EndpointSpec{
				Owner:       openchoreov1alpha1.EndpointOwner{ProjectName: project},
				Type:        typeRef,
				BackendRefs: refs,
			},
		}
		Expect(k8sClient.Create(ctx, ep)).To(Succeed())
		DeferCleanup(func() {
			latest := &openchoreov1alpha1.Endpoint{}
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(ep), latest); err == nil {
				latest.Finalizers = nil
				_ = k8sClient.Update(ctx, latest)
				_ = k8sClient.Delete(ctx, latest)
			}
		})
		return ep
	}

	reconcileAndGet := func(ep *openchoreov1alpha1.Endpoint) *openchoreov1alpha1.Endpoint {
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(ep)})
		Expect(err).NotTo(HaveOccurred())
		updated := &openchoreov1alpha1.Endpoint{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ep), updated)).To(Succeed())
		return updated
	}

	readyCondition := func(ep *openchoreov1alpha1.Endpoint) *metav1.Condition {
		cond := meta.FindStatusCondition(ep.Status.Conditions, string(ConditionReady))
		Expect(cond).NotTo(BeNil(), "expected Ready condition to be set")
		return cond
	}

	It("sets Ready=False, Reason=EndpointTypeNotFound when the referenced type does not exist", func() {
		ep := createEndpoint("ep-missing-type",
			openchoreov1alpha1.EndpointTypeRef{Name: "does-not-exist"},
			openchoreov1alpha1.EndpointBackendRef{ComponentName: "payments-v1", Endpoint: "api"})

		updated := reconcileAndGet(ep)

		cond := readyCondition(updated)
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal(string(ReasonEndpointTypeNotFound)))
		Expect(updated.Status.LatestRelease).To(BeNil())
	})

	It("sets Ready=False, Reason=BackendNotFound when a backend component is missing or in another project", func() {
		createClusterEndpointType("cet-backend-check")
		createComponent("backend-other-project", "another-project")

		ep := createEndpoint("ep-missing-backend",
			openchoreov1alpha1.EndpointTypeRef{
				Kind: openchoreov1alpha1.EndpointTypeRefKindClusterEndpointType,
				Name: "cet-backend-check",
			},
			openchoreov1alpha1.EndpointBackendRef{ComponentName: "backend-absent", Endpoint: "api"},
			openchoreov1alpha1.EndpointBackendRef{ComponentName: "backend-other-project", Endpoint: "api"})

		updated := reconcileAndGet(ep)

		cond := readyCondition(updated)
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal(string(ReasonBackendNotFound)))
		Expect(cond.Message).To(ContainSubstring("backend-absent"))
		Expect(cond.Message).To(ContainSubstring("backend-other-project"))
		Expect(updated.Status.LatestRelease).To(BeNil())
	})

	It("cuts an EndpointRelease that snapshots the type and backends, and a new one when weights change", func() {
		createClusterEndpointType("cet-release")
		createComponent("release-v1", project)
		createComponent("release-v2", project)

		ep := createEndpoint("ep-release",
			openchoreov1alpha1.EndpointTypeRef{
				Kind: openchoreov1alpha1.EndpointTypeRefKindClusterEndpointType,
				Name: "cet-release",
			},
			openchoreov1alpha1.EndpointBackendRef{ComponentName: "release-v1", Endpoint: "api", Weight: ptr.To[int32](90)},
			openchoreov1alpha1.EndpointBackendRef{ComponentName: "release-v2", Endpoint: "api", Weight: ptr.To[int32](10)})

		By("cutting the first release")
		updated := reconcileAndGet(ep)
		Expect(readyCondition(updated).Status).To(Equal(metav1.ConditionTrue))
		Expect(updated.Status.LatestRelease).NotTo(BeNil())
		first := updated.Status.LatestRelease.Name
		Expect(first).To(HavePrefix("ep-release-"))

		release := &openchoreov1alpha1.EndpointRelease{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: first, Namespace: ns}, release)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, release) })
		Expect(release.Spec.Owner).To(Equal(openchoreov1alpha1.EndpointReleaseOwner{
			ProjectName: project, EndpointName: "ep-release",
		}))
		Expect(release.Spec.EndpointType.Kind).To(Equal(openchoreov1alpha1.EndpointTypeRefKindClusterEndpointType))
		Expect(release.Spec.EndpointType.Name).To(Equal("cet-release"))
		Expect(release.Spec.EndpointType.Spec.Resources).To(HaveLen(1))
		Expect(release.Spec.BackendRefs).To(HaveLen(2))
		Expect(*release.Spec.BackendRefs[0].Weight).To(Equal(int32(90)))

		By("reconciling again without changes keeps the same release")
		Expect(reconcileAndGet(ep).Status.LatestRelease.Name).To(Equal(first))

		By("shifting traffic to 50/50 cuts a new release")
		updated.Spec.BackendRefs[0].Weight = ptr.To[int32](50)
		updated.Spec.BackendRefs[1].Weight = ptr.To[int32](50)
		Expect(k8sClient.Update(ctx, updated)).To(Succeed())

		shifted := reconcileAndGet(ep)
		second := shifted.Status.LatestRelease.Name
		Expect(second).NotTo(Equal(first))

		secondRelease := &openchoreov1alpha1.EndpointRelease{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: second, Namespace: ns}, secondRelease)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, secondRelease) })
		Expect(*secondRelease.Spec.BackendRefs[0].Weight).To(Equal(int32(50)))

		By("the earlier release is left in place for bindings still pinned to it")
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: first, Namespace: ns}, &openchoreov1alpha1.EndpointRelease{})).To(Succeed())
	})

	It("resolves a namespaced EndpointType when kind is omitted", func() {
		et := &openchoreov1alpha1.EndpointType{
			ObjectMeta: metav1.ObjectMeta{Name: "et-namespaced", Namespace: ns},
			Spec:       openchoreov1alpha1.EndpointTypeSpec{Resources: routeTemplates()},
		}
		Expect(k8sClient.Create(ctx, et)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, et) })
		createComponent("namespaced-v1", project)

		ep := createEndpoint("ep-namespaced",
			openchoreov1alpha1.EndpointTypeRef{Name: "et-namespaced"},
			openchoreov1alpha1.EndpointBackendRef{ComponentName: "namespaced-v1", Endpoint: "api"})

		updated := reconcileAndGet(ep)
		Expect(readyCondition(updated).Status).To(Equal(metav1.ConditionTrue))

		release := &openchoreov1alpha1.EndpointRelease{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: updated.Status.LatestRelease.Name, Namespace: ns}, release)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, release) })
		Expect(release.Spec.EndpointType.Kind).To(Equal(openchoreov1alpha1.EndpointTypeRefKindEndpointType))
		Expect(*release.Spec.BackendRefs[0].Weight).To(Equal(int32(1)), "API server should default weight to 1")
	})

	It("rejects changing spec.type after creation", func() {
		createClusterEndpointType("cet-immutable")
		ep := createEndpoint("ep-immutable",
			openchoreov1alpha1.EndpointTypeRef{
				Kind: openchoreov1alpha1.EndpointTypeRefKindClusterEndpointType,
				Name: "cet-immutable",
			},
			openchoreov1alpha1.EndpointBackendRef{ComponentName: "anything", Endpoint: "api"})

		latest := &openchoreov1alpha1.Endpoint{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ep), latest)).To(Succeed())
		latest.Spec.Type.Name = "something-else"
		err := k8sClient.Update(ctx, latest)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("spec.type cannot be changed after creation"))
	})
})
