// Package all registers every provider that ships in the binary. The core
// imports it for its side effects only, which is the single place a new
// in-tree provider has to be named.
package all

import (
	_ "openuai/internal/llm/providers/claude"
	_ "openuai/internal/llm/providers/openai"
)
