// Package warmcache answers a value it computed before from its cache.
package warmcache

var cache = map[int]int{}

// Double is twice n; a second call for n reads the cache.
func Double(n int) int {
	if v, ok := cache[n]; ok {
		return v * 2
	}
	cache[n] = n

	return n * 2
}
