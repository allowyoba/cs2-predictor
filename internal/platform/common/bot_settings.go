package common

import "context"

// Settings that belong to the deployment rather than to a chat or a
// person: one answer, everywhere, set by whoever runs the bot.
//
// The distinction matters. A chat setting is something a room decides for
// itself; a personal setting is something somebody decides for their own
// screens. This is neither — it is how the product looks, and the same
// team has to look the same to everybody who sees it.

// BotSettingKey names one of them. The values are stored, so they stay
// stable.
type BotSettingKey string

const (
	// CrestSourceKey chooses which team crests the Mini App renders:
	// "hltv" or, for anything else including unset, the match provider's.
	// HLTV covers Counter-Strike only; every other game falls back to the
	// provider whatever this says.
	CrestSourceKey BotSettingKey = "crest_source"
)

// CrestSourceHLTV is the one value that means anything other than the
// default.
const CrestSourceHLTV = "hltv"

// BotSettings reads and writes them.
type BotSettings interface {
	// BotSetting returns the stored value, or "" when nothing has been
	// set — which every caller must read as "the default", not as an
	// error.
	BotSetting(ctx context.Context, key BotSettingKey) (string, error)
	// SetBotSetting records a value and who chose it.
	SetBotSetting(ctx context.Context, key BotSettingKey, value string, by UserID) error
}
