package main

import (
	"github.com/SuInk/diana/model/assistant"
	"testing"
)

func TestFactorySharesOneBotConnectionRegardlessOfOrderAndSourceEnabled(t *testing.T) {
	for _, mode := range []string{assistant.OneBotTransportReverseWS, assistant.OneBotTransportForwardWS, assistant.OneBotTransportHTTP} {
		for _, sourceEnabled := range []bool{false, true} {
			t.Run(mode+map[bool]string{true: "/enabled", false: "/disabled"}[sourceEnabled], func(t *testing.T) {
				source := assistant.DefaultBotConfig()
				source.ID, source.Enabled, source.OneBotTransport = "source", sourceEnabled, mode
				source.OneBotAccessToken = "source-access-token"
				source.OneBotHTTPURL, source.OneBotHTTPSecret = "http://localhost:3000", "http-secret"
				source.OneBotWSEndpoint = "ws://localhost:3001"
				child := assistant.DefaultBotConfig()
				child.ID, child.Enabled, child.ConnectionProfileID = "child", true, source.ID
				for _, profiles := range [][]assistant.BotConfig{{child, source}, {source, child}} {
					server := assistant.NewOneBotReverseServer(assistant.OneBotConfig{})
					channel := newBotChannelSetFactory(server, &forwardWSOriginTracker{})(assistant.ProfileSet{ActiveID: child.ID, Profiles: profiles}).(*assistant.MultiChannel)
					want := 1
					if sourceEnabled {
						want = 2
					}
					statuses := channel.ChannelStatuses()
					if len(statuses) != want {
						t.Fatalf("got %d statuses, want %d", len(statuses), want)
					}
					for _, status := range statuses {
						if !status.AccessTokenConfigured {
							t.Fatal("source credentials not used")
						}
					}
				}
			})
		}
	}
}
