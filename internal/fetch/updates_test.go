package fetch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

// snowLeopardSecurity is 10.6's updates = "security", in fetch order:
// the members of Apple's client 10.6.8 combo product (catalogue product
// 041-98179) -- carried beside its distribution, which installs them in
// one installer run -- then that distribution, then Security Update
// 2013-004 (041-91751). MEASURED 2026-10-04: installing the members one
// at a time breaks a running 10.6.0; the distribution in one run does not.
var snowLeopardSecurity = []string{
	"apple-combo-10.6.8-part0", "apple-combo-10.6.8-part1", "apple-combo-10.6.8-part2",
	"apple-combo-10.6.8-part3", "apple-combo-10.6.8-part4", "apple-combo-10.6.8-part5",
	"apple-combo-10.6.8-part6", "apple-combo-10.6.8-part7", "apple-combo-10.6.8-part8",
	"apple-combo-10.6.8-part9", "apple-combo-10.6.8-part10", "apple-combo-10.6.8-part11",
	"apple-combo-10.6.8-part12", "apple-combo-10.6.8-subasesystem", "apple-combo-10.6.8-qt7",
	"apple-combo-10.6.8-x11", "apple-combo-10.6.8-rosetta", "apple-combo-10.6.8-meta",
	"apple-combo-10.6.8-dist",
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

// TestAProductsMembersAreCarriedUnderTheirOwnNames: a distribution names
// its packages by their own file names, so a member is staged under its
// base name and marked a member, not installed by itself; the
// distribution and the security update are what get installed, in order.
func TestAProductsMembersAreCarriedUnderTheirOwnNames(t *testing.T) {
	dist := []byte(`<?xml version="1.0"?><installer-gui-script minSpecVersion="1"></installer-gui-script>`)
	bodies := map[string][]byte{}
	var rows []string
	srvBodies := func(n, file string, b []byte) {
		bodies["/"+file] = b
		rows = append(rows, fmt.Sprintf("%s\t%%s/%s\t%s", n, file, sum(b)))
	}
	for _, n := range snowLeopardSecurity {
		file := n + ".pkg"
		b := xar(n)
		if n == "apple-combo-10.6.8-dist" {
			file, b = "041-98179.English.dist", dist
		}
		srvBodies(n, file, b)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b, ok := bodies[r.URL.Path]; ok {
			w.Write(b)
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	var reg strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&reg, r+"\n", srv.URL)
	}
	pr, err := pins.Parse(strings.NewReader(reg.String()))
	if err != nil {
		t.Fatal(err)
	}
	got, err := getter(t).UpdatesFor(context.Background(), pr, "snowleopard", "security")
	if err != nil {
		t.Fatal(err)
	}
	var installed []string
	for _, u := range got {
		if u.Member {
			if u.Staged != filepath.Base(u.Path) {
				t.Errorf("member %s staged as %s; want its own name %s", u.Name, u.Staged, filepath.Base(u.Path))
			}
			continue
		}
		installed = append(installed, u.Staged)
	}
	want := []string{"mqg-update-01-041-98179.English.dist", "mqg-update-02-apple-secupd-2013-004-snowleopard.pkg"}
	if !slices.Equal(installed, want) {
		t.Fatalf("installed %v; want %v", installed, want)
	}
}

func TestADistributionThatIsNotOneIsRefused(t *testing.T) {
	body := []byte("not a distribution")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer srv.Close()
	reg, _ := pins.Parse(strings.NewReader(fmt.Sprintf("apple-combo-10.6.8-dist\t%s/x.dist\t%s\n", srv.URL, sum(body))))
	if _, err := getter(t).getUpdate(context.Background(), reg, "apple-combo-10.6.8-dist", 1); err == nil || !strings.Contains(err.Error(), "installer-gui-script") {
		t.Fatalf("err = %v", err)
	}
}
