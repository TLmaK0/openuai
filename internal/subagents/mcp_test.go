package subagents

import "testing"

func TestParseMCPServers(t *testing.T) {
	src := `---
name: browser
description: x
mcpServers:
  - github
  - playwright:
      type: stdio
      command: npx
      args: ["-y", "@playwright/mcp@latest"]
  - remote:
      url: https://example.com/mcp
disallowedTools: mcp_github_delete*
---
body`
	d, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.MCPServers) != 3 {
		t.Fatalf("got %+v", d.MCPServers)
	}
	if d.MCPServers[0].Name != "github" || d.MCPServers[0].Inline != nil {
		t.Fatalf("reference: %+v", d.MCPServers[0])
	}
	p := d.MCPServers[1].Inline
	if p == nil || p.Command != "npx" || len(p.Args) != 2 || p.Name != "playwright" {
		t.Fatalf("inline stdio: %+v", p)
	}
	if r := d.MCPServers[2].Inline; r == nil || r.URL != "https://example.com/mcp" {
		t.Fatalf("inline http: %+v", r)
	}
	if !Disallowed(d, "mcp_github_delete_repo") || Disallowed(d, "mcp_github_get_issue") {
		t.Fatal("Disallowed")
	}
	for _, bad := range []string{
		"mcpServers:\n  - x:\n      type: ws\n      url: ws://a\n",
		"mcpServers:\n  - x:\n      type: stdio\n",
		"mcpServers:\n  - 'bad name'\n",
	} {
		if _, err := Parse([]byte("---\nname: a\ndescription: x\n" + bad + "---\nbody")); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}
