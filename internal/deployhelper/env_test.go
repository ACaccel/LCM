package deployhelper

import "testing"

func TestEnvSets(t *testing.T) {
	for data, want := range map[string]string{
		"PORT=1\n":                     "PORT",
		"  PORT = 1\n":                 "PORT",
		"\tHOST=0.0.0.0":               "HOST",
		"export PORT=1\n":              "PORT",
		"export\tHOST=x\n":             "HOST",
		`"PORT"=1`:                     "PORT",
		"'HOST'=1":                     "HOST",
		"port=1\n":                     "PORT",
		"A=1\r\nPORT=2\r\n":            "PORT",
		"A=x\\\nPORT=2\n":              "PORT",
		"PO\\\nRT=2\n":                 "PORT",
		"A=\"multi\nHOST=0.0.0.0\"\n":  "HOST",
		"SECRET=1\n# PORT=1\n; HOST=2": "",
		"PORTS=1\nHOSTNAME=x\n":        "",
		"exportPORT=1\n":               "",
		"":                             "",
	} {
		if got := envSets(data, "PORT", "HOST"); got != want {
			t.Errorf("%q: %q, want %q", data, got, want)
		}
	}
}
