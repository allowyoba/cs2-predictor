package telegram

import (
	"context"
	"encoding/json"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

// MembershipAdapter implements chat.MembershipGateway via getChatMember,
// mapping Telegram's status string to chat.MemberRole exactly as
// TelegramMembershipAdapter does (unrecognized/"left" both map to LEFT).
type MembershipAdapter struct {
	client *Client
}

func NewMembershipAdapter(client *Client) *MembershipAdapter {
	return &MembershipAdapter{client: client}
}

func (a *MembershipAdapter) Role(ctx context.Context, chatID common.ChatID, userID common.UserID) (chat.MemberRole, error) {
	result, err := a.client.Call(ctx, "getChatMember", map[string]any{"chat_id": chatID.Value, "user_id": userID.Value})
	if err != nil {
		return "", err
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(result, &body); err != nil {
		return "", err
	}
	switch body.Status {
	case "creator":
		return chat.RoleOwner, nil
	case "administrator":
		return chat.RoleAdministrator, nil
	case "member":
		return chat.RoleMember, nil
	case "restricted":
		return chat.RoleRestricted, nil
	case "kicked":
		return chat.RoleKicked, nil
	default:
		return chat.RoleLeft, nil
	}
}

func (a *MembershipAdapter) Administrators(ctx context.Context, chatID common.ChatID) ([]common.UserID, error) {
	result, err := a.client.Call(ctx, "getChatAdministrators", map[string]any{"chat_id": chatID.Value})
	if err != nil {
		return nil, err
	}
	var body []struct {
		User struct {
			ID    int64 `json:"id"`
			IsBot bool  `json:"is_bot"`
		} `json:"user"`
	}
	if err := json.Unmarshal(result, &body); err != nil {
		return nil, err
	}
	out := make([]common.UserID, 0, len(body))
	for _, item := range body {
		if item.User.IsBot {
			continue
		}
		out = append(out, common.UserID{Value: item.User.ID})
	}
	return out, nil
}
