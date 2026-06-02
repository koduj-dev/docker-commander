package monitor

import "testing"

func TestParseResourceDefaults(t *testing.T) {
	c, err := parseResource(`{"threshold":80}`)
	if err != nil {
		t.Fatal(err)
	}
	if c.Metric != "cpu" || c.Op != ">" || c.DurationSec != 30 {
		t.Errorf("defaults not applied: %+v", c)
	}
	if _, err := parseResource("not json"); err == nil {
		t.Error("invalid JSON should error")
	}
}

func TestResourceExceeds(t *testing.T) {
	up := resourceConfig{Op: ">", Threshold: 80}
	if !up.exceeds(81) || up.exceeds(80) || up.exceeds(10) {
		t.Error("> threshold logic wrong")
	}
	down := resourceConfig{Op: "<", Threshold: 20}
	if !down.exceeds(10) || down.exceeds(20) || down.exceeds(50) {
		t.Error("< threshold logic wrong")
	}
}

func TestParseStateAndMatches(t *testing.T) {
	c, err := parseState(`{"events":["die","oom"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !c.matches("die") || !c.matches("oom") || c.matches("start") {
		t.Errorf("state match wrong: %+v", c)
	}
}

func TestParseRestartDefaults(t *testing.T) {
	c, err := parseRestart(`{}`)
	if err != nil {
		t.Fatal(err)
	}
	if c.WindowSec != 60 || c.Count != 3 {
		t.Errorf("restart defaults: %+v", c)
	}
	c2, _ := parseRestart(`{"windowSec":120,"count":5}`)
	if c2.WindowSec != 120 || c2.Count != 5 {
		t.Errorf("restart parse: %+v", c2)
	}
}

func TestParseLogSubstringAndRegex(t *testing.T) {
	sub, err := parseLog(`{"pattern":"ERROR"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !sub.match("an error occurred") || sub.match("all good") { // case-insensitive substring
		t.Error("substring matcher wrong")
	}

	re, err := parseLog(`{"pattern":"pa[in]+c","isRegex":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if !re.match("kernel panic") || re.match("calm") {
		t.Error("regex matcher wrong")
	}

	if _, err := parseLog(`{"pattern":""}`); err == nil {
		t.Error("empty pattern should error")
	}
	if _, err := parseLog(`{"pattern":"(","isRegex":true}`); err == nil {
		t.Error("invalid regex should error")
	}
}

func TestMatchTarget(t *testing.T) {
	if !matchTarget("", "anything") || !matchTarget("*", "anything") {
		t.Error("blank/* should match all")
	}
	if !matchTarget("web", "my-web-1") {
		t.Error("substring should match")
	}
	if matchTarget("db", "my-web-1") {
		t.Error("non-matching substring should not match")
	}
}

func TestRuleKeyAndTruncate(t *testing.T) {
	if ruleKey(7, "abc") != ruleKey(7, "abc") {
		t.Error("ruleKey should be stable")
	}
	if ruleKey(7, "abc") == ruleKey(8, "abc") {
		t.Error("different rule ids should differ")
	}
	if truncate("hello", 10) != "hello" {
		t.Error("short string unchanged")
	}
	if got := truncate("hello world", 5); len(got) <= 5 || got[:5] != "hello" {
		t.Errorf("truncate kept prefix + marker, got %q", got)
	}
}

func TestEmailHelpers(t *testing.T) {
	if got := splitRecipients(" a@x.io, b@x.io ,, c@x.io"); len(got) != 3 {
		t.Errorf("splitRecipients trims + drops empties, got %v", got)
	}
	if splitRecipients("") != nil {
		t.Error("empty recipients → nil")
	}
	if shortID("0123456789abcdef") != "0123456789ab" {
		t.Error("shortID truncates to 12")
	}
	if shortID("short") != "short" {
		t.Error("shortID leaves short ids alone")
	}
	msg := string(buildMessage("from@x.io", "to@x.io", "Subj", "Body line\n"))
	for _, want := range []string{"From: from@x.io", "To: to@x.io", "Subject: Subj", "Body line"} {
		if !contains(msg, want) {
			t.Errorf("message missing %q", want)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
