package media

import (
	"regexp"
	"testing"
)

// The tests in this file check assets/privops/*.sh: the payloads the
// microVM runs to assemble and digest installer media. They are shell
// scripts because they run inside the privops guest, a busybox system,
// so what is checked here is static invariants of the scripts' own text.
// The host side's ordering (assembly before the ownership pass) and the
// digest's found/hashed comparison are covered where the media build is
// tested.

var (
	chownRe        = regexp.MustCompile(`(?m)^[^#]*\bchown\b`)
	tarPipeRe      = regexp.MustCompile(`tar x.*\|`)
	volumeArtifact = []string{".Spotlight-V100", ".fseventsd", ".Trashes"}
)

// "the assembly payload never chowns: that is the ownership pass's job":
// fix-ownership.sh records the setuid/setgid modes BEFORE the chown that
// strips them and restores them after. A chown anywhere else would run
// before the code that makes it reversible.
func TestAssembleShNeverChowns(t *testing.T) {
	if chownRe.Match(embedded(t, "assets/privops/assemble.sh")) {
		t.Error("assets/privops/assemble.sh calls chown -- that is fix-ownership.sh's job")
	}
	if !embeddedContains(t, "assets/privops/fix-ownership.sh", "SPECIAL=") {
		t.Error("assets/privops/fix-ownership.sh does not record SPECIAL=")
	}
}

// "what gets injected is staged before the ownership pass runs": the
// assets/privops/assemble.sh-specific half of the bats test -- it writes
// to the raw disk MQG_RAW3 names, and its injection step must not run
// through a pipe, because busybox ash has no pipefail and `tar x ... |
// sed` would report sed's exit status, not tar's.
func TestAssembleShStagesThroughARawDiskAndNeverPipesItsUntar(t *testing.T) {
	if !embeddedContains(t, "assets/privops/assemble.sh", "MQG_RAW3") {
		t.Error("assets/privops/assemble.sh never mentions MQG_RAW3")
	}
	if tarPipeRe.Match(embedded(t, "assets/privops/assemble.sh")) {
		t.Error("assets/privops/assemble.sh pipes tar's extraction, hiding its exit status")
	}
}

// "the content digest skips what booting a volume leaves behind": macOS
// creates .Spotlight-V100 (with a fresh UUID each boot), .fseventsd and
// .Trashes the first time a guest boots the installer media, which made
// two media built from the same ESD digest differently although every
// real file matched.
func TestContentDigestSkipsVolumeArtifacts(t *testing.T) {
	b := embedded(t, "assets/privops/content-digest.sh")
	for _, name := range volumeArtifact {
		if !regexp.MustCompile(regexp.QuoteMeta(name)).Match(b) {
			t.Errorf("assets/privops/content-digest.sh does not skip %s", name)
		}
	}
}

// "the content digest hashes in bulk, not one process per file": 39,000
// separate sha256sum processes took five minutes; one xargs took twenty
// seconds, and busybox's sha256sum has less per-process overhead to hide
// behind than coreutils'.
func TestContentDigestHashesInBulk(t *testing.T) {
	if !embeddedContains(t, "assets/privops/content-digest.sh", "xargs") {
		t.Error("assets/privops/content-digest.sh does not use xargs")
	}
}

// "the content digest counts the files it found and the files it hashed":
// an I/O error off a corrupt volume can make sha256sum print nothing for a
// file and carry on; the two counts are what catches that silently.
func TestContentDigestCountsFoundAndHashed(t *testing.T) {
	if !embeddedContains(t, "assets/privops/content-digest.sh", "MQG-DIGEST-FILES") {
		t.Error("assets/privops/content-digest.sh never emits MQG-DIGEST-FILES")
	}
	if !embeddedContains(t, "assets/privops/content-digest.sh", "MQG-DIGEST-HASHED") {
		t.Error("assets/privops/content-digest.sh never emits MQG-DIGEST-HASHED")
	}
}

func embeddedContains(t *testing.T, name, substr string) bool {
	t.Helper()
	return regexp.MustCompile(regexp.QuoteMeta(substr)).Match(embedded(t, name))
}
