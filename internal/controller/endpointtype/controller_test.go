// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointtype

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

var _ = Describe("EndpointType Controller", func() {
	Context("When reconciling an EndpointType resource", func() {
		const etName = "test-endpointtype"
		const etNamespace = "default"

		etNamespacedName := types.NamespacedName{
			Name:      etName,
			Namespace: etNamespace,
		}

		It("should successfully reconcile the resource", func() {
			By("Creating the EndpointType resource")
			et := &openchoreov1alpha1.EndpointType{}
			err := k8sClient.Get(ctx, etNamespacedName, et)
			if err != nil && errors.IsNotFound(err) {
				et = &openchoreov1alpha1.EndpointType{
					ObjectMeta: metav1.ObjectMeta{
						Name:      etName,
						Namespace: etNamespace,
					},
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
				}
				Expect(k8sClient.Create(ctx, et)).To(Succeed())
			}

			By("Reconciling the EndpointType resource")
			etReconciler := &Reconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err = etReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: etNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking the EndpointType resource exists")
			Eventually(func() error {
				return k8sClient.Get(ctx, etNamespacedName, et)
			}, time.Second*10, time.Millisecond*500).Should(Succeed())

			By("Cleaning up the EndpointType resource")
			Expect(k8sClient.Delete(ctx, et)).To(Succeed())
		})
	})
})
