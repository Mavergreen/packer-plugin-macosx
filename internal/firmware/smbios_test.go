package firmware

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/Mavergreen/packer-plugin-macosx"
)

func embeddedConfig(t *testing.T) []byte {
	t.Helper()
	b, err := fs.ReadFile(macosx.Files, "assets/firmware/config.plist")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestTheTableMatchesItsGoldenWordForWord: the evidence is measured
// text, so an edit to it is an edit to testdata/golden/smbios-models.txt
// too.
func TestTheTableMatchesItsGoldenWordForWord(t *testing.T) {
	var got strings.Builder
	for _, m := range SMBIOSModels {
		got.WriteString(m.Model + "\t" + m.Status + "\t" + m.Evidence + "\n")
	}
	if want := golden(t, "smbios-models.txt"); got.String() != want {
		t.Fatalf("got:\n%s\ngolden:\n%s", got.String(), want)
	}
}

func TestTheDefaultIsWhatConfigPlistShips(t *testing.T) {
	if DefaultSMBIOS != "iMac14,2" || ProductName(embeddedConfig(t)) != DefaultSMBIOS {
		t.Fatalf("default %s, config.plist %s", DefaultSMBIOS, ProductName(embeddedConfig(t)))
	}
}

// TestVerdictsMatchTheirGoldens: SMBIOSVerdict and SMBIOSStatusText,
// for a verified, a panicked and an unlisted model and every status word.
func TestVerdictsMatchTheirGoldens(t *testing.T) {
	models := []string{"iMac14,2", "MacPro5,1", "Macmini6,2"}
	verdicts := goldenLines(t, "smbios-verdict.txt")
	if len(verdicts) != len(models) {
		t.Fatalf("%d verdicts, want %d", len(verdicts), len(models))
	}
	for i, m := range models {
		s, d := SMBIOSVerdict(m)
		if want := verdicts[i] + "\n"; s+"\t"+d+"\n" != want {
			t.Errorf("verdict %s:\n got:    %q\n golden: %q", m, s+"\t"+d, want)
		}
	}
	statuses := []string{"VERIFIED", "BOOTED", "PANICKED", "NOT-TESTED", "UNLISTED", "WEIRD"}
	texts := goldenLines(t, "smbios-status-text.txt")
	if len(texts) != len(statuses) {
		t.Fatalf("%d status texts, want %d", len(texts), len(statuses))
	}
	for i, st := range statuses {
		if want := texts[i]; SMBIOSStatusText(st) != want {
			t.Errorf("status text %s differs", st)
		}
	}
}

func TestWellformedness(t *testing.T) {
	for m, ok := range map[string]bool{
		"iMac14,2": true, "MacPro5,1": true, "My_Model-1.0": true,
		"": false, "iMac<14>": false, "a b": false, "a&b": false, `a"b`: false, "a\nb": false,
		strings.Repeat("a", 64): true, strings.Repeat("a", 65): false,
	} {
		if SMBIOSWellformed(m) != ok {
			t.Errorf("%q: want %v", m, ok)
		}
	}
}

// TestSetProductNameMatchesItsGoldenByteForByte: the shipped
// config.plist with each model set, byte for byte.
func TestSetProductNameMatchesItsGoldenByteForByte(t *testing.T) {
	for m, file := range map[string]string{
		"MacPro5,1": "smbios-plist-set-MacPro5-1.plist",
		"iMac14,2":  "smbios-plist-set-iMac14-2.plist",
	} {
		got, err := SetProductName(embeddedConfig(t), m)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != golden(t, file) {
			t.Errorf("%s: the edit differs from %s", m, file)
		}
	}
}

func TestSettingTheModelAlreadyThereChangesNoByte(t *testing.T) {
	if same, _ := SetProductName(embeddedConfig(t), DefaultSMBIOS); string(same) != string(embeddedConfig(t)) {
		t.Fatal("setting the model already there must not change a byte")
	}
}

// TestProductNameAndSetProductNameWhenACommentPrecedesTheStringTag: a
// comment (or anything else starting with "<") between the
// SystemProductName key and its <string> value is not stripped as a
// prefix: ProductName and SetProductName read lines, not XML, and fall
// back to their weaker, literal behaviour, byte for byte what the
// goldens hold, rather than guessing.
func TestProductNameAndSetProductNameWhenACommentPrecedesTheStringTag(t *testing.T) {
	fixture := "<key>SystemProductName</key>\n\t<!-- c --><string>iMac14,2</string>\n"

	if got, want := ProductName([]byte(fixture))+"\n", golden(t, "smbios-comment-product-name.txt"); got != want {
		t.Errorf("ProductName:\n got:    %q\n golden: %q", got, want)
	}

	got, err := SetProductName([]byte(fixture), "MacPro5,1")
	if err != nil {
		t.Fatal(err)
	}
	want := golden(t, "smbios-comment-plist-set.plist")
	if string(got) != want {
		t.Errorf("SetProductName:\n got:    %q\n golden: %q", got, want)
	}
}

// A key line that also carries its <string>: taking it for the key alone
// would rewrite the next line that contains <string> -- whatever
// key/value follows, not SystemProductName's own value. SetProductName
// refuses this shape outright.
func TestSetProductNameRefusesWhenKeyAndValueShareALine(t *testing.T) {
	plist := "<key>SystemProductName</key><string>x</string>\n"
	_, err := SetProductName([]byte(plist), "MacPro5,1")
	if err == nil || !strings.Contains(err.Error(), "share a line") {
		t.Fatalf("err = %v", err)
	}
}

func TestSetProductNameRefusesWhatItCannotDoSafely(t *testing.T) {
	if _, err := SetProductName(embeddedConfig(t), "a<b"); err == nil {
		t.Fatal("a malformed model must be refused")
	}
	two := strings.Replace(string(embeddedConfig(t)), "<key>SystemProductName</key>",
		"<key>SystemProductName</key>\n<string>x</string>\n<key>SystemProductName</key>", 1)
	if _, err := SetProductName([]byte(two), "MacPro5,1"); err == nil || !strings.Contains(err.Error(), "2 SystemProductName keys") {
		t.Fatalf("err = %v", err)
	}
	if _, err := SetProductName([]byte("<plist></plist>\n"), "MacPro5,1"); err == nil || !strings.Contains(err.Error(), "0 SystemProductName keys") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckNeverFails(t *testing.T) {
	var log strings.Builder
	logf := func(f string, a ...any) { log.WriteString(f + "\n") }
	for _, m := range []string{"iMac14,2", "MacPro5,1", "Macmini6,2"} {
		SMBIOSCheck(m, logf) // no error to return: the table is guidance
	}
	if !strings.Contains(log.String(), "warning") {
		t.Fatal("an unlisted or panicked model must warn")
	}
}
