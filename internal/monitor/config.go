package monitor

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
)

// Rule config shapes. Each alert rule stores type-specific JSON in its Config
// column; these decode it.

// resourceConfig is a level-triggered threshold rule.
//
// Metric picks what the threshold is compared against, and the choice matters
// more than it looks:
//
//	cpu       percent of ONE core, the `docker stats` convention. A container
//	          busy on four cores reads ~400%, so a "> 80%" rule on a multi-core
//	          host is over threshold essentially always.
//	cpu_total percent of ALL the host's cores — 0–100% whatever the core count,
//	          which is what most people mean when they write "> 80%".
//	mem       percent of the container's memory LIMIT (not of host RAM).
//
// "cpu" stays the default so rules written before cpu_total existed keep the
// exact meaning they had.
type resourceConfig struct {
	Metric      string  `json:"metric"` // "cpu" | "cpu_total" | "mem"
	Op          string  `json:"op"`     // ">" | "<"
	Threshold   float64 `json:"threshold"`
	DurationSec int     `json:"durationSec"`
}

// metricKey groups rules that talk about the same thing, so overlapping rules
// over one metric are one condition rather than competing alerts. Both CPU
// flavours describe the same underlying usage, so they share a key.
func (c resourceConfig) metricKey() string {
	if c.Metric == "cpu_total" {
		return "cpu"
	}
	return c.Metric
}

func (c resourceConfig) exceeds(v float64) bool {
	if c.Op == "<" {
		return v < c.Threshold
	}
	return v > c.Threshold
}

func parseResource(s string) (resourceConfig, error) {
	var c resourceConfig
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		return c, err
	}
	if c.Metric == "" {
		c.Metric = "cpu"
	}
	if c.Op == "" {
		c.Op = ">"
	}
	if c.DurationSec <= 0 {
		c.DurationSec = 30
	}
	return c, nil
}

type stateConfig struct {
	Events []string `json:"events"` // docker event actions: "die","kill","oom","stop","health_status: unhealthy"
}

func (c stateConfig) matches(action string) bool {
	for _, e := range c.Events {
		if e == action {
			return true
		}
	}
	return false
}

func parseState(s string) (stateConfig, error) {
	var c stateConfig
	err := json.Unmarshal([]byte(s), &c)
	return c, err
}

type restartConfig struct {
	WindowSec int `json:"windowSec"`
	Count     int `json:"count"`
}

func parseRestart(s string) (restartConfig, error) {
	var c restartConfig
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		return c, err
	}
	if c.WindowSec <= 0 {
		c.WindowSec = 60
	}
	if c.Count <= 0 {
		c.Count = 3
	}
	return c, nil
}

type logRuleConfig struct {
	Pattern string `json:"pattern"`
	IsRegex bool   `json:"isRegex"`
}

func parseLog(s string) (logMatcher, error) {
	var c logRuleConfig
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		return logMatcher{}, err
	}
	if c.Pattern == "" {
		return logMatcher{}, fmt.Errorf("empty log pattern")
	}
	if c.IsRegex {
		re, err := regexp.Compile(c.Pattern)
		if err != nil {
			return logMatcher{}, err
		}
		return logMatcher{re: re}, nil
	}
	return logMatcher{substr: c.Pattern}, nil
}

// small formatting helpers used by the engine
func sprintf(format string, a ...any) string { return fmt.Sprintf(format, a...) }
func itoa(n int64) string                    { return strconv.FormatInt(n, 10) }
