package fetch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Mavergreen/packer-plugin-mavericks/internal/pins"
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
