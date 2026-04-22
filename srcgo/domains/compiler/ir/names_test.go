package ir

import "testing"

// TestCamelToSnake_Acronyms pins the D21 algorithm against the acronym
// pitfalls other implementations get wrong. The common bug is emitting
// `create_p_r` for `CreatePR` — a naive "insert _ before every
// uppercase" rule over-splits trailing acronyms. Our algorithm only
// splits an acronym when the NEXT rune is lowercase (the
// acronym-then-word boundary in `URLParser` → `url_parser`), leaving
// trailing acronyms contiguous.
func TestCamelToSnake_Acronyms(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// Baseline CamelCase.
		{"Product", "product"},
		{"ProductCategory", "product_category"},
		{"User", "user"},
		{"A", "a"},
		{"", ""},
		// Trailing acronym — bug case from D21 review. Must stay
		// contiguous, not split into per-letter segments.
		{"CreatePR", "create_pr"},
		{"IOError", "io_error"},
		// Mixed word + trailing acronym.
		{"CreatePROrder", "create_pr_order"},
		{"JSONToXML", "json_to_xml"},
		// Leading acronym, word follows — acronym-then-word boundary.
		{"URLParser", "url_parser"},
		{"HTTPServer", "http_server"},
		{"DashboardURLField", "dashboard_url_field"},
		// Fully-acronym name.
		{"API", "api"},
		// Single-letter word after acronym — documents the known
		// asymmetry: O+Auth parses as two words because "Auth" starts
		// a new CamelCase word (lowercase follows). Alternative
		// spellings (`OauthToken`, explicit `name:` override) avoid
		// the hyphenation.
		{"OAuthToken", "o_auth_token"},
	}
	for _, c := range cases {
		got := camelToSnake(c.in)
		if got != c.want {
			t.Errorf("camelToSnake(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
