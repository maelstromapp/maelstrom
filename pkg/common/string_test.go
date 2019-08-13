package common

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestInterpolate(t *testing.T) {
	m := map[string]string{
		"A": "aval",
		"B": "bval",
		"b": "bvallower",
		"E": "",
	}

	assert.Equal(t, "", InterpolateWithMap("", m))
	assert.Equal(t, "aval", InterpolateWithMap("${A}", m))
	assert.Equal(t, "${A}", InterpolateWithMap("$${A}", m))
	assert.Equal(t, " hello ${A} ", InterpolateWithMap(" hello $${A} ", m))
	assert.Equal(t, "aval bval\n bvallower ${} ${", InterpolateWithMap("${A} ${B}\n ${b} ${} ${", m))
	assert.Equal(t, "", InterpolateWithMap("${E}", m))
	assert.Equal(t, "defaultVal", InterpolateWithMap("${E:-defaultVal}", m))
	assert.Equal(t, "", InterpolateWithMap("${E-defaultVal}", m))
	assert.Equal(t, "test defaultVal", InterpolateWithMap("test ${D:-defaultVal}", m))
	assert.Equal(t, "test defaultVal", InterpolateWithMap("test ${D:defaultVal}", m))
	assert.Equal(t, "test ", InterpolateWithMap("test ${D:}", m))
}
