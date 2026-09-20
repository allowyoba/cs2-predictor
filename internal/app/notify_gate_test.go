package app

import (
	"context"
	"testing"

	"cs2predictor/internal/platform/common"
)

// allNotificationsOn stands for a deployment where every switch has been
// turned on, which is what the behaviour tests around it are actually
// about: they check what a notification says and when it fires, not
// whether anybody asked for it. The asking is covered here.
type allNotificationsOn struct{}

func (allNotificationsOn) NotifyEnabled(context.Context, common.NotifyScope, int64, string) (bool, error) {
	return true, nil
}

func (allNotificationsOn) NotifySettings(context.Context, common.NotifyScope, int64) (map[string]bool, error) {
	return nil, nil
}

func (allNotificationsOn) SetNotifyEnabled(context.Context, common.NotifyScope, int64, string, bool) error {
	return nil
}

func (allNotificationsOn) NotifySubjects(_ context.Context, _ common.NotifyScope, _ string, candidates []int64) ([]int64, error) {
	return candidates, nil
}

// A gate with nothing behind it says no. A deployment that loses its
// preference store must go quiet, not start broadcasting to everybody —
// the failure mode of a switchboard is silence.
func TestNotifyGate_WithoutAStoreEverythingIsOff(t *testing.T) {
	var gate NotifyGate
	ctx := context.Background()

	if on, err := gate.ChatWants(ctx, common.ChatID{Value: -100}, common.ChatNotifyDigests); err != nil || on {
		t.Fatalf("ChatWants = %v, %v; want false with no store", on, err)
	}
	if on, err := gate.UserWants(ctx, common.UserID{Value: 7}, common.NotifyResultRecaps); err != nil || on {
		t.Fatalf("UserWants = %v, %v; want false with no store", on, err)
	}
	if on, err := gate.OperatorWants(ctx, 1, common.AdminAlertHostPressure); err != nil || on {
		t.Fatalf("OperatorWants = %v, %v; want false with no store", on, err)
	}
	wanting, err := gate.ChatsWanting(ctx, common.ChatNotifyDigests, []int64{-100, -200})
	if err != nil || len(wanting) != 0 {
		t.Fatalf("ChatsWanting = %v, %v; want nobody with no store", wanting, err)
	}
}

// The "it is working again" half of a pair is not separately switchable:
// an operator who asked to hear about a broken provider must also hear
// that it recovered, or the alert they kept is worse than none.
func TestNotifyGate_RecoveryFollowsTheAlertThatOpenedIt(t *testing.T) {
	gate := NotifyGate{Switches: onlyKind{kind: string(common.AdminAlertProviderDown)}}
	ctx := context.Background()

	for _, kind := range []common.AdminAlertKind{common.AdminAlertProviderDown, common.AdminAlertProviderRecovered} {
		on, err := gate.OperatorWants(ctx, 1, kind)
		if err != nil || !on {
			t.Fatalf("OperatorWants(%s) = %v, %v; want true", kind, on, err)
		}
	}
	if on, _ := gate.OperatorWants(ctx, 1, common.AdminAlertHostPressure); on {
		t.Fatal("an unrelated alert rode in on another kind's switch")
	}
}

type onlyKind struct {
	allNotificationsOn
	kind string
}

func (o onlyKind) NotifyEnabled(_ context.Context, _ common.NotifyScope, _ int64, kind string) (bool, error) {
	return kind == o.kind, nil
}
