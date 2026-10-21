package app

import (
	"regexp"
	"runtime"
	"strconv"
)

var releaseVersionPattern = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

// parseRelease reads a release tag such as v1.2.3. Development builds (dev,
// a commit hash, v1.2.3-4-gabc-dirty) have no place in the release order.
func parseRelease(v string) ([3]int, bool) {
	m := releaseVersionPattern.FindStringSubmatch(v)
	if m == nil {
		return [3]int{}, false
	}
	var out [3]int
	for i := range out {
		out[i], _ = strconv.Atoi(m[i+1])
	}
	return out, true
}

// compareRelease orders two release tags: -1 when a is older, 0 when equal, 1
// when newer. ok is false when either is not a release.
func compareRelease(a, b string) (order int, ok bool) {
	pa, okA := parseRelease(a)
	pb, okB := parseRelease(b)
	if !okA || !okB {
		return 0, false
	}
	for i := range pa {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

func localPlatform() string { return runtime.GOOS + "/" + runtime.GOARCH }
