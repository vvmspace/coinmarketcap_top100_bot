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
