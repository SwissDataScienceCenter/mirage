package decide_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestDecide(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Decide Suite")
}
