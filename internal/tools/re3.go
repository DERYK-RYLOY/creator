package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/DERYK-RYLOY/creator/internal/reduce"
)

func rot13Tool() Tool {
	return Tool{
		Name:        "rot13",
		ReadOnly:    true,
		Description: "ROT13-decode a string. Quick triage of encoded strings.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "text": {"type": "string", "description": "Text to ROT13-decode."}
  },
  "required": ["text"]
}`),
		Handler: handleRot13,
	}
}

type rot13Args struct {
	Text string `json:"text"`
}

func handleRot13(ctx context.Context, raw json.RawMessage) (string, error) {
	var a rot13Args
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	env := currentEnv(ctx)
	var out strings.Builder
	for _, c := range a.Text {
		if c >= 'A' && c <= 'Z' {
			c = ((c - 'A' + 13) % 26) + 'A'
		} else if c >= 'a' && c <= 'z' {
			c = ((c - 'a' + 13) % 26) + 'a'
		}
		out.WriteRune(c)
	}
	return clip(env, out.String(), reduce.KindGeneric), nil
}

func hexToAsciiTool() Tool {
	return Tool{
		Name:        "hex_to_ascii",
		ReadOnly:    true,
		Description: "Convert a hex string to ASCII. Quick decoding for hex-encoded payloads.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "hex": {"type": "string", "description": "Hex string (spaces allowed)."}
  },
  "required": ["hex"]
}`),
		Handler: handleHexToAscii,
	}
}

type hexToAsciiArgs struct {
	Hex string `json:"hex"`
}

func handleHexToAscii(ctx context.Context, raw json.RawMessage) (string, error) {
	var a hexToAsciiArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	env := currentEnv(ctx)
	h := strings.ReplaceAll(strings.TrimSpace(a.Hex), " ", "")
	if len(h)%2 != 0 {
		return "", fmt.Errorf("hex string has a dangling nibble (%d hex digits)", len(h))
	}
	var out strings.Builder
	for i := 0; i+1 < len(h); i += 2 {
		b, err := parseHexByte(h[i : i+2])
		if err != nil {
			return "", err
		}
		out.WriteByte(b)
	}
	return clip(env, out.String(), reduce.KindGeneric), nil
}

func parseHexByte(s string) (byte, error) {
	var b byte
	for _, c := range s {
		b <<= 4
		switch {
		case c >= '0' && c <= '9':
			b |= byte(c - '0')
		case c >= 'a' && c <= 'f':
			b |= byte(c-'a') + 10
		case c >= 'A' && c <= 'F':
			b |= byte(c-'A') + 10
		default:
			return 0, fmt.Errorf("invalid hex char %q", c)
		}
	}
	return b, nil
}

func base64DecodeTool() Tool {
	return Tool{
		Name:        "base64_decode",
		ReadOnly:    true,
		Description: "Base64-decode a string. Useful for decoding encoded payloads and C2 traffic.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "text": {"type": "string", "description": "Base64-encoded text."}
  },
  "required": ["text"]
}`),
		Handler: handleBase64Decode,
	}
}

type base64DecodeArgs struct {
	Text string `json:"text"`
}

func handleBase64Decode(ctx context.Context, raw json.RawMessage) (string, error) {
	var a base64DecodeArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	env := currentEnv(ctx)
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(a.Text))
	if err != nil {
		// try raw
		data, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(a.Text))
		if err != nil {
			return "", fmt.Errorf("invalid base64: %w", err)
		}
	}
	return clip(env, string(data), reduce.KindGeneric), nil
}

func caesarShiftTool() Tool {
	return Tool{
		Name:        "caesar_shift",
		ReadOnly:    true,
		Description: "Caesar cipher shift. Try shifts 1-25 for quick crypto triage.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "text": {"type": "string", "description": "Text to shift."},
    "shift": {"type": "integer", "description": "Shift amount (default 3)."}
  },
  "required": ["text"]
}`),
		Handler: handleCaesarShift,
	}
}

type caesarShiftArgs struct {
	Text  string `json:"text"`
	Shift int    `json:"shift"`
}

func handleCaesarShift(ctx context.Context, raw json.RawMessage) (string, error) {
	var a caesarShiftArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	env := currentEnv(ctx)
	if a.Shift == 0 {
		a.Shift = 3
	}
	shift := ((a.Shift % 26) + 26) % 26
	var out strings.Builder
	for _, c := range a.Text {
		if c >= 'A' && c <= 'Z' {
			c = ((c-'A')+rune(shift))%26 + 'A'
		} else if c >= 'a' && c <= 'z' {
			c = ((c-'a')+rune(shift))%26 + 'a'
		}
		out.WriteRune(c)
	}
	return clip(env, out.String(), reduce.KindGeneric), nil
}

// writeFileTool is a convenience for writing decoded payloads to disk during
// RE workflows (e.g., after decoding an XOR payload).
func writeFileTool() Tool {
	return Tool{
		Name:     "write_file",
		ReadOnly: false,
		Description: "Write decoded/extracted bytes to a file. Use after decoding payloads " +
			"(XOR, base64, hex) to save the result for further analysis.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path inside the workspace."},
    "content": {"type": "string", "description": "Content to write."}
  },
  "required": ["path", "content"]
}`),
		Handler: handleWriteFile,
	}
}

type writeFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func handleWriteFile(ctx context.Context, raw json.RawMessage) (string, error) {
	var a writeFileArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	env := currentEnv(ctx)
	resolved, err := env.resolve(a.Path, "")
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(resolved); err == nil && info.IsDir() {
		return "", fmt.Errorf("%s is a directory", a.Path)
	}
	note := checkpointForMutation(env, resolved, "write_file")
	if err := writeFilePreserveMode(env, resolved, []byte(a.Content)); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %s (%d bytes)%s", env.rel(resolved), len(a.Content), note), nil
}
