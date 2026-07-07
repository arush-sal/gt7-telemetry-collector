package main

import (
	"go/format"
	"strings"
	"testing"
)

func TestEmitSourceIsValidAndSorted(t *testing.T) {
	names := map[int32]string{104: "Nissan Sileighty", 24: "Foo", 296: "Bar"}

	src, err := emitSource(names)
	if err != nil {
		t.Fatalf("emitSource() error: %v", err)
	}

	// Output must be valid, gofmt-clean Go source.
	if _, err := format.Source(src); err != nil {
		t.Fatalf("emitSource() produced invalid Go: %v\n%s", err, src)
	}

	// Deterministic generation: entries must be ordered by ascending numeric id (24, 104, 296),
	// not by map iteration order.
	s := string(src)
	i24, i104, i296 := strings.Index(s, "24:"), strings.Index(s, "104:"), strings.Index(s, "296:")
	if i24 < 0 || i24 >= i104 || i104 >= i296 {
		t.Errorf("entries not sorted by id: idx 24=%d 104=%d 296=%d", i24, i104, i296)
	}

	// Must be marked generated so tooling and reviewers skip it.
	if !strings.HasPrefix(s, "// Code generated") {
		t.Errorf("missing generated-code marker; got first line %q", strings.SplitN(s, "\n", 2)[0])
	}
}
