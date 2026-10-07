package models

import "testing"

// TestZeroThresholdIsUnsetNotPermissive is a regression test for a bug that
// left no trace anywhere.
//
// SubjectSuggestCrossLanguage was added to this struct and forgotten in the
// configuration path, so it was stored as zero. Nothing failed, nothing logged
// — but "suggest a merge for anything scoring above 0" turned every pair of
// subjects into a question for a curator, including ones scoring 0.46.
//
// A similarity threshold of zero is always a missing setting, never a chosen
// one: choosing it would mean "merge everything", which nobody wants.
func TestZeroThresholdIsUnsetNotPermissive(t *testing.T) {
	defaults := DefaultLLMSettings()

	// The shape the bug produced: some fields written, one forgotten.
	stored := LLMSettings{
		SubjectMergeThreshold:   0.95,
		SubjectSuggestThreshold: 0.85,
		// SubjectSuggestCrossLanguage left at zero
	}

	repaired := stored.WithThresholdDefaults()

	if repaired.SubjectSuggestCrossLanguage != defaults.SubjectSuggestCrossLanguage {
		t.Errorf("cross-language threshold = %v, want the default %v — a zero "+
			"threshold suggests every pair of subjects",
			repaired.SubjectSuggestCrossLanguage, defaults.SubjectSuggestCrossLanguage)
	}
	// Deliberate values survive.
	if repaired.SubjectMergeThreshold != 0.95 || repaired.SubjectSuggestThreshold != 0.85 {
		t.Error("a threshold that was actually set was overwritten")
	}
}

func TestEveryThresholdIsRepaired(t *testing.T) {
	repaired := LLMSettings{}.WithThresholdDefaults()

	if repaired.SubjectMergeThreshold <= 0 ||
		repaired.SubjectSuggestThreshold <= 0 ||
		repaired.SubjectSuggestCrossLanguage <= 0 {
		t.Errorf("an empty settings row left a threshold at zero: %+v", repaired)
	}
	if repaired.EmbeddingModel == "" {
		t.Error("an empty settings row left no embedding model, which disables the layer silently")
	}

	// Negative values are just as wrong and just as silent.
	negative := LLMSettings{SubjectMergeThreshold: -1}.WithThresholdDefaults()
	if negative.SubjectMergeThreshold <= 0 {
		t.Error("a negative threshold was kept")
	}
}

// TestSwitchingALayerOffIsPossible: an operator who genuinely wants no
// automatic merging needs a way to say so that is not zero.
func TestSwitchingALayerOffIsPossible(t *testing.T) {
	off := LLMSettings{SubjectMergeThreshold: 2}.WithThresholdDefaults()

	if off.SubjectMergeThreshold != 2 {
		t.Errorf("merge threshold = %v, want 2 — a value above 1 disables the "+
			"layer and must be respected", off.SubjectMergeThreshold)
	}
}

// TestGroupThresholdsAreNeverZero. They were added after the settings row
// existed, so an installation that predates them holds zeros — and zero as an
// accept threshold means "publish everything", which is the one failure a
// missing setting must never look like.
func TestGroupThresholdsAreNeverZero(t *testing.T) {
	filled := LLMSettings{}.WithThresholdDefaults()

	if filled.GroupAcceptThreshold <= 0 || filled.GroupCurateThreshold <= 0 {
		t.Errorf("a settings row with no group thresholds stayed at zero: %+v", filled)
	}
	// And the accept bar must sit above the curate bar, or nothing reaches a
	// human.
	if filled.GroupAcceptThreshold <= filled.GroupCurateThreshold {
		t.Errorf("accept %d is not above curate %d",
			filled.GroupAcceptThreshold, filled.GroupCurateThreshold)
	}
	// Lower than the message bar, deliberately: a group is judged on far less
	// and a wrongly refused one is a meeting nobody hears about.
	if filled.GroupAcceptThreshold >= filled.AcceptThreshold {
		t.Errorf("group accept %d is not below the message bar %d",
			filled.GroupAcceptThreshold, filled.AcceptThreshold)
	}

	// The message pair is filled too. Writing this test is what showed it was
	// not: an accept threshold of zero publishes everything and looks exactly
	// like a working register.
	if filled.AcceptThreshold <= 0 || filled.CurateThreshold <= 0 {
		t.Errorf("the message thresholds stayed at zero: %+v", filled)
	}

	// An operator's own values survive.
	chosen := LLMSettings{GroupAcceptThreshold: 95, GroupCurateThreshold: 70}.WithThresholdDefaults()
	if chosen.GroupAcceptThreshold != 95 || chosen.GroupCurateThreshold != 70 {
		t.Errorf("deliberate values were overwritten: %+v", chosen)
	}
}
