// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2" //nolint:revive
	. "github.com/onsi/gomega"    //nolint:revive

	"github.com/openchoreo/openchoreo/test/e2e/framework"
)

// A canary rollout end to end through occ and openchoreo-api: start adds the
// new version at 0%, each step writes the development binding's
// backendOverrides and the gateway's route follows, a stale step is refused,
// abort goes back to stable, and finish leaves the new version alone.
var _ = Describe("Endpoint Rollout", Ordered, Label("tier2"), func() {
	const (
		routeSelector = "openchoreo.dev/endpoint=" + endpointName
		pollInterval  = 2 * time.Second
	)

	var dpDevNs string

	bindingName := endpointName + "-" + envDev

	jsonpath := func(g Gomega, resource, name, path string) string {
		out, err := framework.KubectlGetJsonpath(kubeContext, cpNs, resource, name, path)
		g.Expect(err).NotTo(HaveOccurred(), "reading %s %s %s", resource, name, path)
		return out
	}

	rollout := func(args ...string) (string, error) {
		stdout, _, err := occ.Run(append([]string{"endpoint", "rollout"}, append(args, "--namespace", cpNs)...)...)
		return stdout, err
	}

	// routeWeights is the rendered route's backend weights by Service, which
	// is named after the backend's component.
	routeWeights := func(g Gomega) map[string]int {
		names, err := framework.GetHTTPRouteNames(kubeContext, dpDevNs, routeSelector)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(names).To(HaveLen(1), "expected exactly one HTTPRoute for the Endpoint in %s", dpDevNs)
		out, err := framework.KubectlGetJsonpath(kubeContext, dpDevNs, "httproute.gateway.networking.k8s.io", names[0],
			`{range .spec.rules[0].backendRefs[*]}{.name}:{.weight}{" "}{end}`)
		g.Expect(err).NotTo(HaveOccurred())
		weights := map[string]int{}
		for _, ref := range strings.Fields(out) {
			name, raw, _ := strings.Cut(ref, ":")
			w, err := strconv.Atoi(raw)
			g.Expect(err).NotTo(HaveOccurred(), "weight of %s", ref)
			weights[name] = w
		}
		return weights
	}

	expectRoute := func(want map[string]int) {
		GinkgoHelper()
		Eventually(func(g Gomega) {
			g.Expect(routeWeights(g)).To(Equal(want))
		}, 2*time.Minute, pollInterval).Should(Succeed())
	}

	// servedWeight is what the development binding reports a backend gets.
	servedWeight := func(g Gomega, backend string) string {
		return jsonpath(g, "endpointreleasebinding", bindingName,
			fmt.Sprintf(`{.status.backends[?(@.name=="%s")].weight}`, backend))
	}

	BeforeAll(func() {
		By("creating the control-plane namespace and platform resources")
		output, err := framework.KubectlApplyLiteral(kubeContext, cpNamespaceYAML())
		Expect(err).NotTo(HaveOccurred(), "failed to create CP namespace: %s", output)
		output, err = framework.KubectlApplyLiteral(kubeContext, platformResourcesYAML())
		Expect(err).NotTo(HaveOccurred(), "failed to apply platform resources: %s", output)

		By("deploying both versions to development")
		for _, comp := range []string{compStable, compCanary} {
			output, err = framework.KubectlApplyLiteral(kubeContext, backendComponentYAML(comp))
			Expect(err).NotTo(HaveOccurred(), "failed to create %s: %s", comp, output)
		}
		Eventually(func() error {
			var discoverErr error
			dpDevNs, discoverErr = framework.GetDPNamespace(kubeContext, cpNs, projectName, envDev)
			return discoverErr
		}, 3*time.Minute, 5*time.Second).Should(Succeed(), "dp namespace for development not found")
		output, err = framework.KubectlApplyLiteral(kubeContext, testerPodYAML(dpDevNs))
		Expect(err).NotTo(HaveOccurred(), "failed to create tester pod: %s", output)
		for _, comp := range []string{compStable, compCanary} {
			framework.WaitForReleaseBindingReady(kubeContext, cpNs, comp+"-"+envDev)
		}
		Eventually(func(g Gomega) {
			framework.AssertAllPodsRunning(g, kubeContext, dpDevNs)
		}, 3*time.Minute, pollInterval).Should(Succeed(), "pods in %s not running in time", dpDevNs)

		By("applying the ClusterEndpointType and an Endpoint serving the stable version")
		output, err = framework.KubectlApplyLiteral(kubeContext, clusterEndpointTypeYAML())
		Expect(err).NotTo(HaveOccurred(), "failed to create ClusterEndpointType: %s", output)
		output, err = framework.KubectlApplyLiteral(kubeContext, endpointYAML())
		Expect(err).NotTo(HaveOccurred(), "failed to create Endpoint: %s", output)
		expectRoute(map[string]int{compStable: 100})
	})

	AfterAll(func() {
		if os.Getenv("E2E_KEEP_RESOURCES") == "true" {
			By("skipping cleanup because E2E_KEEP_RESOURCES=true")
			return
		}
		By("cleaning up")
		_, _ = framework.Kubectl(kubeContext, "delete", "namespace", cpNs, "--ignore-not-found", "--wait=false")
		_, _ = framework.Kubectl(kubeContext, "delete", "clusterendpointtype", endpointTypeName, "--ignore-not-found", "--wait=false")
		if dpDevNs != "" {
			_, _ = framework.Kubectl(kubeContext, "delete", "namespace", dpDevNs, "--ignore-not-found", "--wait=false")
		}
	})

	It("starts a canary with the new version at 0%", func() {
		_, err := rollout("start", endpointName, "--strategy", "canary", "--stable", "stable",
			"--candidate", "canary="+compCanary+"/http", "--steps", "25,100")
		Expect(err).NotTo(HaveOccurred())

		Eventually(func(g Gomega) {
			g.Expect(jsonpath(g, "endpoints.openchoreo.dev", endpointName, "{.spec.rollout.strategy}")).To(Equal("canary"))
		}, time.Minute, pollInterval).Should(Succeed())

		By("autoDeploy moving development to the release with both versions")
		expectRoute(map[string]int{compStable: 100, compCanary: 0})
		_, err = rollout("status", endpointName, "--env", envDev, "--timeout", "2m")
		Expect(err).NotTo(HaveOccurred())
	})

	It("moves 25% to the new version on the first step", func() {
		_, err := rollout("step", endpointName, "--env", envDev, "--expect-step", "0")
		Expect(err).NotTo(HaveOccurred())

		Eventually(func(g Gomega) {
			g.Expect(jsonpath(g, "endpointreleasebinding", bindingName,
				`{.spec.backendOverrides[?(@.name=="canary")].weight}`)).To(Equal("25"))
		}, time.Minute, pollInterval).Should(Succeed())
		expectRoute(map[string]int{compStable: 75, compCanary: 25})
		_, err = rollout("status", endpointName, "--env", envDev, "--timeout", "2m")
		Expect(err).NotTo(HaveOccurred())
	})

	It("refuses a step from a step the environment has left", func() {
		_, err := rollout("step", endpointName, "--env", envDev, "--expect-step", "0")
		Expect(err).To(MatchError(ContainSubstring("is at step 1")))
		Consistently(func(g Gomega) {
			g.Expect(routeWeights(g)).To(Equal(map[string]int{compStable: 75, compCanary: 25}))
		}, 10*time.Second, pollInterval).Should(Succeed())
	})

	It("refuses to finish while the version that leaves still gets traffic", func() {
		_, err := rollout("finish", endpointName, "--keep", "canary")
		Expect(err).To(MatchError(ContainSubstring("still gets traffic")))
		Eventually(func(g Gomega) {
			g.Expect(jsonpath(g, "endpoints.openchoreo.dev", endpointName, "{.spec.rollout.strategy}")).To(Equal("canary"))
		}, time.Minute, pollInterval).Should(Succeed())
	})

	It("sends all traffic back to stable on abort", func() {
		_, err := rollout("abort", endpointName, "--env", envDev)
		Expect(err).NotTo(HaveOccurred())
		expectRoute(map[string]int{compStable: 100, compCanary: 0})
	})

	It("ships the new version: last step, then finish", func() {
		_, err := rollout("step", endpointName, "--env", envDev, "--last")
		Expect(err).NotTo(HaveOccurred())
		expectRoute(map[string]int{compStable: 0, compCanary: 100})
		Eventually(func(g Gomega) {
			g.Expect(servedWeight(g, "stable")).To(Equal("0"))
		}, time.Minute, pollInterval).Should(Succeed())

		_, err = rollout("finish", endpointName, "--keep", "canary")
		Expect(err).NotTo(HaveOccurred())

		Eventually(func(g Gomega) {
			g.Expect(jsonpath(g, "endpoints.openchoreo.dev", endpointName, "{.spec.rollout}")).To(BeEmpty())
			g.Expect(jsonpath(g, "endpoints.openchoreo.dev", endpointName, "{.spec.backendRefs[*].name}")).To(Equal("canary"))
		}, time.Minute, pollInterval).Should(Succeed())

		By("autoDeploy moving development to the release with the new version alone")
		Eventually(func(g Gomega) {
			weights := routeWeights(g)
			g.Expect(weights).To(HaveLen(1))
			g.Expect(weights).To(HaveKey(compCanary))
		}, 2*time.Minute, pollInterval).Should(Succeed())

		By("serving the new version through the gateway")
		Eventually(func(g Gomega) {
			out, err := framework.KubectlExecByLabel(kubeContext, dpDevNs, testerLabel, testerContainer,
				"wget", "-q", "-O", "-", "-T", "5", routeURL())
			g.Expect(err).NotTo(HaveOccurred(), "request to %s failed", routeURL())
			g.Expect(strings.TrimSpace(out)).To(Equal(compCanary))
		}, 2*time.Minute, time.Second).Should(Succeed())
	})
})
