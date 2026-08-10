package config_test

import (
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/SwissDataScienceCenter/mirage/internal/config"
)

var _ = Describe("Load", func() {
	It("reads handled and masked entries", func() {
		c, err := config.Load(filepath.Join("testdata", "valid.yaml"))
		Expect(err).NotTo(HaveOccurred())

		Expect(c.Handled).To(Equal([]config.Resource{
			{Group: "", Resource: "pods"},
			{Group: "shipwright.io", Resource: "builds"},
		}))
		Expect(c.Masked).To(Equal([]config.Masked{
			{
				Resource: config.Resource{Group: "shipwright.io", Resource: "clusterbuildstrategies"},
				Kind:     "ClusterBuildStrategy",
			},
		}))
	})

	It("rejects unknown fields", func() {
		// API versions are deliberately not part of the config; an entry applies
		// to every version. Silently ignoring a `version` key would leave the
		// Deployer believing it did something.
		_, err := config.Load(filepath.Join("testdata", "unknown-field.yaml"))
		Expect(err).To(HaveOccurred())
	})

	It("reports a missing file", func() {
		_, err := config.Load(filepath.Join("testdata", "does-not-exist.yaml"))
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("Validate", func() {
	It("accepts an empty configuration", func() {
		Expect(config.Config{}.Validate()).To(Succeed())
	})

	It("accepts the same resource name in two groups", func() {
		Expect(config.Config{Handled: []config.Resource{
			{Group: "shipwright.io", Resource: "builds"},
			{Group: "tekton.dev", Resource: "builds"},
		}}.Validate()).To(Succeed())
	})

	It("rejects a handled entry without a resource", func() {
		Expect(config.Config{Handled: []config.Resource{
			{Group: "shipwright.io"},
		}}.Validate()).NotTo(Succeed())
	})

	It("rejects a masked entry without a kind", func() {
		// Without a Kind there is no name for the empty list Mirage synthesises.
		Expect(config.Config{Masked: []config.Masked{
			{Resource: config.Resource{Group: "shipwright.io", Resource: "clusterbuildstrategies"}},
		}}.Validate()).NotTo(Succeed())
	})

	It("rejects the same resource handled twice", func() {
		Expect(config.Config{Handled: []config.Resource{
			{Group: "shipwright.io", Resource: "builds"},
			{Group: "shipwright.io", Resource: "builds"},
		}}.Validate()).NotTo(Succeed())
	})

	It("rejects a resource that is both handled and masked", func() {
		// The two decisions are mutually exclusive; preferring one silently would
		// make Mirage's behaviour depend on an ordering the Deployer cannot see.
		Expect(config.Config{
			Handled: []config.Resource{{Group: "shipwright.io", Resource: "builds"}},
			Masked: []config.Masked{
				{Resource: config.Resource{Group: "shipwright.io", Resource: "builds"}, Kind: "Build"},
			},
		}.Validate()).NotTo(Succeed())
	})
})
