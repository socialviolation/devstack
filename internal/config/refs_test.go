package config

import "testing"

func testBook() PortBook {
	return PortBook{
		"api": {"http": 8080, "grpc": 9090},
		"web": {"http": 4200},
	}
}

func TestResolveRefsFields(t *testing.T) {
	book := testBook()
	cases := []struct {
		name string
		in   string
		self string
		want string
	}{
		{"port.http", "${api.port.http}", "web", "8080"},
		{"port other key", "${api.port.grpc}", "web", "9090"},
		{"host", "${api.host}", "web", "localhost"},
		{"url", "${api.url}", "web", "http://localhost:8080"},
		{"self port", "${self.port.http}", "web", "4200"},
		{"self url", "${self.url}", "api", "http://localhost:8080"},
		{"literal only", "http://static", "web", "http://static"},
		{"ref inside literal", "http://api:${api.port.http}/v1", "web", "http://api:8080/v1"},
		{"multiple refs", "${api.host}:${api.port.http} and ${web.url}", "api", "localhost:8080 and http://localhost:4200"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveRefs(tc.in, tc.self, book)
			if err != nil {
				t.Fatalf("ResolveRefs(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ResolveRefs(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestResolveRefsErrors(t *testing.T) {
	book := testBook()
	cases := []struct {
		name string
		in   string
		self string
	}{
		{"unknown service", "${ghost.port.http}", "web"},
		{"unknown port key", "${api.port.metrics}", "web"},
		{"url without http", "${nohttp.url}", "web"},
		{"self unknown key", "${self.port.http}", "nohttp"},
		{"malformed no field", "${api}", "web"},
		{"malformed empty", "${}", "web"},
		{"malformed too many parts", "${api.port.http.extra}", "web"},
		{"unclosed", "${api.port.http", "web"},
		{"bad field", "${api.portz}", "web"},
	}
	bookWithNoHTTP := PortBook{"nohttp": {"grpc": 9090}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := book
			if tc.self == "nohttp" || tc.name == "url without http" {
				b = bookWithNoHTTP
			}
			got, err := ResolveRefs(tc.in, tc.self, b)
			if err == nil {
				t.Fatalf("ResolveRefs(%q) = %q, want error", tc.in, got)
			}
			if got != "" {
				t.Errorf("ResolveRefs(%q) returned %q alongside error; must be empty", tc.in, got)
			}
		})
	}
}

// A resolved string must never carry an unresolved ${ downstream.
func TestResolveRefsNeverLeaksSigil(t *testing.T) {
	if _, err := ResolveRefs("ok ${api.port.http} ${broken", "web", testBook()); err == nil {
		t.Fatal("a trailing malformed ${ must fail, not pass through")
	}
}

func TestBuildPortBook(t *testing.T) {
	rw := &ResolvedWorkspace{Services: map[string]ResolvedService{
		"api":        {Name: "api", Manifest: &ServiceManifest{Ports: map[string]int{"http": 8080}}},
		"noports":    {Name: "noports", Manifest: &ServiceManifest{}},
		"nomanifest": {Name: "nomanifest"},
	}}
	book := BuildPortBook(rw)
	if book["api"]["http"] != 8080 {
		t.Errorf("api http port = %d, want 8080", book["api"]["http"])
	}
	if _, ok := book["noports"]; ok {
		t.Error("a service with no ports must not appear in the book")
	}
	if _, ok := book["nomanifest"]; ok {
		t.Error("a service with no manifest must not appear in the book")
	}
}
