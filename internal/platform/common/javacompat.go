package common

import (
	"crypto/md5" //nolint:gosec // intentionally MD5: must byte-match Java's UUID.nameUUIDFromBytes
	"unicode/utf16"

	"github.com/google/uuid"
)

// NameUUID replicates Java's UUID.nameUUIDFromBytes(bytes): an MD5 digest of
// the raw UTF-8 bytes with NO namespace prefix (unlike uuid.NewMD5, which
// hashes namespace+data and would produce a different UUID), then the
// standard RFC 4122 version-3/variant bits are stamped onto the digest.
//
// This is used everywhere the PandaScore adapter and the Postgres
// persistence layer derive a deterministic id from an external id (e.g.
// "pandascore:event:123", "stage:<eventID>:<stageName>") — it must produce
// byte-identical UUIDs across deployments so ids derived from the same
// external key never drift, even if some rows were written by an older
// build.
func NameUUID(name string) uuid.UUID {
	sum := md5.Sum([]byte(name))    //nolint:gosec
	sum[6] = (sum[6] & 0x0f) | 0x30 // version 3
	sum[8] = (sum[8] & 0x3f) | 0x80 // variant RFC 4122
	var id uuid.UUID
	copy(id[:], sum[:])
	return id
}

// JavaStringHashCode replicates java.lang.String.hashCode(): a 31-multiplier
// rolling hash over the string's UTF-16 code units (s[0]*31^(n-1) + ... +
// s[n-1]), returned sign-extended as used for a Postgres advisory-lock key
// so advisory-lock keys stay stable across deployments.
//
// This operates on UTF-16 code units, not bytes or runes, because Java
// strings are UTF-16 — for the ASCII lock names this app actually uses
// ("cs2predictor:discover-events" etc.) the result is identical to a
// byte-wise hash, but the UTF-16 path keeps it correct in general.
func JavaStringHashCode(s string) int32 {
	var h int32
	for _, unit := range utf16.Encode([]rune(s)) {
		h = 31*h + int32(unit)
	}
	return h
}

// AdvisoryLockKey converts a lock name to the int64 key ClusterLock passes
// to pg_try_advisory_lock/pg_advisory_unlock (sign-extension of the 32-bit
// hash into an int64).
func AdvisoryLockKey(name string) int64 {
	return int64(JavaStringHashCode(name))
}
