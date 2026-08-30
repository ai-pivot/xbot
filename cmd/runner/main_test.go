package main

import "testing"

// m8: the user ID auto-detected from the --server URL must be the last PATH
// segment only. The old strings.LastIndex("/") parsing kept the query string:
// "ws://host:8080/ws/admin?foo=bar" produced userID "admin?foo=bar".
func TestUserIDFromServerURL(t *testing.T) {
	cases := []struct {
		server string
		want   string
	}{
		{"ws://host:8080/ws/admin", "admin"},
		{"ws://host:8080/ws/admin?foo=bar", "admin"},
		{"ws://host:8080/ws/admin#frag", "admin"},
		{"ws://host:8080/ws/web-1?token=abc&x=1", "web-1"},
		{"wss://host/ws/cli_user", "cli_user"},
		{"ws://host:8080/", ""},
		{"ws://host:8080", ""},
		{"not a url at all", ""},
	}
	for _, c := range cases {
		if got := userIDFromServerURL(c.server); got != c.want {
			t.Errorf("userIDFromServerURL(%q) = %q, want %q", c.server, got, c.want)
		}
	}
}
