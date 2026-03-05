package utils

// MergeByKey appends items from additional into base when their key is not yet present.
func MergeByKey[T any, K comparable](base, additional []T, key func(T) K) []T {
	seen := make(map[K]struct{}, len(base))
	for _, item := range base {
		seen[key(item)] = struct{}{}
	}

	merged := append([]T{}, base...)
	for _, item := range additional {
		k := key(item)
		if _, exists := seen[k]; exists {
			continue
		}
		merged = append(merged, item)
		seen[k] = struct{}{}
	}

	return merged
}
