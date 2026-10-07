package models

// CurationSettingsID is the fixed identifier of the single settings row.
const CurationSettingsID = "curation"

// CurationSettings are the policies curators work under.
type CurationSettings struct {
	Model

	// SpamRetentionHours is how long a dropped submission is kept before it is
	// purged for good.
	//
	// The classifier drops without a human, so this window is the only chance
	// anybody has to notice it was wrong. It is deliberately short: this is a
	// holding bin for sampling, not an archive of everything ever refused, and
	// the content in it was judged not to belong here.
	SpamRetentionHours int `json:"spam_retention_hours"`
}

// DefaultCurationSettings is what a fresh installation starts from.
func DefaultCurationSettings() CurationSettings {
	return CurationSettings{
		Model:              Model{ID: CurationSettingsID},
		SpamRetentionHours: 24,
	}
}
