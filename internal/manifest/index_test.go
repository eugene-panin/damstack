package manifest

import "testing"

// A template runs with missingkey=error, yet a setting an app may leave out is
// read with index, which gives nothing for a missing key: the userpass of an
// account set relies on it.
func TestAMissingSettingIsReadWithIndex(t *testing.T) {
	tmpl := `{{ $o := index .app "operators" }}{{ if not $o }}sauron-operators{{ else if ne (printf "%v" (index $o "from")) "db" }}{{ or (index $o "vault_mount") "sauron-operators" }}{{ end }}`
	for _, tc := range []struct {
		app  map[string]any
		want string
	}{
		{map[string]any{}, "sauron-operators"},
		{map[string]any{"operators": map[string]any{"from": "db"}}, ""},
		{map[string]any{"operators": map[string]any{"from": "vault", "vault_mount": "ops"}}, "ops"},
		{map[string]any{"operators": map[string]any{"vault_mount": "ops"}}, "ops"},
	} {
		got, err := RenderData("userpass", tmpl, map[string]any{"app": tc.app}, nil)
		if err != nil || got != tc.want {
			t.Errorf("%v: got %q, %v; want %q", tc.app, got, err, tc.want)
		}
	}
}
