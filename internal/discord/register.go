package discord

import "github.com/bwmarrin/discordgo"

const (
	reviewCommandName = "review"
	pingCommandName   = "ping"
)

// ReviewCommand returns the slash command definition.
func ReviewCommand() *discordgo.ApplicationCommand {
	return &discordgo.ApplicationCommand{
		Name:        reviewCommandName,
		Description: "Run an AI code review on a GitLab merge request",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "url",
				Description: "GitLab merge request URL",
				Required:    true,
			},
		},
	}
}

// PingCommand returns a lightweight liveness-check command. Replies with an
// ephemeral "pong" so users can confirm the bot is online without triggering
// a review job.
func PingCommand() *discordgo.ApplicationCommand {
	return &discordgo.ApplicationCommand{
		Name:        pingCommandName,
		Description: "Check whether the bot is online and responsive",
	}
}

// RegisterCommands installs (overwrites) the slash commands for the application.
// If guildID is non-empty, registration is scoped to that guild (faster rollout
// during development); otherwise commands are registered globally.
func RegisterCommands(s SessionAPI, appID, guildID string) error {
	_, err := s.ApplicationCommandBulkOverwrite(appID, guildID, []*discordgo.ApplicationCommand{
		ReviewCommand(),
		PingCommand(),
	})
	return err
}
