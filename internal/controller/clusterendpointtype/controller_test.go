// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package clusterendpointtype

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

var _ = Describe("ClusterEndpointType Controller", func() {
	Context("When reconciling a ClusterEndpointType resource", func() {
		const cetName = "test-clusterendpointtype"

		cetNamespacedName := types.NamespacedName{Name: cetName}

		It("should successfully reconcile the resource", func() {
			By("Creating the ClusterEndpointType resource")
			cet := &openchoreov1alpha1.ClusterEndpointType{}
			err := k8sClient.Get(ctx, cetNamespacedName, cet)
			if err != nil && errors.IsNotFound(err) {
				cet = &openchoreov1alpha1.ClusterEndpointType{
					ObjectMeta: metav1.ObjectMeta{
						Name: cetName,
					},
					Spec: openchoreov1alpha1.ClusterEndpointTypeSpec{
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
				Expect(k8sClient.Create(ctx, cet)).To(Succeed())
			}

			By("Reconciling the ClusterEndpointType resource")
			cetReconciler := &Reconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err = cetReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: cetNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking the ClusterEndpointType resource exists")
			Eventually(func() error {
				return k8sClient.Get(ctx, cetNamespacedName, cet)
			}, time.Second*10, time.Millisecond*500).Should(Succeed())

			By("Cleaning up the ClusterEndpointType resource")
			Expect(k8sClient.Delete(ctx, cet)).To(Succeed())
		})
	})
})
