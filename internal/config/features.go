package config

import (
	"github.com/CoderSyndicate/doleances/internal/content"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// KeyDisableGroups switches off local action groups and their actions.
const KeyDisableGroups = "disable-groups"

// RegisterFeatureFlags declares what a deployment can switch off.
//
// Groups are the only such switch, and they earn it by costing something the
// rest of the register does not: a group is created by proving its contact
// address answers, which means a mail server, which a doléance-only
// installation would otherwise have to run and never use.
//
// Named for what it does rather than what it enables — `--disable-groups` is
// read correctly by somebody who has never seen this file, where
// `--groups=false` invites a moment's arithmetic about defaults.
func RegisterFeatureFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().Bool(KeyDisableGroups, false,
		"switch off local action groups and their actions, and with them the need for a mail server")
}

// GroupsEnabled reports whether this deployment offers groups.
func GroupsEnabled() bool {
	return !viper.GetBool(KeyDisableGroups)
}

// KeyExcerptRunes is how much of a doléance a card shows.
const KeyExcerptRunes = "card-excerpt-runes"

// RegisterCardFlags declares the frontend's presentation settings.
//
// A card's length is a judgement about a shape on a screen rather than a fact,
// so it is configuration like the classifier thresholds: an operator whose
// readers are on phones, or whose register runs to very long submissions, will
// want a different number from the one that suited the corpus.
func RegisterCardFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().Int(KeyExcerptRunes, content.DefaultExcerptRunes,
		"how many characters of a doléance a card shows before it is cut")
}

// LoadExcerptRunes reads it.
//
// A value that was never written reads as zero, and zero here would show empty
// cards — so it falls back rather than being taken at its word. The same rule
// the LLM thresholds follow, learned the same way.
func LoadExcerptRunes() int {
	runes := viper.GetInt(KeyExcerptRunes)
	if runes <= 0 {
		return content.DefaultExcerptRunes
	}
	return runes
}
