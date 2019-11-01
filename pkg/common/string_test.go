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

func TestGlobMatches(t *testing.T) {
	// glob = "*" - matches everything
	assert.True(t, GlobMatches("*", "foo"))
	assert.True(t, GlobMatches("*", ""))

	// glob is string - only exact match
	assert.True(t, GlobMatches("cat", "cat"))
	assert.False(t, GlobMatches("cat", "Cat"))
	assert.False(t, GlobMatches("cat", "cat1"))
	assert.False(t, GlobMatches("cat", "1cat"))

	// glob on both sides - substring match
	assert.True(t, GlobMatches("*cat*", "cat"))
	assert.True(t, GlobMatches("*cat*", "1cat"))
	assert.True(t, GlobMatches("*cat*", "1cat1"))
	assert.True(t, GlobMatches("*cat*", "cat1"))
	assert.False(t, GlobMatches("*cat*", "cdat1"))

	// prefix glob
	assert.True(t, GlobMatches("*cat", "cat"))
	assert.True(t, GlobMatches("*cat", "foocat"))
	assert.False(t, GlobMatches("*cat", "catfoo"))

	// suffix glob
	assert.True(t, GlobMatches("cat*", "cat"))
	assert.False(t, GlobMatches("cat*", "foocat"))
	assert.True(t, GlobMatches("cat*", "catfoo"))
}
