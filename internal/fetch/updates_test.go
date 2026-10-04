package fetch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Mavergreen/packer-plugin-macosx/internal/pins"
)

func TestSelectionsNest(t *testing.T) {
	none, _ := UpdateNames("none")
	sec, _ := UpdateNames("security")
	all, _ := UpdateNames("all")
	if len(none) != 0 || !slices.Equal(sec, []string{"apple-secupd-2016-004"}) || len(all) != 7 || all[0] != sec[0] {
		t.Fatalf("none=%v security=%v all=%v", none, sec, all)
	}
	if _, err := UpdateNames("most"); err == nil || !strings.Contains(err.Error(), "most") {
		t.Fatalf("err = %v", err)
	}
}

func TestEveryUpdateIsPinnedWithARealChecksum(t *testing.T) {
	reg, err := pins.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	all, _ := UpdateNames("all")
	for _, n := range all {
		if _, err := reg.Lookup(n); err != nil {
			t.Error(err)
		}
	}
}

func TestUpdatesFetchInInstallOrderWithStagedNames(t *testing.T) {
	bodies := map[string][]byte{"/SecUpd.pkg": xar("s"), "/Safari.pkg": xar("f")}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b, ok := bodies[r.URL.Path]; ok {
			w.Write(b)
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	reg, _ := pins.Parse(strings.NewReader(fmt.Sprintf(
		"apple-secupd-2016-004\t%s/SecUpd.pkg\t%s\n", srv.URL, sum(bodies["/SecUpd.pkg"]))))
	got, err := getter(t).Updates(context.Background(), reg, "security")
	if err != nil || len(got) != 1 || got[0].Staged != "mqg-update-01-SecUpd.pkg" {
		t.Fatalf("%+v %v", got, err)
	}
	if none, err := getter(t).Updates(context.Background(), reg, "none"); err != nil || len(none) != 0 {
		t.Fatalf("none must fetch nothing: %+v %v", none, err)
	}
}

func TestAnUpdateThatIsNotAFlatPackageIsRefused(t *testing.T) {
	body := []byte("PK\x03\x04zip")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer srv.Close()
	reg, _ := pins.Parse(strings.NewReader(fmt.Sprintf("apple-secupd-2016-004\t%s/S.pkg\t%s\n", srv.URL, sum(body))))
	_, err := getter(t).Updates(context.Background(), reg, "security")
	if err == nil || !strings.Contains(err.Error(), "xar") {
		t.Fatalf("err = %v", err)
	}
}

func TestStagedName(t *testing.T) {
	if StagedName(3, "/c/x/iTunesX.pkg") != "mqg-update-03-iTunesX.pkg" {
		t.Fatal(StagedName(3, "/c/x/iTunesX.pkg"))
	}
}

// snowLeopardSecurity is Apple's own 10.6.8 combo product's client
// packages in the order its distribution lists them (catalogue product
// 041-98121), then 10.6's last security update (041-91751): MEASURED
// 2026-10-04 from index-leopard-snowleopard.merged-1.sucatalog.
var snowLeopardSecurity = []string{
	"apple-subasesystem-combo-10.6.8",
	"apple-client-combo-10.6.8",
	"apple-rosetta-combo-10.6.8",
	"apple-qt7-combo-10.6.8",
	"apple-x11-combo-10.6.8",
	"apple-secupd-2013-004-snowleopard",
}

func TestSnowLeopardSelections(t *testing.T) {
	none, err := UpdateNamesFor("snowleopard", "none")
	if err != nil || len(none) != 0 {
		t.Fatalf("none = %v, %v", none, err)
	}
	sec, err := UpdateNamesFor("snowleopard", "security")
	if err != nil || !slices.Equal(sec, snowLeopardSecurity) {
		t.Fatalf("security = %v, %v; want %v", sec, err, snowLeopardSecurity)
	}
	_, err = UpdateNamesFor("snowleopard", "all")
	if err == nil || err.Error() != `unknown updates selection "all": choose one of none, security` {
		t.Fatalf("err = %v", err)
	}
}

func TestUpdateNamesForMavericksIsUpdateNames(t *testing.T) {
	for _, sel := range []string{"none", "security", "all"} {
		a, _ := UpdateNames(sel)
		b, err := UpdateNamesFor("mavericks", sel)
		if err != nil || !slices.Equal(a, b) {
			t.Fatalf("%s: UpdateNamesFor = %v, %v; UpdateNames = %v", sel, b, err, a)
		}
	}
}

func TestEverySnowLeopardUpdateIsPinnedWithARealChecksum(t *testing.T) {
	reg, err := pins.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range snowLeopardSecurity {
		s, err := reg.Lookup(n)
		if err != nil {
			t.Error(err)
			continue
		}
		if len(s.SHA256) != 64 {
			t.Errorf("%s: sha256 %q", n, s.SHA256)
		}
	}
}

// TestSnowLeopardOptionalUpdatesCarryTheirCondition: the combo's Rosetta,
// QuickTime 7 and X11 packages go in only where Apple's distribution
// would put them, the guest already having the component.
func TestSnowLeopardOptionalUpdatesCarryTheirCondition(t *testing.T) {
	want := map[string]string{
		"apple-rosetta-combo-10.6.8": "/usr/libexec/oah/translate",
		"apple-qt7-combo-10.6.8":     "/Applications/Utilities/QuickTime Player 7.app",
		"apple-x11-combo-10.6.8":     "/usr/bin/quartz-wm",
	}
	for _, n := range snowLeopardSecurity {
		if got := UpdateIf(n); got != want[n] {
			t.Errorf("UpdateIf(%s) = %q; want %q", n, got, want[n])
		}
	}
}
