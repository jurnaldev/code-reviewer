package discord

import "github.com/bwmarrin/discordgo"

// SessionAPI is the subset of *discordgo.Session the bot uses.
// A real *discordgo.Session satisfies this interface (compile-time checked below).
type SessionAPI interface {
	InteractionRespond(i *discordgo.Interaction, r *discordgo.InteractionResponse, options ...discordgo.RequestOption) error
	InteractionResponseEdit(i *discordgo.Interaction, r *discordgo.WebhookEdit, options ...discordgo.RequestOption) (*discordgo.Message, error)
	ApplicationCommandBulkOverwrite(appID, guildID string, commands []*discordgo.ApplicationCommand, options ...discordgo.RequestOption) ([]*discordgo.ApplicationCommand, error)
}

var _ SessionAPI = (*discordgo.Session)(nil)
