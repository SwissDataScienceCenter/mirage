package mask_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestMask(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Mask Suite")
}
