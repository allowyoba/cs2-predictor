package common

import "testing"

func TestNewID_FunctionsProduceDistinctValues(t *testing.T) {
	if a, b := NewEventID(), NewEventID(); a == b {
		t.Fatal("NewEventID produced the same value twice")
	}
	if a, b := NewMatchID(), NewMatchID(); a == b {
		t.Fatal("NewMatchID produced the same value twice")
	}
	if a, b := NewTeamID(), NewTeamID(); a == b {
		t.Fatal("NewTeamID produced the same value twice")
	}
	if a, b := NewPollID(), NewPollID(); a == b {
		t.Fatal("NewPollID produced the same value twice")
	}
	if a, b := NewRequestID(), NewRequestID(); a == b {
		t.Fatal("NewRequestID produced the same value twice")
	}
}

func TestRequestID_StringIsCompactAndRoundTrips(t *testing.T) {
	id := NewRequestID()
	compact := id.String()
	if len(compact) != 32 {
		t.Fatalf("RequestID.String() = %q, want 32 hex characters with no dashes", compact)
	}

	got, err := ParseRequestID(compact)
	if err != nil || got != id {
		t.Fatalf("ParseRequestID(compact form) = %v, %v; want %v, nil", got, err, id)
	}

	dashed := id.Value.String()
	got, err = ParseRequestID(dashed)
	if err != nil || got != id {
		t.Fatalf("ParseRequestID(dashed form) = %v, %v; want %v, nil", got, err, id)
	}

	if _, err := ParseRequestID("not-a-uuid"); err == nil {
		t.Fatal("ParseRequestID accepted garbage input")
	}
}

func TestUserID_ChatID_String(t *testing.T) {
	user := UserID{Value: 42}
	if got := user.String(); got != "42" {
		t.Fatalf("UserID.String() = %q, want \"42\"", got)
	}
	chat := ChatID{Value: -100123}
	if got := chat.String(); got != "-100123" {
		t.Fatalf("ChatID.String() = %q, want \"-100123\"", got)
	}
}

func TestLocaleCode_Tag(t *testing.T) {
	if got := LocaleRU.Tag(); got != "ru-RU" {
		t.Fatalf("LocaleRU.Tag() = %q, want \"ru-RU\"", got)
	}
	if got := LocaleEN.Tag(); got != "en-US" {
		t.Fatalf("LocaleEN.Tag() = %q, want \"en-US\"", got)
	}
}

func TestLocaleFrom(t *testing.T) {
	cases := map[string]LocaleCode{
		"EN":    LocaleEN,
		"en":    LocaleEN,
		"en-US": LocaleEN,
		"en-us": LocaleEN,
		"RU":    LocaleRU,
		"ru-RU": LocaleRU,
		"":      LocaleRU,
		"fr":    LocaleRU,
		"junk":  LocaleRU,
	}
	for input, want := range cases {
		if got := LocaleFrom(input); got != want {
			t.Errorf("LocaleFrom(%q) = %q, want %q", input, got, want)
		}
	}
}
