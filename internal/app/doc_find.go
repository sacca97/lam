package app

import (
	"path"
	"strings"
	"time"
)

const (
	findMaxDirs  = 200             // folders listed while looking for a file
	findMaxDepth = 5               // levels below a base
	findBudget   = 4 * time.Second // a host far away answers slowly: give up
)

// findDocument turns a path an agent printed into a file that exists. Agents
// name files relative to wherever they were working, which is not always the
// folder the session started in, so rel is looked for, in this order:
//
//  1. as it stands under each base (the session's folder, then its parents);
//  2. below the bases, breadth first, in the first folder whose path ends with
//     rel (a bare "main.pdf" finds paper/main.pdf).
//
// It reports false when nothing matches in the budget. src must be able to list
// folders (documentCompleter) for step 2; without that only step 1 runs.
func findDocument(src documentSource, bases []string, rel string) (string, bool) {
	rel = strings.TrimPrefix(path.Clean(rel), "./")
	if rel == "" || rel == "." || path.IsAbs(rel) {
		return "", false
	}
	exists := func(p string) bool {
		_, _, _, err := src.ReadFileChunk(p, 0, 1)
		return err == nil
	}
	for _, b := range bases {
		for d, n := b, 0; d != "" && n < 4; n++ {
			if p := path.Join(d, rel); exists(p) {
				return p, true
			}
			parent := path.Dir(d)
			if parent == d || parent == "/" {
				break
			}
			d = parent
		}
	}
	lister, ok := src.(documentCompleter)
	if !ok {
		return "", false
	}
	deadline := time.Now().Add(findBudget)
	type node struct {
		dir   string
		depth int
	}
	var queue []node
	seen := map[string]bool{}
	for _, b := range bases {
		if b != "" && !seen[b] {
			seen[b] = true
			queue = append(queue, node{b, 0})
		}
	}
	for listed := 0; len(queue) > 0 && listed < findMaxDirs && time.Now().Before(deadline); listed++ {
		n := queue[0]
		queue = queue[1:]
		matches, err := lister.CompleteDocumentPath(n.dir+"/", n.dir)
		if err != nil {
			continue
		}
		var dirs []string
		for _, m := range matches {
			if strings.HasSuffix(m, "/") {
				name := path.Base(strings.TrimSuffix(m, "/"))
				if n.depth < findMaxDepth && name != "node_modules" && name != "vendor" {
					dirs = append(dirs, strings.TrimSuffix(m, "/"))
				}
				continue
			}
			if m == path.Join(n.dir, rel) || strings.HasSuffix(m, "/"+rel) {
				return m, true
			}
		}
		for _, d := range dirs {
			if !seen[d] {
				seen[d] = true
				queue = append(queue, node{d, n.depth + 1})
			}
		}
	}
	return "", false
}
