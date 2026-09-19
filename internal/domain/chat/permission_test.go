package chat

import "testing"

// Permissions arrive as raw strings from callback data and from the
// database, so "is this a permission at all" is a real question with a
// security answer, not a formality.
func TestValidPermission(t *testing.T) {
	for _, p := range AllPermissions() {
		if !ValidPermission(p) {
			t.Fatalf("%q is in AllPermissions but not accepted by ValidPermission", p)
		}
	}
	for _, p := range []Permission{"", "manage_everything", "MANAGE_GROUP_SETTINGS "} {
		if ValidPermission(p) {
			t.Fatalf("%q must not be accepted as a permission", p)
		}
	}
}
