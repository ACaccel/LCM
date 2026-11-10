package hostsetup

import "testing"

// TestUIDMinReadsALoginDefsFile parses text in the layout of a distribution
// file: tab-separated values, commented defaults and similar names.
func TestUIDMinReadsALoginDefsFile(t *testing.T) {
	const ubuntu = "# Min/max values for automatic uid selection in useradd(8)\n#\nUID_MIN\t\t\t 1000\nUID_MAX\t\t\t60000\n" +
		"# System accounts\nSYS_UID_MIN\t\t  100\nSYS_UID_MAX\t\t  999\n"
	for name, tc := range map[string]struct {
		file string
		want int
	}{
		"distribution file": {ubuntu, 1000},
		"custom value":      {"SYS_UID_MIN 100\nUID_MIN\t\t500\n", 500},
		"trailing comment":  {"UID_MIN 700 # local\n", 700},
		"commented default": {"#UID_MIN 500\nSYS_UID_MIN 100\n", 1000},
		"unparsable value":  {"UID_MIN lots\n", 1000},
		"no UID_MIN":        {"SYS_UID_MIN 100\n", 1000},
		"empty":             {"", 1000},
	} {
		fsys := newMemFS()
		fsys.file("/etc/login.defs", tc.file, 0o644, 0, 0)
		if got := uidMin(fsys); got != tc.want {
			t.Errorf("%s: uidMin = %d, want %d", name, got, tc.want)
		}
	}
}

// TestParsePasswdFeedsTheAccountCheck chains the parser into the account
// check with passwd-format text as useradd leaves it.
func TestParsePasswdFeedsTheAccountCheck(t *testing.T) {
	acc, ok, err := ParsePasswd("lcmb-web:x:996:995:LCM build web:/var/lib/lcm-build/web:/usr/sbin/nologin\n", "lcmb-web")
	want := Account{Name: "lcmb-web", UID: 996, GID: 995, Home: "/var/lib/lcm-build/web", GECOS: "LCM build web", Shell: "/usr/sbin/nologin"}
	if err != nil || !ok || acc != want {
		t.Fatalf("%+v %v %v", acc, ok, err)
	}
	if p := systemAccountProblem(acc, want.Home, BuildComment("web"), 1000); p != "" {
		t.Fatalf("parsed account refused: %s", p)
	}
	other, _, err := ParsePasswd("lcmb-web:x:996:995:someone else:/var/lib/lcm-build/web:/usr/sbin/nologin\n", "lcmb-web")
	if err != nil || systemAccountProblem(other, want.Home, BuildComment("web"), 1000) == "" {
		t.Fatalf("an account with another comment was accepted: %v", err)
	}
}
