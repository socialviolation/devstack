package cmd

import (
	"strings"
	"testing"
)

func TestLocateServiceBlock(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		svc       string
		wantFound bool
	}{
		{
			name: "block with comment marker",
			content: `
# navexa-api
local_resource(
    "navexa-api",
    serve_cmd="dotnet run",
)
`,
			svc:       "navexa-api",
			wantFound: true,
		},
		{
			name: "block without comment marker (user-edited)",
			content: `local_resource(
    "navexa-api",
    serve_cmd="dotnet run",
)
`,
			svc:       "navexa-api",
			wantFound: true,
		},
		{
			name: "block absent",
			content: `local_resource(
    "other",
    serve_cmd="echo",
)
`,
			svc:       "navexa-api",
			wantFound: false,
		},
		{
			name: "name appears only inside another resource's body — not a match",
			content: `local_resource(
    "other",
    serve_env={"LINK": "navexa-api",},
)
`,
			svc:       "navexa-api",
			wantFound: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			start, blockStart := locateServiceBlock(tc.content, tc.svc)
			if tc.wantFound {
				if start == -1 || blockStart == -1 {
					t.Fatalf("expected to locate block, got start=%d blockStart=%d", start, blockStart)
				}
				if blockStart < start {
					t.Fatalf("blockStart (%d) must be >= start (%d)", blockStart, start)
				}
			} else {
				if start != -1 {
					t.Fatalf("expected no match, got start=%d", start)
				}
			}
		})
	}
}

func TestBuildTiltBlockProbeSyntax(t *testing.T) {
	out := buildTiltBlock("api", "dotnet run", "/svc", "dotnet", 8080, map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:4317",
		"OTEL_EXPORTER_OTLP_PROTOCOL": "grpc",
	})
	want := "readiness_probe=probe(http_get=http_get_action(port=8080)"
	if !strings.Contains(out, want) {
		t.Fatalf("generated block missing %q\n--- got ---\n%s", want, out)
	}
	bad := "probe(http_get_action("
	if strings.Contains(out, bad) {
		t.Fatalf("generated block contains broken probe syntax %q\n--- got ---\n%s", bad, out)
	}
}
