package bot

import "testing"

func TestFormatTelegramHTML(t *testing.T) {
	input := "### **Market Context**\nUse <tags> & symbols"
	got := formatTelegramHTML(input)
	want := "### <b>Market Context</b>\nUse &lt;tags&gt; &amp; symbols"
	if got != want {
		t.Fatalf("unexpected output:\nwant: %q\ngot:  %q", want, got)
	}
}
