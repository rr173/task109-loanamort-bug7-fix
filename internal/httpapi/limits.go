package httpapi

import "net/http"

// requireNonNegativeQuery validates optional as-of query parameters at the
// edge while preserving the existing default of current-state reads.
func requireNonNegativeQuery(r *http.Request, key string) (int, bool) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return -1, true
	}
	n, err := parseIntStrict(v)
	return n, err == nil && n >= 0
}
