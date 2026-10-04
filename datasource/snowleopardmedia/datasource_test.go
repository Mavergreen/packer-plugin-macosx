package snowleopardmedia

import (
	"strings"
	"testing"
	"time"
)

func configure(raw map[string]interface{}) (*Datasource, error) {
	d := new(Datasource)
	return d, d.Configure(raw)
}

func TestConfigureDefaults(t *testing.T) {
	d, err := configure(map[string]interface{}{"installer": "/v.hfs"})
	if err != nil {
		t.Fatal(err)
	}
	c := d.config
	if c.User != "vagrant" || c.Updates != "security" || c.PrivopsTimeout != 15*time.Minute {
		t.Fatalf("defaults = %+v; want vagrant, security, 15m", c)
	}
}

func TestConfigureRequiresTheInstaller(t *testing.T) {
	_, err := configure(map[string]interface{}{})
	if err == nil || !strings.Contains(err.Error(), "installer") {
		t.Fatalf("err = %v", err)
	}
}

func TestSnowLeopardMediaRefusesUpdatesAll(t *testing.T) {
	_, err := configure(map[string]interface{}{"installer": "/v.hfs", "updates": "all"})
	if err == nil || !strings.Contains(err.Error(), `updates "all": choose one of none, security`) {
		t.Fatalf("err = %v", err)
	}
}

// TestSnowLeopardMediaRefusesOpenSSH: the family's OpenSSH is for 10.9,
// so 10.6's schema has no openssh key at all.
func TestSnowLeopardMediaRefusesOpenSSH(t *testing.T) {
	if _, err := configure(map[string]interface{}{"installer": "/v.hfs", "openssh": true}); err == nil {
		t.Fatal("an openssh key was accepted")
	}
}

func TestConfigureRefusesABadUser(t *testing.T) {
	_, err := configure(map[string]interface{}{"installer": "/v.hfs", "user": "bob smith"})
	if err == nil || !strings.Contains(err.Error(), "bob smith") {
		t.Fatalf("err = %v", err)
	}
}
