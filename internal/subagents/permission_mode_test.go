package subagents

import "testing"

func TestParsePermissionMode(t *testing.T) {
	cases := map[string]string{"": "", "default": "", "manual": "", "auto": "", "bypassPermissions": "",
		"acceptEdits": "acceptEdits", "dontAsk": "dontAsk", "plan": "plan", "PLAN": "plan"}
	for in, want := range cases {
		src := "---\nname: rev\ndescription: x\n"
		if in != "" {
			src += "permissionMode: " + in + "\n"
		}
		d, err := Parse([]byte(src + "---\nbody"))
		if err != nil || d.PermissionMode != want {
			t.Fatalf("%q: got %q err=%v, want %q", in, d.PermissionMode, err, want)
		}
	}
	if _, err := Parse([]byte("---\nname: rev\ndescription: x\npermissionMode: yolo\n---\nbody")); err == nil {
		t.Fatal("invalid permissionMode must be rejected")
	}
}
