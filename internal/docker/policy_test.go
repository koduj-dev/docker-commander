package docker

import (
	"encoding/json"
	"testing"
)

func allModes(mode PolicyMode) map[PolicyRuleID]PolicyMode {
	m := make(map[PolicyRuleID]PolicyMode, len(AllPolicyRules))
	for _, r := range AllPolicyRules {
		m[r] = mode
	}
	return m
}

func hasViolation(violations []PolicyViolation, rule PolicyRuleID, service string) bool {
	for _, v := range violations {
		if v.Rule == rule && v.Service == service {
			return true
		}
	}
	return false
}

func TestEvaluatePolicy_Privileged(t *testing.T) {
	cfg := `{"services":{"web":{"image":"nginx:1.27","privileged":true}}}`
	v, err := EvaluatePolicy([]byte(cfg), allModes(ModeBlock))
	if err != nil {
		t.Fatal(err)
	}
	if !hasViolation(v, RulePrivileged, "web") {
		t.Errorf("expected a privileged violation, got %+v", v)
	}
}

func TestEvaluatePolicy_HostNetwork(t *testing.T) {
	cfg := `{"services":{"web":{"image":"nginx:1.27","network_mode":"host"}}}`
	v, _ := EvaluatePolicy([]byte(cfg), allModes(ModeBlock))
	if !hasViolation(v, RuleHostNetwork, "web") {
		t.Errorf("expected a host_network violation, got %+v", v)
	}
}

func TestEvaluatePolicy_HostPID(t *testing.T) {
	cfg := `{"services":{"web":{"image":"nginx:1.27","pid":"host"}}}`
	v, _ := EvaluatePolicy([]byte(cfg), allModes(ModeBlock))
	if !hasViolation(v, RuleHostPID, "web") {
		t.Errorf("expected a host_pid violation, got %+v", v)
	}
}

func TestEvaluatePolicy_DockerSocketMount(t *testing.T) {
	cfg := `{"services":{"web":{"image":"nginx:1.27","volumes":[
		{"type":"bind","source":"/var/run/docker.sock","target":"/var/run/docker.sock"}
	]}}}`
	v, _ := EvaluatePolicy([]byte(cfg), allModes(ModeBlock))
	if !hasViolation(v, RuleDockerSocket, "web") {
		t.Errorf("expected a docker_socket_mount violation, got %+v", v)
	}
}

func TestEvaluatePolicy_DockerSocketMount_OrdinaryBindVolumeIsFine(t *testing.T) {
	cfg := `{"services":{"web":{"image":"nginx:1.27","volumes":[
		{"type":"bind","source":"/data","target":"/data"}
	]}}}`
	v, _ := EvaluatePolicy([]byte(cfg), allModes(ModeBlock))
	if hasViolation(v, RuleDockerSocket, "web") {
		t.Errorf("an ordinary bind mount must not trigger docker_socket_mount, got %+v", v)
	}
}

func TestEvaluatePolicy_LatestTagVariants(t *testing.T) {
	cases := []struct {
		image      string
		wantLatest bool
	}{
		{"nginx", true},
		{"nginx:latest", true},
		{"nginx:1.27", false},
		{"registry:5000/repo", true},
		{"registry:5000/repo:v1", false},
		{"repo@sha256:abcd1234", false},
		{"registry:5000/repo@sha256:abcd1234", false},
	}
	for _, c := range cases {
		body := map[string]any{"services": map[string]any{"web": map[string]any{"image": c.image}}}
		b, _ := json.Marshal(body)
		v, err := EvaluatePolicy(b, allModes(ModeBlock))
		if err != nil {
			t.Fatalf("image %q: %v", c.image, err)
		}
		got := hasViolation(v, RuleLatestTag, "web")
		if got != c.wantLatest {
			t.Errorf("image %q: latest_tag violation = %v, want %v", c.image, got, c.wantLatest)
		}
	}
}

func TestEvaluatePolicy_MissingResourceLimits(t *testing.T) {
	cfg := `{"services":{"web":{"image":"nginx:1.27"}}}`
	v, _ := EvaluatePolicy([]byte(cfg), allModes(ModeBlock))
	if !hasViolation(v, RuleMissingLimits, "web") {
		t.Errorf("expected a missing_resource_limits violation, got %+v", v)
	}
}

func TestEvaluatePolicy_PartialResourceLimitDoesNotTrigger(t *testing.T) {
	cfg := `{"services":{"web":{"image":"nginx:1.27","deploy":{"resources":{"limits":{"cpus":"0.5"}}}}}}`
	v, _ := EvaluatePolicy([]byte(cfg), allModes(ModeBlock))
	if hasViolation(v, RuleMissingLimits, "web") {
		t.Errorf("a partial (CPU-only) limit must not trigger missing_resource_limits, got %+v", v)
	}
}

// TestEvaluatePolicy_ServiceLevelResourceLimitsSatisfyTheRule is the P2 fix:
// Compose's short syntax (`cpus:`/`mem_limit:` directly on the service) is a
// distinct field from the long `deploy.resources.limits` syntax the rule used
// to check exclusively. A service using only the short syntax has a real
// limit and must not be reported as missing one.
func TestEvaluatePolicy_ServiceLevelResourceLimitsSatisfyTheRule(t *testing.T) {
	cfg := `{"services":{"web":{"image":"nginx:1.27","cpus":"0.5","mem_limit":"64m"}}}`
	v, err := EvaluatePolicy([]byte(cfg), allModes(ModeBlock))
	if err != nil {
		t.Fatal(err)
	}
	if hasViolation(v, RuleMissingLimits, "web") {
		t.Errorf("service-level cpus/mem_limit must satisfy missing_resource_limits, got %+v", v)
	}
}

func TestEvaluatePolicy_ServiceLevelCPUsAloneSatisfiesTheRule(t *testing.T) {
	cfg := `{"services":{"web":{"image":"nginx:1.27","cpus":"0.5"}}}`
	v, _ := EvaluatePolicy([]byte(cfg), allModes(ModeBlock))
	if hasViolation(v, RuleMissingLimits, "web") {
		t.Errorf("a service-level cpus limit alone must satisfy missing_resource_limits, got %+v", v)
	}
}

func TestEvaluatePolicy_NoLimitsAtAllStillTriggers(t *testing.T) {
	cfg := `{"services":{"web":{"image":"nginx:1.27","cpus":"0","mem_limit":"0"}}}`
	v, _ := EvaluatePolicy([]byte(cfg), allModes(ModeBlock))
	if !hasViolation(v, RuleMissingLimits, "web") {
		t.Errorf("a zero-valued service-level limit must still count as missing, got %+v", v)
	}
}

// TestEvaluatePolicy_DisabledHealthcheckStillTriggers is the P2 fix: Compose
// resolves `healthcheck: { disable: true }` to a non-nil healthcheck object,
// so a nil check alone would let a deliberately-disabled healthcheck satisfy
// missing_healthcheck.
func TestEvaluatePolicy_DisabledHealthcheckStillTriggers(t *testing.T) {
	cfg := `{"services":{"web":{"image":"nginx:1.27","healthcheck":{"disable":true}}}}`
	v, _ := EvaluatePolicy([]byte(cfg), allModes(ModeBlock))
	if !hasViolation(v, RuleMissingHealthcheck, "web") {
		t.Errorf("a disabled healthcheck must still trigger missing_healthcheck, got %+v", v)
	}
}

func TestEvaluatePolicy_MissingHealthcheck(t *testing.T) {
	cfg := `{"services":{"web":{"image":"nginx:1.27"}}}`
	v, _ := EvaluatePolicy([]byte(cfg), allModes(ModeBlock))
	if !hasViolation(v, RuleMissingHealthcheck, "web") {
		t.Errorf("expected a missing_healthcheck violation, got %+v", v)
	}
}

func TestEvaluatePolicy_HealthcheckPresentDoesNotTrigger(t *testing.T) {
	cfg := `{"services":{"web":{"image":"nginx:1.27","healthcheck":{"test":["CMD","curl","-f","http://localhost"]}}}}`
	v, _ := EvaluatePolicy([]byte(cfg), allModes(ModeBlock))
	if hasViolation(v, RuleMissingHealthcheck, "web") {
		t.Errorf("a declared healthcheck must not trigger missing_healthcheck, got %+v", v)
	}
}

func TestEvaluatePolicy_AllOffProducesNoViolations(t *testing.T) {
	cfg := `{"services":{"web":{"image":"nginx:latest","privileged":true,"network_mode":"host","pid":"host"}}}`
	v, err := EvaluatePolicy([]byte(cfg), allModes(ModeOff))
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 0 {
		t.Errorf("expected no violations with every rule off, got %+v", v)
	}
}

func TestEvaluatePolicy_UnknownRuleDefaultsOff(t *testing.T) {
	cfg := `{"services":{"web":{"image":"nginx:latest","privileged":true}}}`
	v, err := EvaluatePolicy([]byte(cfg), map[PolicyRuleID]PolicyMode{}) // no modes configured at all
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 0 {
		t.Errorf("a rule missing from the modes map must default to off, got %+v", v)
	}
}

func TestEvaluatePolicy_WarnModeIsReported(t *testing.T) {
	cfg := `{"services":{"web":{"image":"nginx:1.27","privileged":true}}}`
	v, _ := EvaluatePolicy([]byte(cfg), map[PolicyRuleID]PolicyMode{RulePrivileged: ModeWarn})
	if len(v) != 1 || v[0].Mode != ModeWarn {
		t.Errorf("expected exactly one warn-mode violation, got %+v", v)
	}
}

func TestEvaluatePolicy_MultipleServicesSortedDeterministically(t *testing.T) {
	cfg := `{"services":{
		"zeta":{"image":"nginx:1.27","privileged":true},
		"alpha":{"image":"nginx:1.27","privileged":true}
	}}`
	v, _ := EvaluatePolicy([]byte(cfg), allModes(ModeBlock))
	var services []string
	for _, x := range v {
		if x.Rule == RulePrivileged {
			services = append(services, x.Service)
		}
	}
	if len(services) != 2 || services[0] != "alpha" || services[1] != "zeta" {
		t.Errorf("expected violations sorted by service name [alpha zeta], got %v", services)
	}
}

func TestEvaluatePolicy_InvalidJSONReturnsError(t *testing.T) {
	if _, err := EvaluatePolicy([]byte("not json"), allModes(ModeBlock)); err == nil {
		t.Error("expected an error for invalid compose config JSON")
	}
}
