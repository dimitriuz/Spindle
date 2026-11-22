package art

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "cliamp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cliamp", "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestNavidromeFromConfig(t *testing.T) {
	cases := map[string]NavidromeConfig{
		"[navidrome]\nurl      = \"https://nd.lan/\"\nuser     = \"bob\"\npassword = \"s3cret!\"\n":                                                         {"https://nd.lan", "bob", "s3cret!"},
		"theme = \"x\"\n\n[plugins]\na = 1\n\n[navidrome]\nurl='http://h:4533'  # note\nuser = 'u'\npassword = 'pw with # hash'\n\n[other]\nurl = \"no\"\n": {"http://h:4533", "u", "pw with # hash"},
		"[navidrome]\nurl = \"https://x/music/\"\nuser = \"a\"\npassword = \"b\"":                                                                           {"https://x/music", "a", "b"},
	}
	for body, want := range cases {
		got, ok := NavidromeFromConfig(writeCfg(t, body))
		if !ok || got != want {
			t.Fatalf("%q:\n got %+v %v\nwant %+v", body, got, ok, want)
		}
	}
}

func TestNavidromeFromConfigNeedsAllThreeKeysInTheRightSection(t *testing.T) {
	for _, body := range []string{
		"",
		"[navidrome]\nurl = \"https://x\"\nuser = \"a\"\n",                 // no password
		"[navidrome]\nuser = \"a\"\npassword = \"b\"\n",                    // no url
		"url = \"https://x\"\nuser = \"a\"\npassword = \"b\"\n",            // not in the section
		"[spotify]\nurl = \"https://x\"\nuser = \"a\"\npassword = \"b\"\n", // another section
		"[navidrome]\nurl = \"\"\nuser = \"a\"\npassword = \"b\"\n",        // empty
		"[navidrome]\nurl = \"ftp://x\"\nuser = \"a\"\npassword = \"b\"\n", // not http(s)
	} {
		if c, ok := NavidromeFromConfig(writeCfg(t, body)); ok {
			t.Fatalf("%q must not configure anything, got %+v", body, c)
		}
	}
	if _, ok := NavidromeFromConfig(t.TempDir()); ok {
		t.Fatal("no config file")
	}
}
