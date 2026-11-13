package tui

// roleAssignments are the audited role-level values for every built-in theme.
// Palette literals are preserved where they meet WCAG contrast; **D** values
// from the audit are HSL-preserving lifts or neutral paper/tinted derivations.
// This is data, not rendering logic.
type themeRoles struct {
	Bg           string
	Surface      string
	Surface2     string
	Fg           string
	Dim          string
	Muted        string
	Accent1      string
	Accent2      string
	Success      string
	Warning      string
	Error        string
	DiffAddBg    string
	DiffAddFg    string
	DiffRemoveBg string
	DiffRemoveFg string
	Thinking     string
	ToolBorder   string
	SelectionBg  string
	SelectionFg  string
	StatusBg     string
	StatusFg     string
}

var roleAssignments = map[string]themeRoles{
	"volcanic-ember-glacial-slate": {
		Bg: "#120d0d", Surface: "#162b30", Surface2: "#293333",
		Fg: "#ebcb94", Dim: "#d0ab73", Muted: "#5c8188",
		Accent1: "#ee8f2b", Accent2: "#5c8188",
		Success: "#3fb950", Warning: "#d16e13", Error: "#cb5713",
		DiffAddBg: "#1a2a18", DiffAddFg: "#3fb950", DiffRemoveBg: "#311a0e", DiffRemoveFg: "#df5f15",
		Thinking: "#5c8188", ToolBorder: "#4d6c72", SelectionBg: "#75481a", SelectionFg: "#ffffff",
		StatusBg: "#293333", StatusFg: "#d0ab73",
	},
	"sunlit-cove-copper-reef": {
		Bg: "#0d374d", Surface: "#13526e", Surface2: "#28586c",
		Fg: "#f7d14c", Dim: "#a49d51", Muted: "#969d95",
		Accent1: "#ed922b", Accent2: "#3ea6cb",
		Success: "#3fb950", Warning: "#f4b031", Error: "#ef7b32",
		DiffAddBg: "#164d4e", DiffAddFg: "#5cc86b", DiffRemoveBg: "#334348", DiffRemoveFg: "#f2955a",
		Thinking: "#3ea6cb", ToolBorder: "#2f8daf", SelectionBg: "#72603e", SelectionFg: "#ffffff",
		StatusBg: "#28586c", StatusFg: "#a49d51",
	},
	"cyberpunk-blaze-abyssal-neon": {
		Bg: "#0d0c12", Surface: "#161a29", Surface2: "#28242f",
		Fg: "#f4ede1", Dim: "#98938e", Muted: "#58845b",
		Accent1: "#e92e14", Accent2: "#1280c1",
		Success: "#3fb950", Warning: "#eeaa09", Error: "#ec2d34",
		DiffAddBg: "#16291d", DiffAddFg: "#3fb950", DiffRemoveBg: "#331218", DiffRemoveFg: "#ee454b",
		Thinking: "#1280c1", ToolBorder: "#0e6599", SelectionBg: "#701b13", SelectionFg: "#ffffff",
		StatusBg: "#28242f", StatusFg: "#98938e",
	},
	"toxic-venom-obsidian-moss": {
		Bg: "#0d0d0e", Surface: "#1e1e20", Surface2: "#2c2c2c",
		Fg: "#c3e201", Dim: "#7a8d06", Muted: "#7a7a7a",
		Accent1: "#c3e201", Accent2: "#819e02",
		Success: "#a0bf01", Warning: "#d29922", Error: "#f85149",
		DiffAddBg: "#262b0c", DiffAddFg: "#a0bf01", DiffRemoveBg: "#351918", DiffRemoveFg: "#f85149",
		Thinking: "#819e02", ToolBorder: "#819e02", SelectionBg: "#5f6d08", SelectionFg: "#ffffff",
		StatusBg: "#2c2c2c", StatusFg: "#7a8d06",
	},
	"ancient-parchment-roasted-espresso": {
		Bg: "#0a0706", Surface: "#2a1911", Surface2: "#362f27",
		Fg: "#bab4a4", Dim: "#a79b8a", Muted: "#897b6a",
		Accent1: "#a6936f", Accent2: "#958872",
		Success: "#3fb950", Warning: "#a6936f", Error: "#f85149",
		DiffAddBg: "#132513", DiffAddFg: "#3fb950", DiffRemoveBg: "#321411", DiffRemoveFg: "#f85149",
		Thinking: "#958872", ToolBorder: "#958872", SelectionBg: "#504635", SelectionFg: "#ffffff",
		StatusBg: "#362f27", StatusFg: "#a79b8a",
	},
	"nordic-fog-slate-mist": {
		Bg: "#0d1116", Surface: "#1a1d25", Surface2: "#24272f",
		Fg: "#e6e2d5", Dim: "#c4bdb4", Muted: "#94918b",
		Accent1: "#d5ccbc", Accent2: "#6f7e8a",
		Success: "#3fb950", Warning: "#d29922", Error: "#f85149",
		DiffAddBg: "#162e20", DiffAddFg: "#3fb950", DiffRemoveBg: "#351c1f", DiffRemoveFg: "#f85149",
		Thinking: "#6f7e8a", ToolBorder: "#57626c", SelectionBg: "#676561", SelectionFg: "#ffffff",
		StatusBg: "#24272f", StatusFg: "#c4bdb4",
	},
	"midnight-violet-molten-ember": {
		Bg: "#010102", Surface: "#0e1328", Surface2: "#2d0530",
		Fg: "#efe1d2", Dim: "#90877f", Muted: "#7a746d",
		Accent1: "#d25010", Accent2: "#cd0fce",
		Success: "#3fb950", Warning: "#d29922", Error: "#f85149",
		DiffAddBg: "#0c200f", DiffAddFg: "#3fb950", DiffRemoveBg: "#2b0f0e", DiffRemoveFg: "#f85149",
		Thinking: "#cd0fce", ToolBorder: "#9f0ba0", SelectionBg: "#5f2508", SelectionFg: "#ffffff",
		StatusBg: "#2d0530", StatusFg: "#90877f",
	},
	"electric-rainforest-mint-frost": {
		Bg: "#155049", Surface: "#0e3934", Surface2: "#2c625b",
		Fg: "#e7fee9", Dim: "#ceebd5", Muted: "#91d623",
		Accent1: "#c8fc31", Accent2: "#65cd1a",
		Success: "#91d623", Warning: "#e1ad43", Error: "#fb9c97",
		DiffAddBg: "#2a6743", DiffAddFg: "#aee459", DiffRemoveBg: "#3c5d56", DiffRemoveFg: "#fcbcb8",
		Thinking: "#65cd1a", ToolBorder: "#54aa16", SelectionBg: "#427b43", SelectionFg: "#ffffff",
		StatusBg: "#2c625b", StatusFg: "#ceebd5",
	},
	"shadowed-pine-jade-moss": {
		Bg: "#090a08", Surface: "#19221b", Surface2: "#23231c",
		Fg: "#8f8f7a", Dim: "#787a67", Muted: "#787a66",
		Accent1: "#298f70", Accent2: "#308767",
		Success: "#298f70", Warning: "#d29922", Error: "#f85149",
		DiffAddBg: "#0e211a", DiffAddFg: "#2b9574", DiffRemoveBg: "#321613", DiffRemoveFg: "#f85149",
		Thinking: "#308767", ToolBorder: "#276e54", SelectionBg: "#174637", SelectionFg: "#ffffff",
		StatusBg: "#23231c", StatusFg: "#787a67",
	},
	"bioluminescent-abyss-electric-lime": {
		Bg: "#000103", Surface: "#01102c", Surface2: "#01324c",
		Fg: "#b4fc71", Dim: "#6c9845", Muted: "#5a7f3b",
		Accent1: "#01b8c6", Accent2: "#02d2b2",
		Success: "#3fb950", Warning: "#d29922", Error: "#f85149",
		DiffAddBg: "#0b2010", DiffAddFg: "#3fb950", DiffRemoveBg: "#2a0f0f", DiffRemoveFg: "#f85149",
		Thinking: "#02d2b2", ToolBorder: "#02d2b2", SelectionBg: "#00535b", SelectionFg: "#ffffff",
		StatusBg: "#01324c", StatusFg: "#6c9845",
	},
	"hyperdrive-synthwave-neon-nebula": {
		Bg: "#060215", Surface: "#060225", Surface2: "#060749",
		Fg: "#d274f2", Dim: "#af46d5", Muted: "#995dba",
		Accent1: "#04abfe", Accent2: "#d233b0",
		Success: "#3fb950", Warning: "#d29922", Error: "#f85149",
		DiffAddBg: "#10211f", DiffAddFg: "#3fb950", DiffRemoveBg: "#2f0f1e", DiffRemoveFg: "#f85149",
		Thinking: "#d233b0", ToolBorder: "#d233b0", SelectionBg: "#054e7e", SelectionFg: "#ffffff",
		StatusBg: "#060749", StatusFg: "#af46d5",
	},
	"solar-flare-neon-crimson": {
		Bg: "#010101", Surface: "#252526", Surface2: "#1f1201",
		Fg: "#f5e8da", Dim: "#fe8b04", Muted: "#7a746c",
		Accent1: "#fe5002", Accent2: "#f407f1",
		Success: "#3fb950", Warning: "#fe7203", Error: "#ec0b03",
		DiffAddBg: "#0c200e", DiffAddFg: "#3fb950", DiffRemoveBg: "#290301", DiffRemoveFg: "#f70b03",
		Thinking: "#f407f1", ToolBorder: "#f407f1", SelectionBg: "#732501", SelectionFg: "#ffffff",
		StatusBg: "#1f1201", StatusFg: "#fe8b04",
	},
	"cyber-electric-velvet-orchid": {
		Bg: "#0c0412", Surface: "#180624", Surface2: "#2c0635",
		Fg: "#d2cdd2", Dim: "#837d85", Muted: "#7e7483",
		Accent1: "#f4027b", Accent2: "#04e8d7",
		Success: "#3fb950", Warning: "#d29922", Error: "#f4027b",
		DiffAddBg: "#15231d", DiffAddFg: "#3fb950", DiffRemoveBg: "#330424", DiffRemoveFg: "#f9027d",
		Thinking: "#04e8d7", ToolBorder: "#04e8d7", SelectionBg: "#740341", SelectionFg: "#ffffff",
		StatusBg: "#2c0635", StatusFg: "#837d85",
	},
	"crimson-tide-storm-slate": {
		Bg: "#2c3530", Surface: "#354d4d", Surface2: "#4c5651",
		Fg: "#ece4de", Dim: "#9f9e98", Muted: "#999b96",
		Accent1: "#82a0a1", Accent2: "#f47274",
		Success: "#3fb950", Warning: "#d29922", Error: "#f1737a",
		DiffAddBg: "#2f4b35", DiffAddFg: "#5ac769", DiffRemoveBg: "#4d403d", DiffRemoveFg: "#f49499",
		Thinking: "#f47274", ToolBorder: "#f14c4f", SelectionBg: "#536563", SelectionFg: "#ffffff",
		StatusBg: "#4c5651", StatusFg: "#9f9e98",
	},
	"neon-pulse-sunset-coral": {
		Bg: "#010101", Surface: "#03062a", Surface2: "#100747",
		Fg: "#b08d70", Dim: "#d1736c", Muted: "#8c6f58",
		Accent1: "#f51290", Accent2: "#f16d6d",
		Success: "#3fb950", Warning: "#d29922", Error: "#f35054",
		DiffAddBg: "#0c200e", DiffAddFg: "#3fb950", DiffRemoveBg: "#2a0e0f", DiffRemoveFg: "#f35054",
		Thinking: "#f16d6d", ToolBorder: "#f16d6d", SelectionBg: "#6f0941", SelectionFg: "#ffffff",
		StatusBg: "#100747", StatusFg: "#d1736c",
	},
	"cosmic-volt-solar-flare": {
		Bg: "#030104", Surface: "#05082e", Surface2: "#091150",
		Fg: "#eef4ee", Dim: "#0bcff5", Muted: "#747676",
		Accent1: "#09b0f7", Accent2: "#f85106",
		Success: "#3fb950", Warning: "#f3ef31", Error: "#f85149",
		DiffAddBg: "#0d2011", DiffAddFg: "#3fb950", DiffRemoveBg: "#2d0f10", DiffRemoveFg: "#f85149",
		Thinking: "#f85106", ToolBorder: "#f85106", SelectionBg: "#065071", SelectionFg: "#ffffff",
		StatusBg: "#091150", StatusFg: "#0bcff5",
	},
	"misty-pine-deep-lichen": {
		Bg: "#0c0f0e", Surface: "#142810", Surface2: "#2c3331",
		Fg: "#879387", Dim: "#737d74", Muted: "#727e74",
		Accent1: "#5d8462", Accent2: "#618081",
		Success: "#3fb950", Warning: "#d29922", Error: "#f85149",
		DiffAddBg: "#152c19", DiffAddFg: "#3fb950", DiffRemoveBg: "#341a18", DiffRemoveFg: "#f85149",
		Thinking: "#618081", ToolBorder: "#4b6365", SelectionBg: "#304434", SelectionFg: "#ffffff",
		StatusBg: "#2c3331", StatusFg: "#737d74",
	},
	"crimson-eclipse-midnight-steel": {
		Bg: "#031219", Surface: "#0a1c24", Surface2: "#15282f",
		Fg: "#f2e8e6", Dim: "#929294", Muted: "#6a7c9c",
		Accent1: "#f80528", Accent2: "#e23a55",
		Success: "#3fb950", Warning: "#d29922", Error: "#f85149",
		DiffAddBg: "#0d2e22", DiffAddFg: "#3fb950", DiffRemoveBg: "#2d1d21", DiffRemoveFg: "#f85149",
		Thinking: "#e23a55", ToolBorder: "#bb1b35", SelectionBg: "#710c20", SelectionFg: "#ffffff",
		StatusBg: "#15282f", StatusFg: "#929294",
	},
	"abyssal-sapphire-electric-cobalt": {
		Bg: "#000002", Surface: "#03082e", Surface2: "#04124e",
		Fg: "#6d90ec", Dim: "#5a73b4", Muted: "#4b75ea",
		Accent1: "#518cee", Accent2: "#496fd6",
		Success: "#3fb950", Warning: "#d29922", Error: "#f85149",
		DiffAddBg: "#0b1f0f", DiffAddFg: "#3fb950", DiffRemoveBg: "#2a0e0e", DiffRemoveFg: "#f85149",
		Thinking: "#496fd6", ToolBorder: "#2e58cd", SelectionBg: "#243f6c", SelectionFg: "#ffffff",
		StatusBg: "#04124e", StatusFg: "#5a73b4",
	},
	"emerald-canopy-deep-forest": {
		Bg: "#061504", Surface: "#132f0f", Surface2: "#134a0b",
		Fg: "#55af47", Dim: "#478e39", Muted: "#498b3e",
		Accent1: "#55af47", Accent2: "#4f8a43",
		Success: "#3fb950", Warning: "#d29922", Error: "#f85149",
		DiffAddBg: "#103111", DiffAddFg: "#3fb950", DiffRemoveBg: "#2f1f10", DiffRemoveFg: "#f85149",
		Thinking: "#4f8a43", ToolBorder: "#427338", SelectionBg: "#2a5a22", SelectionFg: "#ffffff",
		StatusBg: "#134a0b", StatusFg: "#478e39",
	},
	"velvet-garnet-smoked-mahogany": {
		Bg: "#0d0a0b", Surface: "#2d1012", Surface2: "#30292d",
		Fg: "#d8a39c", Dim: "#94716c", Muted: "#bd5c55",
		Accent1: "#c6554b", Accent2: "#ad6561",
		Success: "#3fb950", Warning: "#d29922", Error: "#dc3e3b",
		DiffAddBg: "#162817", DiffAddFg: "#3fb950", DiffRemoveBg: "#301313", DiffRemoveFg: "#e05451",
		Thinking: "#ad6561", ToolBorder: "#8f4d4a", SelectionBg: "#602c28", SelectionFg: "#ffffff",
		StatusBg: "#30292d", StatusFg: "#94716c",
	},
}
