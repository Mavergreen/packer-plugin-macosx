package payload

import (
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"

	macosx "github.com/Mavergreen/packer-plugin-macosx"
)

// TestAFailedInstallEndsPackersSSHWaitSayingWhy runs autoinstall.sh's
// refuse_logins -- its answer to a failed install (docs/decisions/0028) -- on
// a free loopback port, and connects to it as Packer's qemu builder (v1.1.7)
// does: x/crypto's ssh v0.57.0, the version in that plugin binary (and in
// this module), with a public key. packer-plugin-sdk v0.6.11's
// StepConnectSSH counts a handshake error toward ssh_handshake_attempts only
// when it contains "authenticate" (communicator/step_connect_ssh.go), and
// reports it as Packer's error: so the client's error must say that, that
// the install failed, and where its log is.
func TestAFailedInstallEndsPackersSSHWaitSayingWhy(t *testing.T) {
	perl, err := exec.LookPath("perl")
	if err != nil {
		t.Skip("no perl on this host")
	}
	b, err := fs.ReadFile(macosx.Files, "assets/guest/autoinstall/autoinstall.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	hook := filepath.Join(dir, "autoinstall.sh")
	if err := os.WriteFile(hook, b, 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	pidfile := filepath.Join(dir, "refuse.pid")
	cmd := exec.Command("/bin/sh", hook, "the installer said: Oct 19 07:31:17 localhost OSInstaller[243]: Install failed: The Installer encountered an error")
	cmd.Env = append(os.Environ(),
		"GUEST_MACOSX_AUTOINSTALL_ONLY=refuse_logins",
		"GUEST_MACOSX_AUTOINSTALL_LOG="+filepath.Join(dir, "auto.log"),
		"GUEST_MACOSX_TARGET_VOLUME=Snow Leopard",
		"GUEST_MACOSX_PERL="+perl,
		"GUEST_MACOSX_REFUSE_ADDR=127.0.0.1",
		"GUEST_MACOSX_REFUSE_PORT="+strconv.Itoa(port),
		"GUEST_MACOSX_REFUSE_PIDFILE="+pidfile,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("refuse_logins: %v\n%s", err, out)
	}
	defer func() {
		if p, err := os.ReadFile(pidfile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(p))); err == nil && pid > 1 {
				_ = syscall.Kill(pid, syscall.SIGTERM)
			}
		}
	}()

	key, err := gossh.ParsePrivateKey(mustRead(t, filepath.Join(repoRoot(), "templates", "snowleopard", "vagrant-standard-insecure-first-boot-only.key.rsa")))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &gossh.ClientConfig{
		User:            "vagrant",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(key)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}
	// Twice, as Packer tries again: the answer is there for every connection.
	for i := 0; i < 2; i++ {
		c, err := gossh.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), cfg)
		if err == nil {
			c.Close()
			t.Fatal("logged in to a failed install")
		}
		msg := err.Error()
		for _, want := range []string{
			"authenticate", // what the SDK counts toward ssh_handshake_attempts
			"disconnect, reason 14",
			"INSTALL FAILED: the installer said: Oct 19 07:31:17 localhost OSInstaller[243]: Install failed: The Installer encountered an error",
			"output-snowleopard-install.log",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("attempt %d: the client's error lacks %q:\n%s", i+1, want, msg)
			}
		}
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
