package launcher

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCFBinaryName(t *testing.T) {
	t.Run("defaults to cf when unset", func(t *testing.T) {
		assert.Equal(t, "cf", CFBinaryName())
	})

	t.Run("honours CF_BINARY_NAME", func(t *testing.T) {
		t.Setenv("CF_BINARY_NAME", "cf8")
		assert.Equal(t, "cf8", CFBinaryName())
	})

	t.Run("treats an empty CF_BINARY_NAME as unset", func(t *testing.T) {
		t.Setenv("CF_BINARY_NAME", "")
		assert.Equal(t, "cf", CFBinaryName())
	})
}
