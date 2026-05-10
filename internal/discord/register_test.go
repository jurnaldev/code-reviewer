package discord

import (
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/stretchr/testify/require"
)

type recordingSession struct {
	fakeSession
	gotApp      string
	gotGuild    string
	gotCommands []*discordgo.ApplicationCommand
}

func (r *recordingSession) ApplicationCommandBulkOverwrite(appID, guildID string, cmds []*discordgo.ApplicationCommand, _ ...discordgo.RequestOption) ([]*discordgo.ApplicationCommand, error) {
	r.gotApp = appID
	r.gotGuild = guildID
	r.gotCommands = cmds
	return cmds, nil
}

func TestRegisterCommands_PassesAppGuildAndDefinition(t *testing.T) {
	s := &recordingSession{}
	require.NoError(t, RegisterCommands(s, "appid", "guildid"))
	require.Equal(t, "appid", s.gotApp)
	require.Equal(t, "guildid", s.gotGuild)
	require.Len(t, s.gotCommands, 2)
	names := []string{s.gotCommands[0].Name, s.gotCommands[1].Name}
	require.Contains(t, names, "review")
	require.Contains(t, names, "ping")
}
