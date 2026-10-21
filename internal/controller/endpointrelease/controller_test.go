// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointrelease

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
)

var _ = Describe("EndpointRelease Controller", func() {
	Context("When reconciling an EndpointRelease resource", func() {
		const erName = "test-endpointrelease"
		const erNamespace = "default"

		erNamespacedName := types.NamespacedName{
			Name:      erName,
			Namespace: erNamespace,
		}

		It("should successfully reconcile the resource", func() {
			By("Creating the EndpointRelease resource")
			er := &openchoreov1alpha1.EndpointRelease{}
			err := k8sClient.Get(ctx, erNamespacedName, er)
			if err != nil && errors.IsNotFound(err) {
				er = &openchoreov1alpha1.EndpointRelease{
					ObjectMeta: metav1.ObjectMeta{
						Name:      erName,
						Namespace: erNamespace,
					},
					Spec: openchoreov1alpha1.EndpointReleaseSpec{
						Owner: openchoreov1alpha1.EndpointReleaseOwner{
							ProjectName:  "test-project",
							EndpointName: "test-endpoint",
						},
						EndpointType: openchoreov1alpha1.EndpointReleaseEndpointType{
							Kind: openchoreov1alpha1.EndpointTypeRefKindClusterEndpointType,
							Name: "http-weighted",
							Spec: openchoreov1alpha1.EndpointTypeSpec{
								Resources: []openchoreov1alpha1.ResourceTemplate{
									{
										ID: "route",
										Template: &runtime.RawExtension{
											Raw: []byte(`{"apiVersion":"gateway.networking.k8s.io/v1","kind":"HTTPRoute","metadata":{"name":"test"}}`),
										},
									},
								},
							},
						},
						Address: openchoreov1alpha1.EndpointAddress{Path: "/test-endpoint"},
						BackendRefs: []openchoreov1alpha1.EndpointBackendRef{
							{Name: "payments-v1", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "payments-v1", Endpoint: "api"}},
						},
					},
				}
				Expect(k8sClient.Create(ctx, er)).To(Succeed())
			}

			By("Reconciling the EndpointRelease resource")
			erReconciler := &Reconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err = erReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: erNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking the EndpointRelease resource exists")
			Eventually(func() error {
				return k8sClient.Get(ctx, erNamespacedName, er)
			}, time.Second*10, time.Millisecond*500).Should(Succeed())

			By("Cleaning up the EndpointRelease resource")
			Expect(k8sClient.Delete(ctx, er)).To(Succeed())
		})
	})
})
