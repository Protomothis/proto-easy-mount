package notify

import (
	"os"
	"strings"
	"testing"
)

// Text is escaped into the toast XML and never reaches the script or the
// command line.
func TestToastKeepsTextOutOfTheScript(t *testing.T) {
	title, text := `<b>"X:"</b>`, `$(Remove-Item C:) & '@`
	cmd := command(title, text)
	for _, a := range cmd.Args {
		if strings.Contains(a, "Remove-Item") || strings.Contains(a, "<b>") {
			t.Fatalf("text reached the command line: %q", a)
		}
	}
	var doc string
	for _, e := range cmd.Env {
		if v, ok := strings.CutPrefix(e, xmlEnv+"="); ok {
			doc = v
		}
	}
	if !strings.Contains(doc, "&lt;b&gt;&#34;X:&#34;&lt;/b&gt;") || !strings.Contains(doc, "&amp; &#39;@") {
		t.Fatalf("toast XML = %s", doc)
	}
}

// PEM_TOAST_LIVE=1 shows a real toast through PowerShell.
func TestToastLive(t *testing.T) {
	if os.Getenv("PEM_TOAST_LIVE") == "" {
		t.Skip("set PEM_TOAST_LIVE=1")
	}
	if out, err := command("Proto Easy Mount 테스트", "알림이 보이면 정상이에요.").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}
