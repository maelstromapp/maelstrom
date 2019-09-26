package maelstrom

import (
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	"github.com/stretchr/testify/assert"
	"math/rand"
	"sort"
	"testing"
)

func TestHttpEventSourceMatches(t *testing.T) {
	var tests = []struct {
		hostname   string
		pathprefix string
		reqhost    string
		reqpath    string
		matches    bool
	}{
		{"", "", "", "", false},
		{"foo.com", "", "foo.com", "", true},
		{"foo.com", "", "foo2.com", "", false},
		{"foo.com", "/foo", "foo.com", "", false},
		{"foo.com", "/foo", "foo.com", "/foo", true},
		{"foo.com", "/foo", "foo.com", "/foo/bar", true},
		{"foo.com", "/foo", "foo2.com", "/foo/bar", false},
	}
	for i, test := range tests {
		es := v1.EventSource{Http: &v1.HttpEventSource{
			Hostname:    test.hostname,
			PathPrefix:  test.pathprefix,
			StripPrefix: false,
		}}
		assert.Equal(t, test.matches, httpEventSourceMatches(es, test.reqhost, test.reqpath), "failed %d", i)
	}
}

func TestHttpEventSourceSorting(t *testing.T) {
	sources := []v1.EventSource{
		httpSource("foo.com", "/aaaa"),
		httpSource("bar.com", "/aaa"),
		httpSource("bar.com", "/aa"),
		httpSource("foo.com", "/"),
		httpSource("bar.com", "/"),
		httpSource("foo.com", ""),
		httpSource("bar.com", ""),
		httpSource("", "/aaaa"),
		httpSource("", "/aaa"),
		httpSource("", "/a"),
		httpSource("", ""),
	}
	expected := make([]v1.EventSource, len(sources))
	for i := 0; i < 20; i++ {
		copy(expected, sources)
		rand.Shuffle(len(sources), func(i, j int) { sources[i], sources[j] = sources[j], sources[i] })
		sort.Sort(httpEventSourcesForResolver(sources))
		assert.Equal(t, expected, sources)
	}
}

func httpSource(hostname string, pathPrefix string) v1.EventSource {
	return v1.EventSource{
		Http: &v1.HttpEventSource{
			Hostname:    hostname,
			PathPrefix:  pathPrefix,
			StripPrefix: false,
		},
	}
}
