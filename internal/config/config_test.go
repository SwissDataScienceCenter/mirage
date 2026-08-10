package config_test

import (
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/SwissDataScienceCenter/mirage/internal/config"
)

var _ = Describe("Load", func() {
	It("reads confined and masked entries", func() {
		c, err := config.Load(filepath.Join("testdata", "valid.yaml"))
		Expect(err).NotTo(HaveOccurred())

		Expect(c.Confined).To(Equal([]config.Resource{
			{Group: "", Plural: "pods"},
			{Group: "shipwright.io", Plural: "builds"},
		}))
		Expect(c.Masked).To(Equal([]config.Masked{
			{
				Resource: config.Resource{Group: "shipwright.io", Plural: "clusterbuildstrategies"},
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
		Expect(config.Config{Confined: []config.Resource{
			{Group: "shipwright.io", Plural: "builds"},
			{Group: "tekton.dev", Plural: "builds"},
		}}.Validate()).To(Succeed())
	})

	It("rejects a confined entry without a plural", func() {
		Expect(config.Config{Confined: []config.Resource{
			{Group: "shipwright.io"},
		}}.Validate()).NotTo(Succeed())
	})

	It("rejects a masked entry without a kind", func() {
		// Without a Kind there is no name for the empty list Mirage synthesises.
		Expect(config.Config{Masked: []config.Masked{
			{Resource: config.Resource{Group: "shipwright.io", Plural: "clusterbuildstrategies"}},
		}}.Validate()).NotTo(Succeed())
	})

	It("rejects the same resource confined twice", func() {
		Expect(config.Config{Confined: []config.Resource{
			{Group: "shipwright.io", Plural: "builds"},
			{Group: "shipwright.io", Plural: "builds"},
		}}.Validate()).NotTo(Succeed())
	})

	It("rejects confined namespaces, which are cluster-scoped", func() {
		// Confining means inserting the Target Namespace into the path, and there is
		// no /api/v1/namespaces/{ns}/namespaces to insert it into.
		Expect(config.Config{Confined: []config.Resource{
			{Plural: "namespaces"},
		}}.Validate()).NotTo(Succeed())
	})

	It("accepts masked namespaces", func() {
		// Masking is coherent: the Client simply sees no namespaces at all.
		Expect(config.Config{Masked: []config.Masked{
			{Resource: config.Resource{Plural: "namespaces"}, Kind: "Namespace"},
		}}.Validate()).To(Succeed())
	})

	It("accepts a confined namespaces resource in another group", func() {
		// A CRD that happens to be called "namespaces" is an ordinary resource and
		// may well be namespaced. Only the core one is cluster-scoped.
		Expect(config.Config{Confined: []config.Resource{
			{Group: "example.com", Plural: "namespaces"},
		}}.Validate()).To(Succeed())
	})

	It("rejects a resource that is both confined and masked", func() {
		// The two decisions are mutually exclusive; preferring one silently would
		// make Mirage's behaviour depend on an ordering the Deployer cannot see.
		Expect(config.Config{
			Confined: []config.Resource{{Group: "shipwright.io", Plural: "builds"}},
			Masked: []config.Masked{
				{Resource: config.Resource{Group: "shipwright.io", Plural: "builds"}, Kind: "Build"},
			},
		}.Validate()).NotTo(Succeed())
	})
})
