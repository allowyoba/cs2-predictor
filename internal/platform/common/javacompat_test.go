package common

import "testing"

// Ground truth from a real JVM (jshell, JDK 21):
//
//	UUID.nameUUIDFromBytes("pandascore:event:123".getBytes(UTF_8))
//	  => d37a235e-6682-35fa-859e-28eff2fb8c5e
//	"cs2predictor:discover-events".hashCode() => 332604669
//	"test".hashCode() => 3556498
func TestNameUUIDMatchesJava(t *testing.T) {
	got := NameUUID("pandascore:event:123").String()
	want := "d37a235e-6682-35fa-859e-28eff2fb8c5e"
	if got != want {
		t.Fatalf("NameUUID mismatch: got %s want %s", got, want)
	}
}

func TestJavaStringHashCodeMatchesJava(t *testing.T) {
	cases := map[string]int32{
		"cs2predictor:discover-events": 332604669,
		"test":                         3556498,
	}
	for s, want := range cases {
		if got := JavaStringHashCode(s); got != want {
			t.Errorf("JavaStringHashCode(%q) = %d, want %d", s, got, want)
		}
	}
}

func TestAdvisoryLockKey_MatchesJavaHashCode(t *testing.T) {
	// int64(int32(332604669)) is a no-op sign-wise since it's positive and
	// fits in 32 bits; this pins that the two functions actually agree.
	if got := AdvisoryLockKey("cs2predictor:discover-events"); got != 332604669 {
		t.Fatalf("AdvisoryLockKey = %d, want 332604669", got)
	}
}

func TestAdvisoryLockKey_IsStableAcrossCalls(t *testing.T) {
	name := "cs2predictor:sync-matches"
	if a, b := AdvisoryLockKey(name), AdvisoryLockKey(name); a != b {
		t.Fatal("AdvisoryLockKey is not deterministic for the same input")
	}
}
