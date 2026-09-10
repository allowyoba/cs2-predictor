package telegram

import (
	"context"
	"encoding/json"
	"testing"
)

// TestRegisterCommands_SetsBothScopesForEveryLanguage verifies the exact
// contract Telegram needs: one setMyCommands call per (scope, language)
// pair, with the group/private command sets never crossed.
func TestRegisterCommands_SetsBothScopesForEveryLanguage(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	client := NewClient(Config{BaseURL: server.URL, Token: "test-token"}, server.Client())

	if err := RegisterCommands(context.Background(), client); err != nil {
		t.Fatal(err)
	}

	type call struct {
		scopeType    string
		languageCode string
		commands     []BotCommand
	}
	var got []call
	for _, c := range *calls {
		if c["__method"] != "setMyCommands" {
			continue
		}
		scope, _ := c["scope"].(map[string]any)
		lang, _ := c["language_code"].(string)
		raw, err := json.Marshal(c["commands"])
		if err != nil {
			t.Fatal(err)
		}
		var commands []BotCommand
		if err := json.Unmarshal(raw, &commands); err != nil {
			t.Fatal(err)
		}
		got = append(got, call{scopeType: scope["type"].(string), languageCode: lang, commands: commands})
	}

	// 2 scopes × 3 languages (default "", "ru", "en").
	if len(got) != 6 {
		t.Fatalf("expected 6 setMyCommands calls, got %d: %+v", len(got), got)
	}
	for _, c := range got {
		if c.scopeType != "all_group_chats" && c.scopeType != "all_private_chats" {
			t.Fatalf("unexpected scope %q", c.scopeType)
		}
		if len(c.commands) == 0 {
			t.Fatalf("call %+v carried no commands", c)
		}
		if c.scopeType == "all_group_chats" {
			if !containsCommand(c.commands, "moderator") || !containsCommand(c.commands, "timezone") {
				t.Fatalf("group scope missing a group-only command: %+v", c.commands)
			}
			if containsCommand(c.commands, "start") {
				t.Fatalf("group scope should not carry the private-only /start command: %+v", c.commands)
			}
		}
		if c.scopeType == "all_private_chats" {
			if !containsCommand(c.commands, "start") {
				t.Fatalf("private scope missing /start: %+v", c.commands)
			}
			if containsCommand(c.commands, "moderator") || containsCommand(c.commands, "topic") {
				t.Fatalf("private scope should not carry group-only commands: %+v", c.commands)
			}
		}
	}
}

func containsCommand(commands []BotCommand, name string) bool {
	for _, c := range commands {
		if c.Command == name {
			return true
		}
	}
	return false
}
