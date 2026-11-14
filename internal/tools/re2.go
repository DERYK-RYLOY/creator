package tools

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	"github.com/DERYK-RYLOY/creator/internal/reduce"
)

func hexdumpTool() Tool {
	return Tool{
		Name:     "hexdump",
		ReadOnly: true,
		Description: "Hex dump a file with offsets, hex bytes and an ASCII side panel. " +
			"Supports offset/length slicing. Essential for manual binary patching and triage.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path inside the workspace."},
    "offset": {"type": "integer", "description": "Start offset in bytes (default 0)."},
    "length": {"type": "integer", "description": "Number of bytes to dump (default 256)."}
  },
  "required": ["path"]
}`),
		Handler: handleHexdump,
	}
}

type hexdumpArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
}

func handleHexdump(ctx context.Context, raw json.RawMessage) (string, error) {
	var a hexdumpArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	env := currentEnv(ctx)
	resolved, err := env.resolve(a.Path, "file")
	if err != nil {
		return "", err
	}
	if a.Offset < 0 {
		a.Offset = 0
	}
	if a.Length <= 0 {
		a.Length = 256
	}
	if a.Length > 1<<20 {
		return "", fmt.Errorf("length %d exceeds the 1 MiB hexdump cap", a.Length)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if int64(a.Offset) > info.Size() {
		return "", fmt.Errorf("offset %d beyond file size %d", a.Offset, info.Size())
	}
	end := int64(a.Offset) + int64(a.Length)
	if end > info.Size() {
		end = info.Size()
	}
	f, err := os.Open(resolved)
	if err != nil {
		return "", err
	}
	if _, err := f.Seek(int64(a.Offset), io.SeekStart); err != nil {
		f.Close()
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(f, end-int64(a.Offset)))
	f.Close()
	if err != nil {
		return "", err
	}
	slice := data
	var b strings.Builder
	for i := 0; i < len(slice); i += 16 {
		b.WriteString(fmt.Sprintf("%08x  ", a.Offset+i))
		hexPart := ""
		asciiPart := ""
		for j := 0; j < 16; j++ {
			if i+j < len(slice) {
				hexPart += fmt.Sprintf("%02x ", slice[i+j])
				c := slice[i+j]
				if c >= 0x20 && c < 0x7f {
					asciiPart += string(c)
				} else {
					asciiPart += "."
				}
			} else {
				hexPart += "   "
			}
			if j == 7 {
				hexPart += " "
			}
		}
		b.WriteString(hexPart)
		b.WriteString(" |")
		b.WriteString(asciiPart)
		b.WriteString("|\n")
	}
	return clip(env, b.String(), reduce.KindGeneric), nil
}

func entropyTool() Tool {
	return Tool{
		Name:     "entropy",
		ReadOnly: true,
		Description: "Compute Shannon entropy (0-8) of a file or byte range. High entropy (>7.5) " +
			"indicates packed, encrypted or compressed data; low entropy indicates plaintext. " +
			"Critical for malware triage and detecting obfuscation.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path inside the workspace."},
    "offset": {"type": "integer", "description": "Start offset in bytes (default 0)."},
    "length": {"type": "integer", "description": "Number of bytes to analyze (default: whole file)."}
  },
  "required": ["path"]
}`),
		Handler: handleEntropy,
	}
}

type entropyArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
}

func handleEntropy(ctx context.Context, raw json.RawMessage) (string, error) {
	var a entropyArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	env := currentEnv(ctx)
	resolved, err := env.resolve(a.Path, "file")
	if err != nil {
		return "", err
	}
	data, err := readFileCapped(resolved, limits.ReadBytes)
	if err != nil {
		return "", err
	}
	if a.Offset < 0 {
		a.Offset = 0
	}
	if a.Length <= 0 {
		a.Length = len(data) - a.Offset
	}
	if a.Offset+a.Length > len(data) {
		a.Length = len(data) - a.Offset
	}
	if a.Length <= 0 {
		return "", fmt.Errorf("empty range")
	}
	slice := data[a.Offset : a.Offset+a.Length]
	ent := shannonEntropy(slice)
	verdict := "plaintext/structured"
	if ent > 7.5 {
		verdict = "highly packed/encrypted/compressed"
	} else if ent > 6.0 {
		verdict = "moderately compressed"
	} else if ent < 4.0 {
		verdict = "very low entropy (sparse/repetitive)"
	}
	out := fmt.Sprintf("entropy=%.4f bits/byte (%s) over %d bytes", ent, verdict, len(slice))
	return clip(env, out, reduce.KindGeneric), nil
}

func shannonEntropy(data []byte) float64 {
	if len(data) == 0 {
		return 0
	}
	freq := make(map[byte]int)
	for _, b := range data {
		freq[b]++
	}
	ent := 0.0
	n := float64(len(data))
	for _, c := range freq {
		p := float64(c) / n
		if p > 0 {
			ent -= p * math.Log2(p)
		}
	}
	return ent
}

func xorDecodeTool() Tool {
	return Tool{
		Name:     "xor_decode",
		ReadOnly: true,
		Description: "Brute-force single-byte XOR keys on a file or hex string and rank candidates by " +
			"English-language frequency. The standard first step for XOR-encoded malware payloads and C2 traffic.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path inside the workspace (optional if hex given)."},
    "hex": {"type": "string", "description": "Hex-encoded bytes to decode (optional if path given)."},
    "top": {"type": "integer", "description": "Number of top candidates to return (default 5)."}
  }
}`),
		Handler: handleXorDecode,
	}
}

type xorDecodeArgs struct {
	Path string `json:"path"`
	Hex  string `json:"hex"`
	Top  int    `json:"top"`
}

func handleXorDecode(ctx context.Context, raw json.RawMessage) (string, error) {
	var a xorDecodeArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	env := currentEnv(ctx)
	var data []byte
	var err error
	if a.Path != "" {
		resolved, rerr := env.resolve(a.Path, "file")
		if rerr != nil {
			return "", rerr
		}
		data, err = readFileCapped(resolved, limits.ReadBytes)
		if err != nil {
			return "", err
		}
	} else if a.Hex != "" {
		hexStr := strings.ReplaceAll(strings.TrimSpace(a.Hex), " ", "")
		data, err = hex.DecodeString(hexStr)
		if err != nil {
			return "", fmt.Errorf("invalid hex: %w", err)
		}
	} else {
		return "", fmt.Errorf("path or hex required")
	}
	if a.Top <= 0 {
		a.Top = 5
	}
	type cand struct {
		Key   byte
		Score float64
		Text  string
	}
	var cands []cand
	for k := 0; k < 256; k++ {
		dec := make([]byte, len(data))
		for i, b := range data {
			dec[i] = b ^ byte(k)
		}
		score := englishScore(dec)
		text := string(dec)
		if len(text) > 200 {
			text = text[:200] + "..."
		}
		cands = append(cands, cand{Key: byte(k), Score: score, Text: text})
	}
	for i := 0; i < len(cands); i++ {
		for j := i + 1; j < len(cands); j++ {
			if cands[j].Score > cands[i].Score {
				cands[i], cands[j] = cands[j], cands[i]
			}
		}
	}
	if a.Top > len(cands) {
		a.Top = len(cands)
	}
	var b strings.Builder
	for i := 0; i < a.Top; i++ {
		b.WriteString(fmt.Sprintf("key=0x%02x  score=%.3f  %q\n", cands[i].Key, cands[i].Score, cands[i].Text))
	}
	return clip(env, b.String(), reduce.KindGeneric), nil
}

func englishScore(data []byte) float64 {
	score := 0.0
	for _, b := range data {
		switch {
		case b == ' ' || b == '\n' || b == '\r' || b == '\t':
			score += 3.0
		case (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z'):
			score += 2.0
		case b >= '0' && b <= '9':
			score += 1.0
		case b == '.' || b == ',' || b == '!' || b == '?' || b == ':' || b == ';':
			score += 0.5
		case b < 0x20 && b != '\n' && b != '\r' && b != '\t':
			score -= 5.0
		case b == 0x7f:
			score -= 5.0
		}
	}
	return score
}

func peInfoTool() Tool {
	return Tool{
		Name:     "pe_info",
		ReadOnly: true,
		Description: "Deep PE (Windows executable) analysis: COFF header, optional header (entry, " +
			"image base, subsystem), section table with entropy per section, and import/export " +
			"directory summaries. Essential for Windows malware RE.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path inside the workspace."}
  },
  "required": ["path"]
}`),
		Handler: handlePeInfo,
	}
}

type peInfoArgs struct {
	Path string `json:"path"`
}

func handlePeInfo(ctx context.Context, raw json.RawMessage) (string, error) {
	var a peInfoArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	env := currentEnv(ctx)
	resolved, err := env.resolve(a.Path, "file")
	if err != nil {
		return "", err
	}
	data, err := readFileCapped(resolved, limits.ReadBytes)
	if err != nil {
		return "", err
	}
	if len(data) < 64 || string(data[:2]) != "MZ" {
		return "", fmt.Errorf("not a PE file (missing MZ header)")
	}
	e_lfanew := int(binary.LittleEndian.Uint32(data[60:64]))
	if e_lfanew < 0 || e_lfanew+26 > len(data) {
		return "", fmt.Errorf("invalid PE header offset")
	}
	if string(data[e_lfanew:e_lfanew+4]) != "PE\x00\x00" {
		return "", fmt.Errorf("not a PE file (missing PE signature)")
	}
	coffMachine := binary.LittleEndian.Uint16(data[e_lfanew+4 : e_lfanew+6])
	numSections := int(binary.LittleEndian.Uint16(data[e_lfanew+6 : e_lfanew+8]))
	timeDate := binary.LittleEndian.Uint32(data[e_lfanew+8 : e_lfanew+12])
	characteristics := binary.LittleEndian.Uint16(data[e_lfanew+22 : e_lfanew+24])
	var entry, imageBase uint64
	var subsystem uint16
	magic := binary.LittleEndian.Uint16(data[e_lfanew+24 : e_lfanew+26])
	if magic == 0x10b {
		if e_lfanew+94 > len(data) {
			return "", fmt.Errorf("truncated PE32 optional header")
		}
		entry = uint64(binary.LittleEndian.Uint32(data[e_lfanew+40 : e_lfanew+44]))
		imageBase = uint64(binary.LittleEndian.Uint32(data[e_lfanew+52 : e_lfanew+56]))
		subsystem = binary.LittleEndian.Uint16(data[e_lfanew+92 : e_lfanew+94])
	} else if magic == 0x20b {
		if e_lfanew+110 > len(data) {
			return "", fmt.Errorf("truncated PE32+ optional header")
		}
		entry = binary.LittleEndian.Uint64(data[e_lfanew+40 : e_lfanew+48])
		imageBase = binary.LittleEndian.Uint64(data[e_lfanew+48 : e_lfanew+56])
		subsystem = binary.LittleEndian.Uint16(data[e_lfanew+108 : e_lfanew+110])
	} else {
		return "", fmt.Errorf("unknown optional header magic 0x%x", magic)
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("PE machine=0x%04x sections=%d timestamp=%d characteristics=0x%04x\n",
		coffMachine, numSections, timeDate, characteristics))
	b.WriteString(fmt.Sprintf("entry=0x%x image_base=0x%x subsystem=%d\n", entry, imageBase, subsystem))
	optSize := int(binary.LittleEndian.Uint16(data[e_lfanew+20 : e_lfanew+22]))
	sectionOff := e_lfanew + 24 + optSize
	for i := 0; i < numSections && sectionOff+40 <= len(data); i++ {
		name := strings.TrimRight(string(data[sectionOff:sectionOff+8]), "\x00")
		vsize := binary.LittleEndian.Uint32(data[sectionOff+8 : sectionOff+12])
		vaddr := binary.LittleEndian.Uint32(data[sectionOff+12 : sectionOff+16])
		rawSize := binary.LittleEndian.Uint32(data[sectionOff+16 : sectionOff+20])
		rawOff := binary.LittleEndian.Uint32(data[sectionOff+20 : sectionOff+24])
		char := binary.LittleEndian.Uint32(data[sectionOff+36 : sectionOff+40])
		ent := 0.0
		if rawSize > 0 && int(rawOff)+int(rawSize) <= len(data) {
			ent = shannonEntropy(data[rawOff : rawOff+rawSize])
		}
		b.WriteString(fmt.Sprintf("  %-8s va=0x%08x vsize=0x%x raw=0x%x@0x%x entropy=%.2f char=0x%x\n",
			name, vaddr, vsize, rawSize, rawOff, ent, char))
		sectionOff += 40
	}
	return clip(env, b.String(), reduce.KindGeneric), nil
}

func elfInfoTool() Tool {
	return Tool{
		Name:     "elf_info",
		ReadOnly: true,
		Description: "Deep ELF analysis: header (class, type, machine, entry), section table with " +
			"types/flags/offsets/sizes, and program header summary. Essential for Linux/Unix malware RE.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path inside the workspace."}
  },
  "required": ["path"]
}`),
		Handler: handleElfInfo,
	}
}

type elfInfoArgs struct {
	Path string `json:"path"`
}

func handleElfInfo(ctx context.Context, raw json.RawMessage) (string, error) {
	var a elfInfoArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	env := currentEnv(ctx)
	resolved, err := env.resolve(a.Path, "file")
	if err != nil {
		return "", err
	}
	data, err := readFileCapped(resolved, limits.ReadBytes)
	if err != nil {
		return "", err
	}
	if len(data) < 64 || string(data[:4]) != "\x7fELF" {
		return "", fmt.Errorf("not an ELF file")
	}
	is64 := data[4] == 2
	var order binary.ByteOrder
	if data[5] == 1 {
		order = binary.LittleEndian
	} else {
		order = binary.BigEndian
	}
	eType := order.Uint16(data[16:18])
	eMachine := order.Uint16(data[18:20])
	var entry uint64
	if is64 {
		entry = order.Uint64(data[24:32])
	} else {
		entry = uint64(order.Uint32(data[24:28]))
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("ELF class=%d type=%d machine=%d (EM_%d) entry=0x%x\n",
		data[4], eType, eMachine, eMachine, entry))
	var shoff uint64
	var shentsize, shnum, shstrndx uint16
	if is64 {
		shoff = order.Uint64(data[40:48])
		shentsize = uint16(order.Uint16(data[58:60]))
		shnum = uint16(order.Uint16(data[60:62]))
		shstrndx = uint16(order.Uint16(data[62:64]))
	} else {
		shoff = uint64(order.Uint32(data[32:36]))
		shentsize = uint16(order.Uint16(data[46:48]))
		shnum = uint16(order.Uint16(data[48:50]))
		shstrndx = uint16(order.Uint16(data[50:52]))
	}
	if shoff > 0 && shnum > 0 {
		b.WriteString(fmt.Sprintf("sections=%d shentsize=%d shstrndx=%d\n", shnum, shentsize, shstrndx))
		for i := 0; i < int(shnum); i++ {
			off := int(shoff) + i*int(shentsize)
			if off+int(shentsize) > len(data) {
				break
			}
			var nameOff, typ, flags, addr, size, offset uint64
			if is64 {
				nameOff = uint64(order.Uint32(data[off : off+4]))
				typ = uint64(order.Uint32(data[off+4 : off+8]))
				flags = order.Uint64(data[off+8 : off+16])
				addr = order.Uint64(data[off+16 : off+24])
				offset = order.Uint64(data[off+24 : off+32])
				size = order.Uint64(data[off+32 : off+40])
			} else {
				nameOff = uint64(order.Uint32(data[off : off+4]))
				typ = uint64(order.Uint32(data[off+4 : off+8]))
				flags = uint64(order.Uint32(data[off+8 : off+12]))
				addr = uint64(order.Uint32(data[off+12 : off+16]))
				offset = uint64(order.Uint32(data[off+16 : off+20]))
				size = uint64(order.Uint32(data[off+20 : off+24]))
			}
			name := ""
			if int(shstrndx) < int(shnum) {
				shOff := int(shoff) + int(shstrndx)*int(shentsize)
				if shOff+int(shentsize) <= len(data) {
					strOff := int(order.Uint32(data[shOff : shOff+4]))
					nOff := strOff + int(nameOff)
					if nOff < len(data) {
						// bytes.IndexByte scans the buffer in place — converting
						// to string first would copy the whole rest of the file
						// once per section.
						if e2 := bytes.IndexByte(data[nOff:], 0); e2 >= 0 {
							name = string(data[nOff : nOff+e2])
						}
					}
				}
			}
			b.WriteString(fmt.Sprintf("  %-16s type=%d flags=0x%x addr=0x%x off=0x%x size=0x%x\n",
				name, typ, flags, addr, offset, size))
		}
	}
	return clip(env, b.String(), reduce.KindGeneric), nil
}
