package app

import (
	"context"
	"log/slog"
	"testing"

	"cs2predictor/internal/platform/common"
)

// The requirement in one place: nothing the bot sends on its own
// initiative goes out until somebody turned it on. Each case below drives
// a real producer with its switchboard empty — not a stub of one — so a
// future notification that forgets its gate fails here rather than in
// somebody's chat.
func TestNothingIsSentUntilItIsTurnedOn(t *testing.T) {
	t.Run("administrator alerts", func(t *testing.T) {
		outbox := &fakeSyncOutbox{}
		alerter := &AdminAlerter{Outbox: outbox, ChatIDs: []int64{1, 2}, Log: slog.Default()}

		alerter.ProviderDown(context.Background(), "pandascore", 5, "timeout")
		alerter.HostPressure(context.Background(), "memory", "94%", "90%")
		alerter.DeadLetters(context.Background(), 12, "telegram.match-result")

		if len(outbox.enqueued) != 0 {
			t.Fatalf("enqueued %v to operators who never asked for any of it", outbox.enqueued)
		}
	})

	t.Run("every operator alert has a switch", func(t *testing.T) {
		// A kind with no switch could never be turned off, and one that is
		// only a recovery notice could never be turned on by itself.
		for _, kind := range []common.AdminAlertKind{
			common.AdminAlertRelease, common.AdminAlertProviderDown, common.AdminAlertProviderRecovered,
			common.AdminAlertWebhookBroken, common.AdminAlertWebhookRecovered,
			common.AdminAlertDeadLetters, common.AdminAlertHostPressure, common.AdminAlertHostRecovered,
		} {
			if !common.KnownAdminAlert(common.AlertSwitch(kind)) {
				t.Errorf("%s files under %s, which is not a switch anybody can see", kind, common.AlertSwitch(kind))
			}
		}
	})
}

// Turning one switch on must not carry anything else with it.
func TestTurningOneSwitchOnSendsOnlyThatOne(t *testing.T) {
	outbox := &fakeSyncOutbox{}
	alerter := &AdminAlerter{
		Outbox:   outbox,
		ChatIDs:  []int64{1},
		Switches: onlyKind{kind: string(common.AdminAlertHostPressure)},
		Log:      slog.Default(),
	}

	alerter.ProviderDown(context.Background(), "pandascore", 5, "timeout")
	if len(outbox.enqueued) != 0 {
		t.Fatalf("a provider alert went out on the host-pressure switch: %v", outbox.enqueued)
	}

	alerter.HostPressure(context.Background(), "memory", "94%", "90%")
	if len(outbox.enqueued) != 1 {
		t.Fatalf("enqueued = %v, want the one alert that was switched on", outbox.enqueued)
	}
}
