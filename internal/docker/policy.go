package docker

import (
	"sort"
	"strings"
)

// PolicyRuleID identifies one deploy-time policy check. The string values are
// the wire/storage format — duplicated (not imported) in internal/store,
// since internal/store cannot import this package without creating a cycle
// (internal/docker already imports internal/store). A cross-package
// consistency test in internal/api keeps the two lists in sync.
type PolicyRuleID string

const (
	RulePrivileged         PolicyRuleID = "privileged"
	RuleHostNetwork        PolicyRuleID = "host_network"
	RuleHostPID            PolicyRuleID = "host_pid"
	RuleDockerSocket       PolicyRuleID = "docker_socket_mount"
	RuleLatestTag          PolicyRuleID = "latest_tag"
	RuleMissingLimits      PolicyRuleID = "missing_resource_limits"
	RuleMissingHealthcheck PolicyRuleID = "missing_healthcheck"
)

// AllPolicyRules lists every rule this engine knows how to evaluate, in a
// stable, deliberate order (roughly: privilege escalation, host-boundary
// breaks, socket access, then hygiene).
var AllPolicyRules = []PolicyRuleID{
	RulePrivileged,
	RuleHostNetwork,
	RuleHostPID,
	RuleDockerSocket,
	RuleLatestTag,
	RuleMissingLimits,
	RuleMissingHealthcheck,
}

// PolicyMode is how a rule's violation is handled.
type PolicyMode string

const (
	ModeOff   PolicyMode = "off"
	ModeWarn  PolicyMode = "warn"
	ModeBlock PolicyMode = "block"
)

// PolicyViolation is one rule failing for one service.
type PolicyViolation struct {
	Rule    PolicyRuleID `json:"rule"`
	Service string       `json:"service"`
	Mode    PolicyMode   `json:"mode"`
	Detail  string       `json:"detail"`
}

// EvaluatePolicy checks the resolved compose config (as returned by
// ComposeConfigJSON) against modes, and returns one PolicyViolation per
// rule+service that fails a non-off rule. A rule missing from modes defaults
// to off — fail-safe, so an unrecognised/unset rule never silently blocks a
// deploy.
func EvaluatePolicy(configJSON []byte, modes map[PolicyRuleID]PolicyMode) ([]PolicyViolation, error) {
	cfg, err := parseComposeConfigDoc(configJSON)
	if err != nil {
		return nil, err
	}

	var out []PolicyViolation
	add := func(rule PolicyRuleID, service, detail string) {
		mode := modes[rule]
		if mode == "" {
			mode = ModeOff
		}
		if mode == ModeOff {
			return
		}
		out = append(out, PolicyViolation{Rule: rule, Service: service, Mode: mode, Detail: detail})
	}

	names := make([]string, 0, len(cfg.Services))
	for name := range cfg.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		svc := cfg.Services[name]
		if svc.Privileged {
			add(RulePrivileged, name, "runs as a privileged container")
		}
		if svc.NetworkMode == "host" {
			add(RuleHostNetwork, name, "uses the host's network namespace")
		}
		if svc.Pid == "host" {
			add(RuleHostPID, name, "uses the host's PID namespace")
		}
		for _, v := range svc.Volumes {
			if v.Type == "bind" && (strings.HasSuffix(v.Source, "docker.sock") || strings.HasSuffix(v.Target, "docker.sock")) {
				add(RuleDockerSocket, name, "mounts the Docker socket")
				break
			}
		}
		if isLatestTag(svc.Image) {
			add(RuleLatestTag, name, "image \""+svc.Image+"\" has no pinned tag or digest")
		}
		hasLimits := svc.Deploy.Resources.Limits.CPUs != 0 || svc.Deploy.Resources.Limits.Memory != 0 ||
			svc.CPUs != 0 || svc.MemLimit != 0
		if !hasLimits {
			add(RuleMissingLimits, name, "has no CPU or memory limit")
		}
		if svc.Healthcheck == nil || svc.Healthcheck.Disable {
			add(RuleMissingHealthcheck, name, "has no healthcheck")
		}
	}
	return out, nil
}

// isLatestTag reports whether ref is unpinned: no digest, and either no tag
// at all (which Docker resolves to "latest") or an explicit "latest" tag. A
// digest reference (ref@sha256:...) is always considered pinned, even if it
// also carries a "latest" tag.
func isLatestTag(ref string) bool {
	if ref == "" {
		return false
	}
	if strings.Contains(ref, "@") {
		return false
	}
	// The tag is whatever follows the last ':' after the last '/' — a port in
	// the registry host (e.g. "registry:5000/repo") must not be mistaken for
	// a tag separator.
	slash := strings.LastIndex(ref, "/")
	rest := ref
	if slash >= 0 {
		rest = ref[slash+1:]
	}
	colon := strings.LastIndex(rest, ":")
	if colon < 0 {
		return true // no tag at all -> resolves to :latest
	}
	return rest[colon+1:] == "latest"
}
