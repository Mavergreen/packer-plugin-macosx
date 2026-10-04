package firmware

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
)

// DefaultSMBIOS is the guest's SMBIOS model when nothing else is asked
// for: assets/firmware/config.plist ships it (docs/decisions/0010).
const DefaultSMBIOS = "iMac14,2"

var smbiosWellformedRe = regexp.MustCompile(`^[A-Za-z0-9,._-]{1,64}$`)

// SMBIOSWellformed is true if model can be written into the plist as an
// Apple model identifier: only letters, digits, comma, dot, dash and
// underscore, and at most 64 characters. This is the one thing refused,
// and it is not about evidence -- SMBIOSModels below is guidance, not a
// whitelist.
func SMBIOSWellformed(model string) bool {
	return smbiosWellformedRe.MatchString(model)
}

// SMBIOSModel is one row of the tested-options table.
type SMBIOSModel struct{ Model, Status, Evidence string }

// SMBIOSModels is the tested-options table, in order: the evidence for
// each row, word for word. TestTheTableMatchesItsGoldenWordForWord
// catches a single wrong byte.
var SMBIOSModels = []SMBIOSModel{
	{"iMac14,2", "VERIFIED", `The default since the first install. Full unattended installs on three hosts -- pet-power-plant (QEMU 8.2.2), squirrel-zapper (QEMU 11.1.1) and ap-juicer (Mac Pro 1,1, QEMU 11.0.2) -- each of which then booted without installer media and answered SSH. The installed guest reports hw.model=iMac14,2, so the override reaches the installed system and not only the installer (docs/configuration-register.md). A 2013 Haswell iMac paired with a Penryn guest CPU, which 10.9 evidently tolerates.`},
	{"MacPro5,1", "PANICKED", `PANICS UNDER KVM in AppleTyMCEDriver, BOOTS UNDER TCG, AND AS OF 2026-09-22 WE KNOW WHY. Three observations under KVM on pet-power-plant (i7-8700B Coffee Lake, NOT a Xeon): 2026-09-17, in the first installs, the INSTALLER panicked under khronokernel's OpenCore 0.6.6 and UTM firmware; 2026-09-21, an ALREADY-INSTALLED 10.9.5 guest on OUR OpenCore 1.0.7, OUR OVMF and OUR config.plist panicked at 40 s; 2026-09-22, reproduced again on a fresh overlay of an installed guest that answers SSH, as a one-variable control -- panic at 40 s, no SSH in 180 s where the default SMBIOS answers in 20-40 s. Also PANICKED on ap-juicer, which IS a Xeon 5150, so it is not about the host CPU. THE CAUSE IS MEASURED AND IT IS NOT THE SMBIOS AND NOT THE HOST. The panic dump prints the CPU registers and RCX is 0x0000000000000280. RCX is the MSR index register for rdmsr/wrmsr and 0x280 is IA32_MC0_CTL2, the first CMCI control register -- which is what a function called enableInterruptForCorrectableMemoryCoreRegister touches. KVM dispatches 0x280-0x29F to get_msr_mce/set_msr_mce, which return 1 (NOT the KVM_MSR_RET_UNSUPPORTED sentinel) when MCG_CMCI_P is clear, so the #GP is injected without ignore_msrs ever being consulted and without any dmesg line. QEMU never sets MCG_CMCI_P. Since Linux commit 281b5278, first released in v6.0 on 2022-10-02. THE CONTROL THAT PROVES IT: the same overlay, the same OpenCore image, the same -cpu line, ONE variable -- -accel tcg instead of -accel kvm -- booted and answered SSH at 80 s, reporting sw_vers 10.9.5 and hw.model MacPro5,1. TCG emulates the MSR instead of delegating it. So this row is a property of KVM's machine-check emulation, not of Mavericks and not of this hardware. WHAT WOULD FALSIFY THE EXPLANATION: a host kernel older than 6.0 with ignore_msrs=1, where 0x280 still fell through to the ignore path -- MacPro5,1 should boot there and dmesg should name 0x280. No host in the fleet is old enough. See docs/configuration-register.md, docs/host-profile.md G14, docs/decisions/0010.`},
}

// SMBIOSStatusText is what one status word means, as a clause that reads
// after the status word itself.
func SMBIOSStatusText(status string) string {
	switch status {
	case "VERIFIED":
		return "a guest was installed with this SMBIOS, booted from it and answered SSH"
	case "BOOTED":
		return "a guest installed under a different SMBIOS booted with this one, but nothing has been installed with it"
	case "PANICKED":
		return "a guest kernel-panicked with this SMBIOS, and the row says where and how often"
	case "NOT-TESTED":
		return "nobody here has tried it, which is not the same as knowing it fails"
	case "UNLISTED":
		return "not one of the models this project has an opinion about, which is not a refusal -- the list is guidance and any well-formed smbios value still works"
	default:
		return fmt.Sprintf("unknown status %q", status)
	}
}

// SMBIOSVerdict is what the table says about model: its status and a
// detail sentence, pure.
func SMBIOSVerdict(model string) (status, detail string) { return Mavericks.SMBIOSVerdict(model) }

// SMBIOSVerdict is what r's table says about model.
func (r Release) SMBIOSVerdict(model string) (status, detail string) {
	for _, m := range r.Models {
		if m.Model == model {
			return m.Status, SMBIOSStatusText(m.Status) + " -- " + m.Evidence
		}
	}
	return "UNLISTED", SMBIOSStatusText("UNLISTED") + ` -- "` + model + `" is not in this project's tested-options table; the default is ` + r.DefaultSMBIOS + ` (` + r.Decision + `)`
}

// SMBIOSCheck is the gate Builder.EFIImage calls. It never fails: the
// table is guidance, not a whitelist.
func SMBIOSCheck(model string, logf func(string, ...any)) { Mavericks.SMBIOSCheck(model, logf) }

// SMBIOSCheck is SMBIOSCheck against r's table.
func (r Release) SMBIOSCheck(model string, logf func(string, ...any)) {
	status, detail := r.SMBIOSVerdict(model)
	switch status {
	case "VERIFIED":
		logf("smbios: %s -- %s", model, detail)
	case "BOOTED":
		logf("smbios: %s -- %s", model, detail)
		logf("warning: smbios: this is not the model the default was verified on.")
		logf("warning: If a guest INSTALLS with it, say so: that is how the row")
		logf("warning: moves to VERIFIED (%s).", r.Decision)
	case "PANICKED":
		logf("warning: smbios: %s -- %s", model, detail)
		logf("warning: Proceeding anyway, because that is the point: this row")
		logf("warning: exists to be re-run, and a panic that reproduces is worth")
		logf("warning: as much as one that does not. WHAT TO WATCH FOR: the panic")
		logf("warning: is on the guest's screen, not in QEMU's output -- watch the")
		logf("warning: display, and remember that 2 colours is white-on-black TEXT")
		logf("warning: and not a blank screen. Record the result in")
		logf("warning: %s either way.", r.Decision)
	case "NOT-TESTED", "UNLISTED":
		logf("warning: smbios: %s -- %s", model, detail)
		logf("warning: Proceeding: the table is guidance, not a whitelist.")
		logf("warning: WHAT TO WATCH FOR: a SystemProductName %s dislikes does", r.Version)
		logf("warning: not fail at QEMU start -- it panics in the guest or hangs")
		logf("warning: at a grey screen, both of which look like 'the install is")
		logf("warning: slow'. If it works, report it; if it panics, report the")
		logf("warning: kext named in the panic. Either answer is worth more than")
		logf("warning: the row it replaces.")
		var names []string
		for _, m := range r.Models {
			names = append(names, m.Model)
		}
		logf("warning: Known models: %s ", strings.Join(names, " "))
	}
}

// productNameStringPrefix is the target line's leading run of non-`<`
// characters, immediately followed by "<string>". It only matches when
// nothing but that run precedes the tag -- a `<` anywhere before
// "<string>" (a comment, say) makes it fail to match at all. ProductName
// reads lines, not XML, and keeps that weakness rather than guess: see
// TestProductNameAndSetProductNameWhenACommentPrecedesTheStringTag.
var productNameStringPrefix = regexp.MustCompile(`^[^<]*<string>`)

// ProductName is the SystemProductName currently in plist, or "": the
// first line containing "<string>" after the key's line, with the prefix
// up to "<string>" and everything from "</string>" on removed.
func ProductName(plist []byte) string {
	lines := strings.Split(string(plist), "\n")
	want := false
	for _, line := range lines {
		if !want {
			if strings.Contains(line, "<key>SystemProductName</key>") {
				want = true
			}
			continue
		}
		if strings.Contains(line, "<string>") {
			s := line
			// Strip the prefix only when it actually matches;
			// otherwise leave the line's start untouched.
			if m := productNameStringPrefix.FindString(s); m != "" {
				s = s[len(m):]
			}
			// Always cut at "</string>", whether or not the prefix
			// matched.
			if i := strings.Index(s, "</string>"); i >= 0 {
				s = s[:i]
			}
			return s
		}
	}
	return ""
}

// SetProductName is plist with SystemProductName set to model, on a
// fresh slice: the input is not modified. A surgical edit, not a
// rewrite: one value changes, the bytes around it do not.
func SetProductName(plist []byte, model string) ([]byte, error) {
	if !SMBIOSWellformed(model) {
		return nil, fmt.Errorf("smbios: %q is not a usable SMBIOS model identifier (letters, digits, comma, dot, dash, underscore; 64 max)", model)
	}

	const keyLine = "<key>SystemProductName</key>"
	lines := strings.Split(strings.TrimSuffix(string(plist), "\n"), "\n")

	keys := 0
	for _, line := range lines {
		if strings.Contains(line, keyLine) {
			keys++
		}
	}
	if keys != 1 {
		return nil, fmt.Errorf("smbios: %d SystemProductName keys, expected 1; refusing to guess which one PlatformInfo reads", keys)
	}

	var out bytes.Buffer
	want := false
	done := false
	for _, line := range lines {
		switch {
		case !done && !want && strings.Contains(line, keyLine):
			// A key line that also carries its <string>: treating it
			// as the key alone would make the NEXT line that contains
			// <string> the one rewritten, which may belong to a
			// different key entirely, silently corrupting it. Refuse
			// instead.
			if strings.Contains(line, "<string>") {
				return nil, fmt.Errorf("smbios: SystemProductName's key and value share a line; refusing to edit a neighbouring key instead")
			}
			want = true
			out.WriteString(line)
			out.WriteByte('\n')
		case want && strings.Contains(line, "<string>"):
			// Keep the leading run of non-`<` characters, not the text
			// up to "<string>": anything from the first `<` onward (a
			// comment, the old tag, ...) is replaced wholesale.
			j := strings.IndexByte(line, '<')
			if j < 0 {
				j = len(line)
			}
			out.WriteString(line[:j])
			out.WriteString("<string>")
			out.WriteString(model)
			out.WriteString("</string>\n")
			want = false
			done = true
		default:
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	if !done {
		return nil, fmt.Errorf("smbios: no <string> after <key>SystemProductName</key>")
	}
	return out.Bytes(), nil
}
