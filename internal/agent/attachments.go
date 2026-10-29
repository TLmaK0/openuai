package agent

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"openuai/internal/llm"
)

// AttachedFilesMarker separates what the user wrote from the description of
// the files attached to the message (which only the model needs to read).
const AttachedFilesMarker = "\n\n[Attached files]\n"

const (
	maxInlineFile  = 64 << 10  // text files up to this size are included
	maxInlineTotal = 200 << 10 // in the message itself, up to this total
)

// ComposeWithAttachments returns the user message the model sees: the text
// followed by every attached file (name, size, type and path, so it can be
// opened with tools) and the content of small text files.
func ComposeWithAttachments(text string, files []llm.Attachment) string {
	if len(files) == 0 {
		return text
	}
	var b strings.Builder
	b.WriteString(text)
	b.WriteString(AttachedFilesMarker)
	b.WriteString("The user attached these files; they are saved locally and can be opened with your tools from the given path.\n")
	for _, f := range files {
		fmt.Fprintf(&b, "- %s (%s, %s): %s\n", f.Name, humanSize(f.Size), mimeOr(f.Mime), f.Path)
	}
	inlined := 0
	for _, f := range files {
		if f.Size > maxInlineFile || inlined+int(f.Size) > maxInlineTotal {
			continue
		}
		data, err := os.ReadFile(f.Path)
		if err != nil || !isText(data) {
			continue
		}
		inlined += len(data)
		fence := "```"
		for strings.Contains(string(data), fence) {
			fence += "`"
		}
		fmt.Fprintf(&b, "\nContent of %s:\n%s\n%s\n%s\n", f.Name, fence, strings.TrimRight(string(data), "\n"), fence)
	}
	return b.String()
}

// DisplayContent is what the user wrote in m, without the attached-files part.
func DisplayContent(m llm.Message) string {
	c := m.Content
	for _, mk := range []string{SkillMarker, HookContextMarker} {
		if i := strings.Index(c, mk); i >= 0 {
			c = c[:i]
		}
	}
	if len(m.Attachments) > 0 {
		if i := strings.Index(c, AttachedFilesMarker); i >= 0 {
			c = c[:i]
		}
	}
	return c
}

// BeginWithAttachments is Begin with images and files attached to the user's
// message. Both are saved with the conversation.
func (a *Agent) BeginWithAttachments(userMessage string, images []string, files []llm.Attachment) {
	a.messages = append(a.messages, llm.Message{
		Role: llm.RoleUser, Content: ComposeWithAttachments(userMessage, files), Images: images, Attachments: files,
	})
	a.runInProgress = true
	a.resumePending = false
	a.clearTurn() // skill grants and overrides last until the next message
}

// UserMessageAttachments returns the files of the n-th user message (0-based,
// counted like RewindToUserMessage), so an edited message keeps them.
func (a *Agent) UserMessageAttachments(n int) []llm.Attachment {
	count := 0
	for _, m := range a.messages {
		if m.Role == llm.RoleUser {
			if count == n {
				return m.Attachments
			}
			count++
		}
	}
	return nil
}

func isText(data []byte) bool {
	head := data
	if len(head) > 8192 {
		head = head[:8192]
	}
	for _, c := range head {
		if c == 0 {
			return false
		}
	}
	return utf8.Valid(data)
}

func mimeOr(m string) string {
	if m == "" {
		return "unknown type"
	}
	return m
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
