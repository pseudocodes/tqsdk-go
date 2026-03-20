package store

import "reflect"

type ApplyResult struct {
	Changed bool
}

func DeepMerge(dst map[string]any, diff map[string]any) bool {
	changed := false
	for k, v := range diff {
		if v == nil {
			if _, ok := dst[k]; ok {
				delete(dst, k)
				changed = true
			}
			continue
		}
		vm, vok := v.(map[string]any)
		if !vok {
			if !reflect.DeepEqual(dst[k], v) {
				dst[k] = v
				changed = true
			}
			continue
		}
		dm, dok := dst[k].(map[string]any)
		if !dok {
			dm = map[string]any{}
			dst[k] = dm
			changed = true
		}
		if DeepMerge(dm, vm) {
			changed = true
		}
	}
	return changed
}

func CloneMap(src map[string]any) map[string]any {
	out := make(map[string]any, len(src))
	for k, v := range src {
		if m, ok := v.(map[string]any); ok {
			out[k] = CloneMap(m)
			continue
		}
		out[k] = v
	}
	return out
}

func GetMap(root map[string]any, path ...string) map[string]any {
	cur := root
	for _, p := range path {
		nextAny, ok := cur[p]
		if !ok {
			return nil
		}
		next, ok := nextAny.(map[string]any)
		if !ok {
			return nil
		}
		cur = next
	}
	return cur
}

func EnsureMap(root map[string]any, path ...string) map[string]any {
	cur := root
	for _, p := range path {
		nextAny, ok := cur[p]
		if !ok {
			next := map[string]any{}
			cur[p] = next
			cur = next
			continue
		}
		next, ok := nextAny.(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	return cur
}
