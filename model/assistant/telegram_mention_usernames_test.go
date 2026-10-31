package assistant

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func telegramMentionTestUsername(value string) *string { return &value }

func telegramMentionTestEvent(userID string, timestamp int64, username *string) MessageEvent {
	return MessageEvent{
		Platform: PlatformTelegram, ProfileID: "telegram-test", SelfID: "90001",
		Kind: EventKindGroup, GroupID: "-10001", UserID: userID, Time: timestamp,
		SenderMentionUsername: username,
	}
}

func TestTelegramMentionUsernamesLatestDirectObservation(t *testing.T) {
	current := telegramMentionTestEvent("10001", 20, telegramMentionTestUsername("CurrentName"))
	tests := []struct {
		name    string
		current MessageEvent
		history []MessageEvent
		want    string
	}{
		{"current wins equal timestamp", current, []MessageEvent{telegramMentionTestEvent("10001", 20, telegramMentionTestUsername("PreviousName"))}, "CurrentName"},
		{"history newer than current", current, []MessageEvent{telegramMentionTestEvent("10001", 30, telegramMentionTestUsername("FutureName"))}, "FutureName"},
		{"out of order history", telegramMentionTestEvent("10001", 10, nil), []MessageEvent{
			telegramMentionTestEvent("10001", 30, telegramMentionTestUsername("NewestName")),
			telegramMentionTestEvent("10001", 20, telegramMentionTestUsername("OlderName")),
		}, "NewestName"},
		{"nil is unknown", telegramMentionTestEvent("10001", 40, nil), []MessageEvent{current}, "CurrentName"},
		{"explicit empty clears older", telegramMentionTestEvent("10001", 40, telegramMentionTestUsername("")), []MessageEvent{current}, ""},
		{"newer empty in history clears current", current, []MessageEvent{telegramMentionTestEvent("10001", 40, telegramMentionTestUsername(""))}, ""},
		{"empty current wins tie", telegramMentionTestEvent("10001", 20, telegramMentionTestUsername("")), []MessageEvent{current}, ""},
		{"conflicting history timestamp falls back", telegramMentionTestEvent("10001", 40, nil), []MessageEvent{
			telegramMentionTestEvent("10001", 30, telegramMentionTestUsername("FirstName")),
			telegramMentionTestEvent("10001", 30, telegramMentionTestUsername("OtherName")),
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveTelegramMentionUsernames(tt.current, tt.history)["10001"]; got != tt.want {
				t.Fatalf("username = %q, want %q", got, tt.want)
			}
			for i, j := 0, len(tt.history)-1; i < j; i, j = i+1, j-1 {
				tt.history[i], tt.history[j] = tt.history[j], tt.history[i]
			}
			if got := resolveTelegramMentionUsernames(tt.current, tt.history)["10001"]; got != tt.want {
				t.Fatalf("reversed history username = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTelegramMentionUsernamesEditedDirectObservation(t *testing.T) {
	for _, username := range []string{"", "RenamedUser"} {
		t.Run("username="+username, func(t *testing.T) {
			current := telegramMentionTestEvent("10001", 40, nil)
			edited := telegramMentionTestEvent("10002", 10, telegramMentionTestUsername(username))
			edited.Edited, edited.EditTime = true, 30
			older := telegramMentionTestEvent("10002", 20, telegramMentionTestUsername("OldName"))
			for _, history := range [][]MessageEvent{{edited, older}, {older, edited}} {
				if got := resolveTelegramMentionUsernames(current, history)["10002"]; got != username {
					t.Fatalf("edited username = %q, want %q", got, username)
				}
			}
			edited.Time, edited.EditTime = 30, 10
			if got := resolveTelegramMentionUsernames(current, []MessageEvent{edited, older})["10002"]; got != username {
				t.Fatalf("older edit timestamp regressed observation: %q", got)
			}
		})
	}
}

func TestTelegramMentionUsernamesEditTimeDoesNotRefreshIndirectObservation(t *testing.T) {
	current := telegramMentionTestEvent("10001", 40, nil)
	edited := telegramMentionTestEvent("10002", 10, nil)
	edited.Edited, edited.EditTime = true, 30
	edited.Quoted = &QuotedMessage{UserID: "10003", SenderMentionUsername: telegramMentionTestUsername("OldQuoted")}
	edited.MentionTargets = []MessageMention{{UserID: "10004", Username: "OldMention", UsernameKnown: true}}
	newer := telegramMentionTestEvent("10002", 20, nil)
	newer.Quoted = &QuotedMessage{UserID: "10003", SenderMentionUsername: telegramMentionTestUsername("NewQuoted")}
	newer.MentionTargets = []MessageMention{{UserID: "10004", Username: "NewMention", UsernameKnown: true}}
	want := map[string]string{"10003": "NewQuoted", "10004": "NewMention"}
	if got := resolveTelegramMentionUsernames(current, []MessageEvent{edited, newer}); !reflect.DeepEqual(got, want) {
		t.Fatalf("edit refreshed indirect observations: %#v, want %#v", got, want)
	}
}

func TestTelegramMentionUsernamesEditedMessageMapping(t *testing.T) {
	for _, username := range []string{"", "RenamedUser"} {
		t.Run("username="+username, func(t *testing.T) {
			channel := NewTelegramChannel(TelegramConfig{})
			var edited MessageEvent
			channel.handler = func(_ context.Context, event MessageEvent) error {
				edited = event
				return nil
			}
			update := telegramUpdate{UpdateID: 10005, EditedMessage: &telegramMessage{
				MessageID: 10003, Date: 10, EditDate: 30, Text: "edited",
				Chat: &telegramChat{ID: -10001, Type: "supergroup"},
				From: &telegramUser{ID: 10002, Username: username},
			}}
			if err := channel.dispatch(context.Background(), update); err != nil {
				t.Fatal(err)
			}
			if !edited.Edited || edited.Time != 10 || edited.EditTime != 30 || edited.SenderMentionUsername == nil || *edited.SenderMentionUsername != username {
				t.Fatalf("incorrect edited event: %+v", edited)
			}
			original := edited
			original.Edited, original.EditTime = false, 0
			original.SenderMentionUsername = telegramMentionTestUsername("OldName")
			later := original
			later.MessageID, later.Time = "10004", 20
			current := edited
			current.UserID, current.Time = "10001", 40
			current.Edited, current.EditTime = false, 0
			current.SenderUsername, current.SenderMentionUsername = "", nil
			session := sessionKey(current)
			runtime := &Runtime{history: map[string][]MessageEvent{session: {original, later}}}
			runtime.refreshRememberedMessageEdit(edited, session)
			message := runtime.resolveOutgoingMentionNames(current, OutgoingMessage{Text: "<diana-at id=\"10002\"/>"})
			if got := message.MentionUsernames["10002"]; got != username {
				t.Fatalf("mapped edited username = %q, want %q", got, username)
			}
		})
	}
}

func TestTelegramMentionUsernamesLegacyAndIndirect(t *testing.T) {
	current := telegramMentionTestEvent("10001", 30, nil)
	current.SenderUsername = "LegacyName"
	if got := resolveTelegramMentionUsernames(current, nil)["10001"]; got != "LegacyName" {
		t.Fatalf("legacy username = %q", got)
	}
	current.SenderMentionUsername = telegramMentionTestUsername("")
	if got := resolveTelegramMentionUsernames(current, nil)["10001"]; got != "" {
		t.Fatalf("legacy username overrode known absent: %q", got)
	}
	current.Quoted = &QuotedMessage{UserID: "10001", SenderMentionUsername: telegramMentionTestUsername("QuotedName")}
	current.MentionTargets = []MessageMention{
		{UserID: "10001", Username: "MentionName", UsernameKnown: true},
		{UserID: "10002", Username: "OtherName", UsernameKnown: true},
		{UserID: "10003", UsernameKnown: true},
		{Username: "UnboundName", UsernameKnown: true},
		{UserID: "10005", Username: "UnknownName"},
	}
	got := resolveTelegramMentionUsernames(current, nil)
	if !reflect.DeepEqual(got, map[string]string{"10002": "OtherName"}) {
		t.Fatalf("indirect metadata overrode direct absence or invented ID: %#v", got)
	}
	current.Quoted = &QuotedMessage{UserID: "10004", SenderMentionUsername: telegramMentionTestUsername("QuotedName")}
	if got := resolveTelegramMentionUsernames(current, nil)["10004"]; got != "QuotedName" {
		t.Fatalf("quoted username = %q", got)
	}
	history := []MessageEvent{telegramMentionTestEvent("10004", 10, telegramMentionTestUsername("DirectName"))}
	if got := resolveTelegramMentionUsernames(current, history)["10004"]; got != "DirectName" {
		t.Fatalf("newer quote overrode older direct observation: %q", got)
	}
	current.Quoted.Semantic = true
	if got := resolveTelegramMentionUsernames(current, nil)["10004"]; got != "" {
		t.Fatalf("semantic quote supplied username: %q", got)
	}
	current.Quoted.Semantic = false
	current.Quoted.GroupID = "-10002"
	if got := resolveTelegramMentionUsernames(current, nil)["10004"]; got != "" {
		t.Fatalf("foreign quote supplied username: %q", got)
	}
	current.Quoted = nil
	current.SenderUsername = ""
	current.SenderMentionUsername = nil
	current.SenderName = "OtherName"
	current.MentionTargets = current.MentionTargets[1:]
	if got := resolveTelegramMentionUsernames(current, nil)["10001"]; got != "" {
		t.Fatalf("display-name reverse lookup supplied username: %q", got)
	}
}

func TestTelegramMentionUsernamesScopeAndBots(t *testing.T) {
	current := telegramMentionTestEvent("10001", 20, nil)
	for _, tt := range []struct {
		name   string
		change func(*MessageEvent)
	}{
		{"platform", func(e *MessageEvent) { e.Platform = PlatformOneBotV11 }},
		{"missing platform", func(e *MessageEvent) { e.Platform = "" }},
		{"profile", func(e *MessageEvent) { e.ProfileID = "other" }},
		{"missing profile", func(e *MessageEvent) { e.ProfileID = "" }},
		{"account", func(e *MessageEvent) { e.SelfID = "90002" }},
		{"missing account", func(e *MessageEvent) { e.SelfID = "" }},
		{"group", func(e *MessageEvent) { e.GroupID = "-10002" }},
		{"private", func(e *MessageEvent) { e.GroupID = ""; e.Kind = EventKindPrivate }},
		{"namespace", func(e *MessageEvent) { e.ContextNamespace = "other" }},
		{"platform scope", func(e *MessageEvent) { e.PlatformScope = "other" }},
		{"guild", func(e *MessageEvent) { e.GuildID = "other" }},
		{"cross group", func(e *MessageEvent) { e.crossGroupContext = true }},
		{"outbound", func(e *MessageEvent) { e.Outbound = true }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			foreign := telegramMentionTestEvent("10002", 10, telegramMentionTestUsername("ForeignName"))
			foreign.Quoted = &QuotedMessage{UserID: "10003", SenderMentionUsername: telegramMentionTestUsername("QuotedName")}
			foreign.MentionTargets = []MessageMention{{UserID: "10004", Username: "MentionName", UsernameKnown: true}}
			tt.change(&foreign)
			if got := resolveTelegramMentionUsernames(current, []MessageEvent{foreign}); len(got) != 0 {
				t.Fatalf("foreign metadata leaked: %#v", got)
			}
		})
	}
	bot := telegramMentionTestEvent("10002", 10, telegramMentionTestUsername("TestBot"))
	bot.SenderIsBot = true
	if got := resolveTelegramMentionUsernames(current, []MessageEvent{bot})["10002"]; got != "TestBot" {
		t.Fatalf("bot display username = %q", got)
	}
	current.Platform = PlatformOneBotV11
	if got := resolveTelegramMentionUsernames(current, []MessageEvent{bot}); len(got) != 0 {
		t.Fatalf("non-Telegram resolver returned %#v", got)
	}
}

func TestTelegramMentionUsernamesPrivateScope(t *testing.T) {
	current := telegramMentionTestEvent("10001", 20, nil)
	current.Kind, current.GroupID = EventKindPrivate, ""
	older := current
	older.Time = 10
	older.SenderMentionUsername = telegramMentionTestUsername("PrivateName")
	foreign := older
	foreign.UserID = "10002"
	foreign.Quoted = &QuotedMessage{UserID: "10001", SenderMentionUsername: telegramMentionTestUsername("ForeignName")}
	if got := resolveTelegramMentionUsernames(current, []MessageEvent{foreign, older}); !reflect.DeepEqual(got, map[string]string{"10001": "PrivateName"}) {
		t.Fatalf("private scope = %#v", got)
	}
}

func TestTelegramMentionUsernamesConflictsAndValidation(t *testing.T) {
	current := telegramMentionTestEvent("10001", 20, telegramMentionTestUsername("SharedName"))
	history := []MessageEvent{telegramMentionTestEvent("10002", 10, telegramMentionTestUsername("sharedNAME"))}
	if got := resolveTelegramMentionUsernames(current, history); len(got) != 0 {
		t.Fatalf("case-insensitive username collision not rejected: %#v", got)
	}
	history = append(history, telegramMentionTestEvent("10002", 30, telegramMentionTestUsername("RenamedUser")))
	want := map[string]string{"10001": "SharedName", "10002": "RenamedUser"}
	if got := resolveTelegramMentionUsernames(current, history); !reflect.DeepEqual(got, want) {
		t.Fatalf("superseded username caused false collision: %#v", got)
	}
	for _, invalid := range []string{"", "@", "@handle", " Handle ", " @Mixed_Name ", "two words", "a\nb", "example.com", "user-name", "中文名", "abcdefghijklmnopqrstuvwxyz1234567"} {
		current.SenderMentionUsername = telegramMentionTestUsername(invalid)
		if got := resolveTelegramMentionUsernames(current, nil); len(got) != 0 {
			t.Fatalf("invalid username %q resolved to %#v", invalid, got)
		}
	}
	for _, valid := range []string{"Mixed_Name", "a", "9name", "10001"} {
		current.SenderMentionUsername = telegramMentionTestUsername(valid)
		if got := resolveTelegramMentionUsernames(current, nil)["10001"]; got != valid {
			t.Fatalf("username = %q, want %q", got, valid)
		}
	}
}

func TestTelegramMentionUsernameMetadataRoundTrip(t *testing.T) {
	for _, username := range []*string{nil, telegramMentionTestUsername(""), telegramMentionTestUsername("AliceName")} {
		event := telegramMentionTestEvent("10001", 10, username)
		event.Quoted = quotedMessageFromHistory(event)
		event.MentionTargets = []MessageMention{{UserID: "10002", UsernameKnown: true}}
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		var restored MessageEvent
		if err := json.Unmarshal(data, &restored); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(restored.SenderMentionUsername, username) || !reflect.DeepEqual(restored.Quoted.SenderMentionUsername, username) || !restored.MentionTargets[0].UsernameKnown {
			t.Fatalf("lost unknown/absent metadata: %s", data)
		}
	}
}

func TestTelegramMentionTargetsCarryKnownUsername(t *testing.T) {
	for _, username := range []string{"", "AliceName"} {
		got := telegramMentionTargets("Alice", []telegramEntity{{Type: "text_mention", Offset: 0, Length: 5, User: &telegramUser{ID: 10001, Username: username}}}, "90001", "TestBot")
		if len(got) != 1 || got[0].UserID != "10001" || got[0].Username != username || !got[0].UsernameKnown {
			t.Fatalf("text_mention metadata = %#v", got)
		}
	}
}

func TestTelegramMentionUsernamesCannotAuthenticateOwner(t *testing.T) {
	cfg := BotConfig{ID: "telegram-test", Platform: PlatformTelegram, OwnerID: "OwnerName"}
	event := telegramMentionTestEvent("10001", 10, telegramMentionTestUsername("OwnerName"))
	event.Quoted = &QuotedMessage{UserID: "10001", SenderMentionUsername: telegramMentionTestUsername("OwnerName")}
	event.MentionTargets = []MessageMention{{UserID: "10001", Username: "OwnerName", UsernameKnown: true}}
	if cfg.IsOwnerEvent(event) {
		t.Fatal("display-only metadata authenticated sender")
	}
	resolveTelegramMentionUsernames(event, nil)
	if event.SenderUsername != "" || cfg.IsOwnerEvent(event) {
		t.Fatal("resolver mutated authenticated identity")
	}
	event.SenderIsBot = true
	event.SenderUsername = "OwnerName"
	if cfg.IsOwnerEvent(event) {
		t.Fatal("legacy bot display handle authenticated sender")
	}
}

func TestRuntimeClearsReusedTelegramMentionUsernames(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	for _, platform := range []string{PlatformTelegram, PlatformOneBotV11, PlatformQQOfficial} {
		for _, text := range []string{"plain text", "<diana-at id=\"10001\"/>"} {
			event := telegramMentionTestEvent("10001", 20, nil)
			event.Platform = platform
			msg := runtime.resolveOutgoingMentionNames(event, OutgoingMessage{
				Text: text, MentionUsernames: map[string]string{"10001": "SourcePlatformName"},
			})
			if len(msg.MentionUsernames) != 0 {
				t.Fatalf("%s %q reused source usernames: %#v", platform, text, msg.MentionUsernames)
			}
		}
	}
}

func TestRuntimeResolvesTelegramMentionUsernamesWithoutDisplayName(t *testing.T) {
	runtime := NewRuntime(BotConfig{Platform: PlatformTelegram, BotAccount: "90001"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := telegramMentionTestEvent("10001", 20, telegramMentionTestUsername("CurrentName"))
	runtime.history[sessionKey(event)] = []MessageEvent{telegramMentionTestEvent("10002", 10, telegramMentionTestUsername("HistoryName"))}
	msg := runtime.resolveOutgoingMentionNames(event, OutgoingMessage{Text: "<diana-at id=\"10001\"/> <diana-at id=\"10002\"/> <diana-at id=\"10003\"/>", MentionUsernames: map[string]string{"10003": "UnverifiedName"}})
	want := map[string]string{"10001": "CurrentName", "10002": "HistoryName"}
	if !reflect.DeepEqual(msg.MentionUsernames, want) {
		t.Fatalf("outgoing usernames = %#v, want %#v", msg.MentionUsernames, want)
	}
	if msg.MentionNames != nil {
		t.Fatalf("invented display names: %#v", msg.MentionNames)
	}
}
