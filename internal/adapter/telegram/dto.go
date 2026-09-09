package telegram

// Update DTOs — the subset of Telegram's webhook update JSON this bot uses.

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
	PollAnswer    *PollAnswer    `json:"poll_answer"`
}

type Message struct {
	MessageID       int64    `json:"message_id"`
	Chat            Chat     `json:"chat"`
	From            *User    `json:"from"`
	Text            *string  `json:"text"`
	MessageThreadID *int64   `json:"message_thread_id"`
	ReplyToMessage  *Message `json:"reply_to_message"`
}

type Chat struct {
	ID    int64   `json:"id"`
	Type  string  `json:"type"`
	Title *string `json:"title"`
}

type User struct {
	ID        int64   `json:"id"`
	Username  *string `json:"username"`
	FirstName string  `json:"first_name"`
	LastName  *string `json:"last_name"`
	// LanguageCode is the IETF tag from the user's own Telegram client
	// ("en", "ru-RU", ...) — used once, on first contact, to pick their
	// private-chat language before they've chosen one explicitly.
	LanguageCode *string `json:"language_code"`
}

func (u User) DisplayName() string {
	if u.LastName != nil && *u.LastName != "" {
		return u.FirstName + " " + *u.LastName
	}
	return u.FirstName
}

type PollAnswer struct {
	PollID    string `json:"poll_id"`
	User      User   `json:"user"`
	OptionIDs []int  `json:"option_ids"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message"`
	Data    *string  `json:"data"`
}
