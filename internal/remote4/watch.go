package remote4

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Watch polls dir for changes to Lua sources and calls onChange (debounced)
// from its own goroutine until ctx ends. Polling rather than fsnotify: the
// tree is small, 500ms is well inside the feedback loop, and it needs no
// platform-specific dependency.
func Watch(ctx context.Context, dir string, interval time.Duration, onChange func(changed []string)) {
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	last := snapshotTree(dir)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cur := snapshotTree(dir)
			if changed := diffTree(last, cur); len(changed) > 0 {
				// Editors write in bursts (save, then rename); let the burst settle.
				time.Sleep(150 * time.Millisecond)
				cur = snapshotTree(dir)
				changed = diffTree(last, cur)
				last = cur
				if len(changed) > 0 {
					onChange(changed)
				}
			}
		}
	}
}

type fileStamp struct {
	mod  time.Time
	size int64
}

func snapshotTree(dir string) map[string]fileStamp {
	out := map[string]fileStamp{}
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if path != dir && (strings.HasPrefix(name, ".") || name == "dist" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".lua") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		out[rel] = fileStamp{mod: info.ModTime(), size: info.Size()}
		return nil
	})
	return out
}

func diffTree(a, b map[string]fileStamp) []string {
	var changed []string
	for k, v := range b {
		if old, ok := a[k]; !ok || old != v {
			changed = append(changed, k)
		}
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			changed = append(changed, k)
		}
	}
	return changed
}
