package store

import "testing"

func TestIgnoreCVEsAndList(t *testing.T) {
	s, ctx := openStore(t)

	if err := s.IgnoreCVEs(ctx, []string{"CVE-2024-1111", "CVE-2024-2222"}, "accepted risk, no fix available", "alice"); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListIgnoredCVEs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(list), list)
	}
	byID := map[string]IgnoredCVE{}
	for _, c := range list {
		byID[c.ID] = c
	}
	if got := byID["CVE-2024-1111"]; got.Reason != "accepted risk, no fix available" || got.AddedBy != "alice" {
		t.Errorf("CVE-2024-1111 = %+v", got)
	}
	if got := byID["CVE-2024-2222"]; got.AddedBy != "alice" {
		t.Errorf("CVE-2024-2222 = %+v", got)
	}
}

func TestIgnoreCVEs_BlankIDsAreDropped(t *testing.T) {
	s, ctx := openStore(t)
	if err := s.IgnoreCVEs(ctx, []string{"CVE-2024-1111", "", "   "}, "", "bob"); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListIgnoredCVEs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "CVE-2024-1111" {
		t.Errorf("list = %+v, want exactly one entry for CVE-2024-1111", list)
	}
}

func TestIgnoreCVEs_ResubmittingTheSameIDKeepsTheOriginal(t *testing.T) {
	s, ctx := openStore(t)
	if err := s.IgnoreCVEs(ctx, []string{"CVE-2024-1111"}, "first reason", "alice"); err != nil {
		t.Fatal(err)
	}
	// A second bulk submission naming the same CVE (e.g. re-selecting rows
	// across two scans) must not silently overwrite who accepted it or why.
	if err := s.IgnoreCVEs(ctx, []string{"CVE-2024-1111"}, "different reason", "bob"); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListIgnoredCVEs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d entries, want 1 (no duplicate row): %+v", len(list), list)
	}
	if list[0].Reason != "first reason" || list[0].AddedBy != "alice" {
		t.Errorf("entry = %+v, want the original attribution preserved", list[0])
	}
}

func TestUnignoreCVE(t *testing.T) {
	s, ctx := openStore(t)
	if err := s.IgnoreCVEs(ctx, []string{"CVE-2024-1111", "CVE-2024-2222"}, "", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := s.UnignoreCVE(ctx, "CVE-2024-1111"); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListIgnoredCVEs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "CVE-2024-2222" {
		t.Errorf("list = %+v, want only CVE-2024-2222 left", list)
	}
	// Unignoring an id that was never ignored (or already removed) is a no-op,
	// not an error — the caller's goal ("this id should not be ignored") is
	// already true.
	if err := s.UnignoreCVE(ctx, "CVE-does-not-exist"); err != nil {
		t.Errorf("unignoring an unknown id should not error: %v", err)
	}
}
