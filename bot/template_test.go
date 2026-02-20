package bot

import "testing"

func TestTemplateFeaturesWork(t *testing.T) {
	ctx := map[string]any{
		"name":  "Alice",
		"items": []any{map[string]any{"name": "BTC"}, map[string]any{"name": "ETH"}},
		"empty": "",
	}
	tpl := "hi %name|X% %% %missing|d% %IF name%ok%END_IF%%IF empty%bad%END_IF% %EACH items%[%name%]%END_EACH%"
	if got := RenderTemplate(tpl, ctx); got != "hi Alice % d ok [BTC][ETH]" {
		t.Fatalf("unexpected output: %q", got)
	}
}

func TestTemplateEachWorksWithTypedSlicesAndIfChecksNonEmptySlice(t *testing.T) {
	ctx := map[string]any{
		"new_coins": []Coin{{Name: "Bitcoin", Symbol: "BTC", Rank: 1}, {Name: "Ethereum", Symbol: "ETH", Rank: 2}},
	}
	tpl := "%EACH new_coins%• #%rank% %name% (%symbol%)\n%END_EACH%%IF new_coins%HAS_NEW%END_IF%"
	got := RenderTemplate(tpl, ctx)
	want := "• #1 Bitcoin (BTC)\n• #2 Ethereum (ETH)\nHAS_NEW"
	if got != want {
		t.Fatalf("unexpected output: got %q want %q", got, want)
	}
}

func TestTemplateIfTreatsEmptySlicesAsFalse(t *testing.T) {
	ctx := map[string]any{"exited_coins": []Coin{}}
	tpl := "%IF exited_coins%📉 Exited:%END_IF%"
	if got := RenderTemplate(tpl, ctx); got != "" {
		t.Fatalf("unexpected output: %q", got)
	}
}
