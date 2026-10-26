// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpoint

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/controller"
)

var _ = Describe("Finalizer", func() {
	finCtx := context.Background()

	newEndpoint := func(name string, withFinalizer bool) *openchoreov1alpha1.Endpoint {
		om := metav1.ObjectMeta{Name: name, Namespace: "default"}
		if withFinalizer {
			om.Finalizers = []string{EndpointFinalizer}
		}
		return &openchoreov1alpha1.Endpoint{
			ObjectMeta: om,
			Spec: openchoreov1alpha1.EndpointSpec{
				Owner: openchoreov1alpha1.EndpointOwner{ProjectName: "p"},
				Type:  openchoreov1alpha1.EndpointTypeRef{Name: "et"},
				BackendRefs: []openchoreov1alpha1.EndpointBackendRef{
					{Name: "c", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "c", Endpoint: "api"}},
				},
			},
		}
	}

	newRelease := func(name, ownerEndpoint, project string) *openchoreov1alpha1.EndpointRelease {
		return &openchoreov1alpha1.EndpointRelease{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: openchoreov1alpha1.EndpointReleaseSpec{
				Owner: openchoreov1alpha1.EndpointReleaseOwner{
					ProjectName:  project,
					EndpointName: ownerEndpoint,
				},
			},
		}
	}

	newBinding := func(name, ownerEndpoint, project string, withFinalizer bool) *openchoreov1alpha1.EndpointReleaseBinding {
		om := metav1.ObjectMeta{Name: name, Namespace: "default"}
		if withFinalizer {
			om.Finalizers = []string{"test-keep"}
		}
		return &openchoreov1alpha1.EndpointReleaseBinding{
			ObjectMeta: om,
			Spec: openchoreov1alpha1.EndpointReleaseBindingSpec{
				Owner: openchoreov1alpha1.EndpointReleaseBindingOwner{
					ProjectName:  project,
					EndpointName: ownerEndpoint,
				},
				Environment: "dev",
			},
		}
	}

	buildClient := func(objs ...client.Object) client.Client {
		return fake.NewClientBuilder().
			WithScheme(scheme.Scheme).
			WithObjects(objs...).
			WithStatusSubresource(&openchoreov1alpha1.Endpoint{}).
			WithIndex(&openchoreov1alpha1.EndpointRelease{},
				controller.IndexKeyEndpointReleaseOwnerEndpointName,
				controller.IndexEndpointReleaseOwner).
			WithIndex(&openchoreov1alpha1.EndpointReleaseBinding{},
				controller.IndexKeyEndpointReleaseBindingOwnerEndpointName,
				controller.IndexEndpointReleaseBindingOwner).
			Build()
	}

	reconcileOnce := func(r *Reconciler, ep *openchoreov1alpha1.Endpoint) reconcile.Result {
		res, err := r.Reconcile(finCtx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(ep)})
		Expect(err).NotTo(HaveOccurred())
		return res
	}

	It("deletes an Endpoint with no backends", func() {
		ep := newEndpoint("orphan-delete", true)
		ep.Spec.BackendRefs = nil
		cli := buildClient(ep)
		r := &Reconciler{Client: cli, Scheme: scheme.Scheme}

		reconcileOnce(r, ep)

		updated := &openchoreov1alpha1.Endpoint{}
		Expect(cli.Get(finCtx, client.ObjectKeyFromObject(ep), updated)).To(Succeed())
		Expect(updated.DeletionTimestamp).NotTo(BeNil(), "deleting; the finalizer hands the URL back")
	})

	It("adds the endpoint-cleanup finalizer on first reconcile", func() {
		ep := newEndpoint("fin-add", false)
		cli := buildClient(ep)
		r := &Reconciler{Client: cli, Scheme: scheme.Scheme}

		reconcileOnce(r, ep)

		updated := &openchoreov1alpha1.Endpoint{}
		Expect(cli.Get(finCtx, client.ObjectKeyFromObject(ep), updated)).To(Succeed())
		Expect(updated.Finalizers).To(ContainElement(EndpointFinalizer))
	})

	It("deletes bindings first and holds the finalizer until they are gone", func() {
		ep := newEndpoint("fin-bindings", true)
		binding := newBinding("fin-bindings-dev", ep.Name, "p", true)
		release := newRelease("fin-bindings-abc", ep.Name, "p")
		cli := buildClient(ep, binding, release)
		r := &Reconciler{Client: cli, Scheme: scheme.Scheme}

		Expect(cli.Delete(finCtx, ep)).To(Succeed())

		By("first reconcile records the Finalizing condition")
		reconcileOnce(r, ep)

		By("next reconcile requests binding deletion and waits")
		res := reconcileOnce(r, ep)
		Expect(res.RequeueAfter).To(Equal(requeueWaitForChildren))

		gotBinding := &openchoreov1alpha1.EndpointReleaseBinding{}
		Expect(cli.Get(finCtx, client.ObjectKeyFromObject(binding), gotBinding)).To(Succeed())
		Expect(gotBinding.DeletionTimestamp.IsZero()).To(BeFalse(), "binding should be marked for deletion")
		Expect(cli.Get(finCtx, client.ObjectKeyFromObject(release), &openchoreov1alpha1.EndpointRelease{})).
			To(Succeed(), "releases must survive while bindings remain")

		updated := &openchoreov1alpha1.Endpoint{}
		Expect(cli.Get(finCtx, client.ObjectKeyFromObject(ep), updated)).To(Succeed())
		Expect(updated.Finalizers).To(ContainElement(EndpointFinalizer))
		cond := meta.FindStatusCondition(updated.Status.Conditions, string(ConditionFinalizing))
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))

		By("once the binding is gone, releases are deleted and the finalizer is removed")
		gotBinding.Finalizers = nil
		Expect(cli.Update(finCtx, gotBinding)).To(Succeed())

		res = reconcileOnce(r, ep)
		Expect(res.RequeueAfter).To(Equal(requeueWaitForChildren))
		Expect(apierrors.IsNotFound(cli.Get(finCtx, client.ObjectKeyFromObject(release), &openchoreov1alpha1.EndpointRelease{}))).
			To(BeTrue())

		reconcileOnce(r, ep)
		Expect(apierrors.IsNotFound(cli.Get(finCtx, client.ObjectKeyFromObject(ep), &openchoreov1alpha1.Endpoint{}))).
			To(BeTrue(), "Endpoint should be gone once its finalizer is removed")
	})

	It("leaves same-named bindings and releases of another project alone", func() {
		ep := newEndpoint("fin-scoped", true)
		otherBinding := newBinding("fin-scoped-other", ep.Name, "other-project", false)
		otherRelease := newRelease("fin-scoped-other-abc", ep.Name, "other-project")
		cli := buildClient(ep, otherBinding, otherRelease)
		r := &Reconciler{Client: cli, Scheme: scheme.Scheme}

		Expect(cli.Delete(finCtx, ep)).To(Succeed())
		reconcileOnce(r, ep)
		reconcileOnce(r, ep)

		Expect(cli.Get(finCtx, client.ObjectKeyFromObject(otherBinding), &openchoreov1alpha1.EndpointReleaseBinding{})).To(Succeed())
		Expect(cli.Get(finCtx, client.ObjectKeyFromObject(otherRelease), &openchoreov1alpha1.EndpointRelease{})).To(Succeed())
		Expect(apierrors.IsNotFound(cli.Get(finCtx, client.ObjectKeyFromObject(ep), &openchoreov1alpha1.Endpoint{}))).To(BeTrue())
	})
})
