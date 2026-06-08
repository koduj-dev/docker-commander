package docker

import (
	"context"
	"strings"
	"testing"
)

func TestCleanBuildCheck(t *testing.T) {
	raw := "#0 building with \"default\" instance using docker driver\n\n" +
		"#1 [internal] load build definition from Dockerfile\n" +
		"#1 transferring dockerfile: 109B done\n" +
		"#1 DONE 0.0s\n\n" +
		"#2 [internal] load metadata for docker.io/library/alpine:3.20\n" +
		"#2 DONE 1.2s\n" +
		"Check complete, 1 warning has been found!\n\n" +
		"WARNING: JSONArgsRecommended - https://example\n" +
		"JSON arguments recommended for CMD\n"
	got := cleanBuildCheck(raw)
	if strings.Contains(got, "#") {
		t.Errorf("BuildKit progress lines not stripped:\n%s", got)
	}
	if !strings.Contains(got, "Check complete, 1 warning") || !strings.Contains(got, "JSONArgsRecommended") {
		t.Errorf("check verdict/warning lost:\n%s", got)
	}
}

func TestDockerfileCheckIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("needs docker buildx")
	}
	ctx := context.Background()
	if !BuildCheckAvailable(ctx) {
		t.Skip("docker build --check unavailable")
	}
	if out, err := DockerfileCheck(ctx, "FROM alpine:3.20\nCMD [\"/bin/true\"]\n"); err != nil {
		t.Errorf("valid Dockerfile should pass: %v\n%s", err, out)
	}
	if _, err := DockerfileCheck(ctx, "FROM\n"); err == nil {
		t.Error("a parse error should fail the check")
	}
}
