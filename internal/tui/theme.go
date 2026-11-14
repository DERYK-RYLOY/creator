// Package tui renders Creator mission control: a full-screen terminal IDE
// around the headless agent.
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Theme is the one contract for every visible TUI surface. Built-in themes are
// plain data: palette, typography metadata and role-level colors. Call sites
// read the active theme at render time; no init-time color captures are used.
type Theme struct {
	Slug        string
	Name        string
	Mode        string
	Palette     []string
	Font        FontSpec
	Border      string
	IconSet     string
	CadenceMs   int
	LineHeight  float64
	Recommended string
	Fallbacks   []string
	Transparent bool
}

type FontSpec struct {
	Recommended string
	Fallback    []string
	Ligatures   string
	Weight      int
	LineHeight  float64
}

type ThemeRow struct {
	Slug   string
	Name   string
	Mode   string
	Font   string
	Active bool
}

type resolvedTheme struct {
	Theme
	bg          string
	surface     string
	surface2    string
	fg          string
	dim         string
	muted       string
	accent1     string
	accent2     string
	success     string
	warning     string
	error       string
	addBg       string
	addFg       string
	removeBg    string
	removeFg    string
	thinking    string
	borders     string
	selectionBg string
	selectionFg string
	statusBg    string
	statusFg    string
	cyan        string
	varis       string
}

var builtinThemes = []Theme{
	{
		Slug: "volcanic-ember-glacial-slate", Name: "Volcanic Ember & Glacial Slate", Mode: "dark",
		Palette: []string{"#120d0d", "#162b30", "#300e0c", "#293333", "#4f1009", "#314c51", "#512a2b", "#700e06", "#732b0e", "#8f0f09", "#90300d", "#4d6c72", "#ab170c", "#ad3011", "#b24c11", "#d16e13", "#d26d2b", "#ee8f2b", "#d0ab73", "#ebcb94"},
		Font:    FontSpec{Recommended: "JetBrains Mono", Fallback: []string{"Fira Code", "IBM Plex Mono", "Cascadia Code", "Consolas", "Menlo", "monospace"}, Ligatures: "code-only", Weight: 500, LineHeight: 1.35},
		Border:  "sharp", IconSet: "auto", CadenceMs: 90, LineHeight: 1.35, Recommended: "JetBrains Mono", Fallbacks: []string{"Fira Code", "IBM Plex Mono", "monospace"},
	},
	{
		Slug: "sunlit-cove-copper-reef", Name: "Sunlit Cove & Copper Reef", Mode: "dark",
		Palette: []string{"#0d374d", "#13526e", "#8e360d", "#28586c", "#156c8f", "#316578", "#2d738f", "#b24f11", "#c85710", "#2f8daf", "#b26e2e", "#ce6d16", "#4e93ab", "#cc7429", "#90978f", "#d5892f", "#ad9571", "#ed922b", "#f4b031", "#f7d14c"},
		Font:    FontSpec{Recommended: "IBM Plex Mono", Fallback: []string{"JetBrains Mono", "Geist Mono", "Cascadia Code", "Consolas", "monospace"}, Ligatures: "off", Weight: 500, LineHeight: 1.35},
		Border:  "rounded", IconSet: "auto", CadenceMs: 90, LineHeight: 1.35, Recommended: "IBM Plex Mono", Fallbacks: []string{"JetBrains Mono", "Geist Mono", "monospace"},
	},
	{
		Slug: "cyberpunk-blaze-abyssal-neon", Name: "Cyberpunk Blaze & Abyssal Neon", Mode: "dark",
		Palette: []string{"#0d0c12", "#161a29", "#251c27", "#28242f", "#162634", "#143451", "#900e0e", "#144773", "#0e578a", "#b41726", "#ce1928", "#0e6599", "#4e7551", "#d2340f", "#e82e14", "#ec2d34", "#eb4d07", "#f37709", "#ec920a", "#eeaa09"},
		Font:    FontSpec{Recommended: "Iosevka", Fallback: []string{"Geist Mono", "JetBrains Mono", "Consolas", "monospace"}, Ligatures: "off-tui-on-editor", Weight: 500, LineHeight: 1.45},
		Border:  "sharp", IconSet: "auto", CadenceMs: 60, LineHeight: 1.45, Recommended: "Iosevka", Fallbacks: []string{"Geist Mono", "JetBrains Mono", "monospace"},
	},
	{
		Slug: "toxic-venom-obsidian-moss", Name: "Toxic Venom & Obsidian Moss", Mode: "dark",
		Palette: []string{"#0d0d0e", "#1e1e20", "#162a03", "#2c2c2c", "#263609", "#344b05", "#474747", "#445a05", "#546e04", "#637c03", "#729002", "#819e02", "#79a609", "#92b201", "#a0bf01", "#98c70b", "#b2d101", "#c1df01", "#bbe404", "#c3e201"},
		Font:    FontSpec{Recommended: "JetBrains Mono", Fallback: []string{"Fira Code", "IBM Plex Mono", "Cascadia Code", "monospace"}, Ligatures: "code-only", Weight: 500, LineHeight: 1.40},
		Border:  "sharp", IconSet: "auto", CadenceMs: 60, LineHeight: 1.40, Recommended: "JetBrains Mono", Fallbacks: []string{"Fira Code", "IBM Plex Mono", "monospace"},
	},
	{
		Slug: "ancient-parchment-roasted-espresso", Name: "Ancient Parchment & Roasted Espresso", Mode: "dark",
		Palette: []string{"#0a0706", "#2a1911", "#362f27", "#372418", "#472d1b", "#4e3726", "#564632", "#595348", "#665c4c", "#6e522f", "#736751", "#897b6a", "#8a764f", "#958872", "#97855a", "#9a9485", "#a6936f", "#a79b8a", "#b0a795", "#bab4a4"},
		Font:    FontSpec{Recommended: "IBM Plex Mono", Fallback: []string{"JetBrains Mono", "Cascadia Code", "Consolas", "monospace"}, Ligatures: "off", Weight: 500, LineHeight: 1.45},
		Border:  "minimal", IconSet: "auto", CadenceMs: 160, LineHeight: 1.45, Recommended: "IBM Plex Mono", Fallbacks: []string{"JetBrains Mono", "Cascadia Code", "monospace"},
	},
	{
		Slug: "nordic-fog-slate-mist", Name: "Nordic Fog & Slate Mist", Mode: "dark",
		Palette: []string{"#0d1116", "#1a1d25", "#1d2129", "#211f28", "#24272f", "#303a42", "#3c444b", "#495157", "#4f5a64", "#57626c", "#65625e", "#6e6d6b", "#94918b", "#a7a39b", "#b7b3ab", "#c4bdb4", "#d5ccbc", "#ddd3c4", "#e0d6c7", "#e6e2d5"},
		Font:    FontSpec{Recommended: "IBM Plex Mono", Fallback: []string{"JetBrains Mono", "Geist Mono", "Cascadia Code", "monospace"}, Ligatures: "off", Weight: 500, LineHeight: 1.40},
		Border:  "minimal", IconSet: "auto", CadenceMs: 160, LineHeight: 1.40, Recommended: "IBM Plex Mono", Fallbacks: []string{"JetBrains Mono", "Geist Mono", "monospace"},
	},
	{
		Slug: "midnight-violet-molten-ember", Name: "Midnight Violet & Molten Ember", Mode: "dark",
		Palette: []string{"#010102", "#0e1328", "#2d0530", "#330e0d", "#0b304b", "#4c130b", "#4d0651", "#6f2e0c", "#700871", "#d25010"},
		Font:    FontSpec{Recommended: "JetBrains Mono", Fallback: []string{"Cascadia Code", "IBM Plex Mono", "Consolas", "monospace"}, Ligatures: "code-only", Weight: 500, LineHeight: 1.40},
		Border:  "minimal", IconSet: "auto", CadenceMs: 120, LineHeight: 1.40, Recommended: "JetBrains Mono", Fallbacks: []string{"Cascadia Code", "IBM Plex Mono", "monospace"},
	},
	{
		Slug: "electric-rainforest-mint-frost", Name: "Electric Rainforest & Mint Frost", Mode: "dark",
		Palette: []string{"#155049", "#318f13", "#479912", "#54aa16", "#6ab519", "#91d623", "#b0ec27", "#c8fc31", "#ceebd5", "#e7fee9"},
		Font:    FontSpec{Recommended: "Geist Mono", Fallback: []string{"JetBrains Mono", "IBM Plex Mono", "Consolas", "monospace"}, Ligatures: "off", Weight: 500, LineHeight: 1.45},
		Border:  "rounded", IconSet: "auto", CadenceMs: 60, LineHeight: 1.45, Recommended: "Geist Mono", Fallbacks: []string{"JetBrains Mono", "IBM Plex Mono", "monospace"},
	},
	{
		Slug: "shadowed-pine-jade-moss", Name: "Shadowed Pine & Jade Moss", Mode: "dark",
		Palette: []string{"#090a08", "#19221b", "#23231c", "#173025", "#2f322a", "#154937", "#454336", "#276e54", "#676757", "#298f70"},
		Font:    FontSpec{Recommended: "IBM Plex Mono", Fallback: []string{"JetBrains Mono", "Cascadia Code", "Consolas", "monospace"}, Ligatures: "off", Weight: 500, LineHeight: 1.40},
		Border:  "rounded", IconSet: "auto", CadenceMs: 120, LineHeight: 1.40, Recommended: "IBM Plex Mono", Fallbacks: []string{"JetBrains Mono", "Cascadia Code", "monospace"},
	},
	{
		Slug: "bioluminescent-abyss-electric-lime", Name: "Bioluminescent Abyss & Electric Lime", Mode: "dark",
		Palette: []string{"#000103", "#01102c", "#092d0d", "#01324c", "#014758", "#295401", "#01b8c6", "#01c6c8", "#02d2b2", "#b4fc71"},
		Font:    FontSpec{Recommended: "JetBrains Mono", Fallback: []string{"Geist Mono", "IBM Plex Mono", "Consolas", "monospace"}, Ligatures: "off-tui", Weight: 500, LineHeight: 1.25},
		Border:  "minimal", IconSet: "auto", CadenceMs: 120, LineHeight: 1.25, Recommended: "JetBrains Mono", Fallbacks: []string{"Geist Mono", "IBM Plex Mono", "monospace"},
	},
	{
		Slug: "hyperdrive-synthwave-neon-nebula", Name: "Hyperdrive Synthwave & Neon Nebula", Mode: "dark",
		Palette: []string{"#060215", "#060225", "#070e91", "#0571fc", "#9050ed", "#af46d5", "#d233b0", "#048dfd", "#04abfe", "#d274f2"},
		Font:    FontSpec{Recommended: "JetBrains Mono", Fallback: []string{"Victor Mono", "Cascadia Code", "monospace"}, Ligatures: "code-only", Weight: 500, LineHeight: 1.40},
		Border:  "sharp", IconSet: "auto", CadenceMs: 60, LineHeight: 1.40, Recommended: "JetBrains Mono", Fallbacks: []string{"Victor Mono", "Cascadia Code", "monospace"},
	},
	{
		Slug: "solar-flare-neon-crimson", Name: "Solar Flare & Neon Crimson", Mode: "dark",
		Palette: []string{"#010101", "#252526", "#b20205", "#d00204", "#ec0b03", "#fb3103", "#fe5002", "#f407f1", "#fe7203", "#fe8b04"},
		Font:    FontSpec{Recommended: "Fira Code", Fallback: []string{"JetBrains Mono", "Cascadia Code", "Consolas", "monospace"}, Ligatures: "code-only", Weight: 500, LineHeight: 1.45},
		Border:  "sharp", IconSet: "auto", CadenceMs: 90, LineHeight: 1.45, Recommended: "Fira Code", Fallbacks: []string{"JetBrains Mono", "Cascadia Code", "monospace"},
	},
	{
		Slug: "cyber-electric-velvet-orchid", Name: "Cyber Electric & Velvet Orchid", Mode: "dark",
		Palette: []string{"#0c0412", "#180624", "#2c0635", "#390844", "#480e4f", "#f4027b", "#05d7c9", "#d2cdd2", "#04e8d7", "#0af7ea"},
		Font:    FontSpec{Recommended: "Geist Mono", Fallback: []string{"JetBrains Mono", "Iosevka", "Consolas", "monospace"}, Ligatures: "off", Weight: 500, LineHeight: 1.40},
		Border:  "rounded", IconSet: "auto", CadenceMs: 60, LineHeight: 1.40, Recommended: "Geist Mono", Fallbacks: []string{"JetBrains Mono", "Iosevka", "monospace"},
	},
	{
		Slug: "crimson-tide-storm-slate", Name: "Crimson Tide & Storm Slate", Mode: "dark",
		Palette: []string{"#2c3530", "#354d4d", "#4c5651", "#526c6d", "#af0f17", "#cc1a26", "#d22b33", "#e5313a", "#eb3a44", "#f14c4f"},
		Font:    FontSpec{Recommended: "Geist Mono", Fallback: []string{"JetBrains Mono", "IBM Plex Mono", "monospace"}, Ligatures: "off", Weight: 500, LineHeight: 1.40},
		Border:  "rounded", IconSet: "auto", CadenceMs: 90, LineHeight: 1.40, Recommended: "Geist Mono", Fallbacks: []string{"JetBrains Mono", "IBM Plex Mono", "monospace"},
	},
	{
		Slug: "neon-pulse-sunset-coral", Name: "Neon Pulse & Sunset Coral", Mode: "dark",
		Palette: []string{"#010101", "#0a18cf", "#0a16ef", "#2e14ce", "#5013cd", "#f51290", "#f35054", "#d1736c", "#f16d6d", "#b08d70"},
		Font:    FontSpec{Recommended: "JetBrains Mono", Fallback: []string{"Fira Code", "IBM Plex Mono", "monospace"}, Ligatures: "code-only", Weight: 500, LineHeight: 1.40},
		Border:  "sharp", IconSet: "auto", CadenceMs: 90, LineHeight: 1.40, Recommended: "JetBrains Mono", Fallbacks: []string{"Fira Code", "IBM Plex Mono", "monospace"},
	},
	{
		Slug: "cosmic-volt-solar-flare", Name: "Cosmic Volt & Solar Flare", Mode: "dark",
		Palette: []string{"#030104", "#05082e", "#091150", "#0f186a", "#0d94e9", "#f85106", "#09b0f7", "#0bcff5", "#f3ef31", "#f1fb4e"},
		Font:    FontSpec{Recommended: "JetBrains Mono", Fallback: []string{"Geist Mono", "IBM Plex Mono", "monospace"}, Ligatures: "off-tui", Weight: 500, LineHeight: 1.35},
		Border:  "sharp", IconSet: "auto", CadenceMs: 90, LineHeight: 1.35, Recommended: "JetBrains Mono", Fallbacks: []string{"Geist Mono", "IBM Plex Mono", "monospace"},
	},
	{
		Slug: "misty-pine-deep-lichen", Name: "Misty Pine & Deep Lichen", Mode: "dark",
		Palette: []string{"#0c0f0e", "#142810", "#112b2c", "#272c16", "#2c3331", "#324735", "#364748", "#4a4b37", "#4b534c", "#6b766b"},
		Font:    FontSpec{Recommended: "IBM Plex Mono", Fallback: []string{"JetBrains Mono", "Geist Mono", "monospace"}, Ligatures: "off", Weight: 500, LineHeight: 1.35},
		Border:  "rounded", IconSet: "auto", CadenceMs: 160, LineHeight: 1.35, Recommended: "IBM Plex Mono", Fallbacks: []string{"JetBrains Mono", "Geist Mono", "monospace"},
	},
	{
		Slug: "crimson-eclipse-midnight-steel", Name: "Crimson Eclipse & Midnight Steel", Mode: "dark",
		Palette: []string{"#031219", "#0a1c24", "#15282f", "#2d2e35", "#2d3544", "#4d2b35", "#901529", "#b11229", "#d10e29", "#f80528"},
		Font:    FontSpec{Recommended: "JetBrains Mono", Fallback: []string{"IBM Plex Mono", "Cascadia Code", "monospace"}, Ligatures: "off-tui", Weight: 500, LineHeight: 1.25},
		Border:  "sharp", IconSet: "auto", CadenceMs: 120, LineHeight: 1.25, Recommended: "JetBrains Mono", Fallbacks: []string{"IBM Plex Mono", "Cascadia Code", "monospace"},
	},
	{
		Slug: "abyssal-sapphire-electric-cobalt", Name: "Abyssal Sapphire & Electric Cobalt", Mode: "dark",
		Palette: []string{"#000002", "#03082e", "#04124e", "#05196c", "#0d2e8f", "#2e58cd", "#336fe9", "#4b75ea", "#518cee", "#6d90ec"},
		Font:    FontSpec{Recommended: "JetBrains Mono", Fallback: []string{"Geist Mono", "Iosevka", "monospace"}, Ligatures: "off-tui", Weight: 500, LineHeight: 1.20},
		Border:  "sharp", IconSet: "auto", CadenceMs: 120, LineHeight: 1.20, Recommended: "JetBrains Mono", Fallbacks: []string{"Geist Mono", "Iosevka", "monospace"},
	},
	{
		Slug: "emerald-canopy-deep-forest", Name: "Emerald Canopy & Deep Forest", Mode: "dark",
		Palette: []string{"#061504", "#132f0f", "#134a0b", "#234c1c", "#2d5626", "#396c30", "#427338", "#478e39", "#508d45", "#55af47"},
		Font:    FontSpec{Recommended: "Iosevka", Fallback: []string{"JetBrains Mono", "IBM Plex Mono", "monospace"}, Ligatures: "off", Weight: 500, LineHeight: 1.00},
		Border:  "rounded", IconSet: "auto", CadenceMs: 90, LineHeight: 1.00, Recommended: "Iosevka", Fallbacks: []string{"JetBrains Mono", "IBM Plex Mono", "monospace"},
	},
	{
		Slug: "velvet-garnet-smoked-mahogany", Name: "Velvet Garnet & Smoked Mahogany", Mode: "dark",
		Palette: []string{"#0d0a0b", "#2d1012", "#2e1924", "#30292d", "#4e1413", "#4e2c2d", "#6b1513", "#6e2f2b", "#8a332c", "#8f4d4a"},
		Font:    FontSpec{Recommended: "IBM Plex Mono", Fallback: []string{"JetBrains Mono", "Fira Code", "monospace"}, Ligatures: "code-only", Weight: 500, LineHeight: 1.40},
		Border:  "rounded", IconSet: "auto", CadenceMs: 90, LineHeight: 1.40, Recommended: "IBM Plex Mono", Fallbacks: []string{"JetBrains Mono", "Fira Code", "monospace"},
	},
	{
		Slug: "lunar-tide-pearl-dusk", Name: "Lunar Tide & Pearl Dusk", Mode: "dark",
		Palette: []string{"#0a0e14", "#101722", "#16202e", "#1d2837", "#27334a", "#32415c", "#3f4f6e", "#4d5f82", "#5d7094", "#7084aa", "#8696b4", "#9da9c2", "#b6bccd", "#cecdd3", "#e2ded6", "#efe9df", "#f5efe4", "#faf5ea", "#fdfbf3", "#ffffff"},
		Font:    FontSpec{Recommended: "JetBrains Mono", Fallback: []string{"IBM Plex Mono", "Geist Mono", "Cascadia Code", "monospace"}, Ligatures: "code-only", Weight: 400, LineHeight: 1.40},
		Border:  "rounded", IconSet: "auto", CadenceMs: 90, LineHeight: 1.40, Recommended: "JetBrains Mono", Fallbacks: []string{"IBM Plex Mono", "Geist Mono", "monospace"},
	},
	{
		Slug: "aurora-veil-frozen-cobalt", Name: "Aurora Veil & Frozen Cobalt", Mode: "dark",
		Palette: []string{"#060b14", "#0b1220", "#101a2e", "#16233b", "#1d2d49", "#243854", "#2d4460", "#36526d", "#42607b", "#4f6f8a", "#5f7f99", "#728fa6", "#87a0b2", "#9db2c0", "#b2c5cd", "#c4d6da", "#d5e6e8", "#e4f1f1", "#eef8f7", "#f7fffb"},
		Font:    FontSpec{Recommended: "Geist Mono", Fallback: []string{"JetBrains Mono", "IBM Plex Mono", "monospace"}, Ligatures: "off", Weight: 500, LineHeight: 1.35},
		Border:  "sharp", IconSet: "auto", CadenceMs: 90, LineHeight: 1.35, Recommended: "Geist Mono", Fallbacks: []string{"JetBrains Mono", "IBM Plex Mono", "monospace"},
	},
	{
		Slug: "imperial-jade-forbidden-gold", Name: "Imperial Jade & Forbidden Gold", Mode: "dark",
		Palette: []string{"#080d0a", "#0d1510", "#121f18", "#182a20", "#203527", "#294134", "#334e3f", "#3f5c4c", "#4d6859", "#5d7567", "#6f8374", "#829283", "#96a294", "#a8b4a6", "#b9c6b4", "#cad6bf", "#dde6cf", "#ecf1da", "#f5f8e8", "#fcfff0"},
		Font:    FontSpec{Recommended: "IBM Plex Mono", Fallback: []string{"Iosevka", "JetBrains Mono", "monospace"}, Ligatures: "off", Weight: 500, LineHeight: 1.40},
		Border:  "minimal", IconSet: "auto", CadenceMs: 160, LineHeight: 1.40, Recommended: "IBM Plex Mono", Fallbacks: []string{"JetBrains Mono", "monospace"},
	},
	{
		Slug: "deep-space-ion-violet", Name: "Deep Space & Ion Violet", Mode: "dark",
		Palette: []string{"#07060f", "#0d0b1c", "#131029", "#191539", "#201c4a", "#282457", "#312d66", "#3d3876", "#494487", "#564f99", "#645fab", "#726fbd", "#837fcf", "#958fe0", "#a89fed", "#baaff8", "#ccc1fd", "#ddd4ff", "#ebe6ff", "#f8f6ff"},
		Font:    FontSpec{Recommended: "Iosevka", Fallback: []string{"JetBrains Mono", "Geist Mono", "Cascadia Code", "monospace"}, Ligatures: "code-only", Weight: 500, LineHeight: 1.40},
		Border:  "sharp", IconSet: "auto", CadenceMs: 60, LineHeight: 1.40, Recommended: "Iosevka", Fallbacks: []string{"JetBrains Mono", "Geist Mono", "monospace"},
	},
	{
		Slug: "sakura-static-neon-blossom", Name: "Sakura Static & Neon Blossom", Mode: "dark",
		Palette: []string{"#0f060d", "#1a0c18", "#25111f", "#30172a", "#3e1e35", "#4d263f", "#5e2e4c", "#6f385a", "#81436a", "#934f7a", "#a65d8b", "#b96b9c", "#cb7bad", "#dd8cbe", "#ec9dcf", "#f6aedd", "#fcc3e8", "#fed7ef", "#ffe7f6", "#fff5fb"},
		Font:    FontSpec{Recommended: "Fira Code", Fallback: []string{"Geist Mono", "JetBrains Mono", "monospace"}, Ligatures: "code-only", Weight: 500, LineHeight: 1.45},
		Border:  "rounded", IconSet: "auto", CadenceMs: 90, LineHeight: 1.45, Recommended: "Fira Code", Fallbacks: []string{"Geist Mono", "JetBrains Mono", "monospace"},
	},
	{
		Slug: "desert-glass-mirage", Name: "Desert Glass & Mirage", Mode: "dark",
		Palette: []string{"#0f0c08", "#1a1510", "#241e17", "#2f2820", "#3b342a", "#484036", "#564e43", "#645c50", "#736b5e", "#837b6d", "#948b7d", "#a69d8f", "#b7aea0", "#c7bfb3", "#d6cec2", "#e5ded2", "#efe9dc", "#f7f2e6", "#fcf8ec", "#fffdf4"},
		Font:    FontSpec{Recommended: "IBM Plex Mono", Fallback: []string{"JetBrains Mono", "Fira Code", "monospace"}, Ligatures: "off", Weight: 500, LineHeight: 1.45},
		Border:  "minimal", IconSet: "auto", CadenceMs: 160, LineHeight: 1.45, Recommended: "IBM Plex Mono", Fallbacks: []string{"JetBrains Mono", "Fira Code", "monospace"},
	},
	{
		Slug: "glacier-core-arctic-cyan", Name: "Glacier Core & Arctic Cyan", Mode: "dark",
		Palette: []string{"#070c10", "#0c141a", "#111c24", "#172630", "#1f303c", "#283d4b", "#324b5b", "#3d596b", "#48697d", "#54798e", "#618aa0", "#6f9cb1", "#7fa9bd", "#90b6c9", "#a2c3d3", "#b4d0de", "#c6ddeb", "#d8eaf3", "#e8f4fa", "#f6fbff"},
		Font:    FontSpec{Recommended: "JetBrains Mono", Fallback: []string{"Cascadia Code", "Geist Mono", "IBM Plex Mono", "monospace"}, Ligatures: "code-only", Weight: 400, LineHeight: 1.35},
		Border:  "rounded", IconSet: "auto", CadenceMs: 90, LineHeight: 1.35, Recommended: "JetBrains Mono", Fallbacks: []string{"Cascadia Code", "Geist Mono", "monospace"},
	},
	{
		Slug: "velvet-nebula-stellar-wine", Name: "Velvet Nebula & Stellar Wine", Mode: "dark",
		Palette: []string{"#100509", "#1b0a10", "#25111a", "#301822", "#3d1f2d", "#4c2738", "#5c2f44", "#6d3851", "#7e425e", "#904d6c", "#a2577a", "#b46389", "#c77198", "#d882a8", "#e795b8", "#f0a8c8", "#f5b9d4", "#f9cbdf", "#fcdcea", "#fff0f5"},
		Font:    FontSpec{Recommended: "Geist Mono", Fallback: []string{"JetBrains Mono", "Iosevka", "monospace"}, Ligatures: "off", Weight: 500, LineHeight: 1.40},
		Border:  "rounded", IconSet: "auto", CadenceMs: 120, LineHeight: 1.40, Recommended: "Geist Mono", Fallbacks: []string{"JetBrains Mono", "Iosevka", "monospace"},
	},
	{
		Slug: "koi-pond-tangerine-jade", Name: "Koi Pond & Tangerine Jade", Mode: "dark",
		Palette: []string{"#060f0f", "#0b1a1a", "#101f20", "#162729", "#1e3034", "#273a40", "#32454d", "#3e515b", "#4a5e69", "#576b77", "#657885", "#748592", "#8495a3", "#94a5b4", "#a5b6c4", "#b7c8d4", "#c9d9e3", "#d9e9ef", "#e8f6fa", "#f5ffff"},
		Font:    FontSpec{Recommended: "JetBrains Mono", Fallback: []string{"Fira Code", "Cascadia Code", "monospace"}, Ligatures: "code-only", Weight: 500, LineHeight: 1.35},
		Border:  "rounded", IconSet: "auto", CadenceMs: 90, LineHeight: 1.35, Recommended: "JetBrains Mono", Fallbacks: []string{"Fira Code", "Cascadia Code", "monospace"},
	},
	{
		Slug: "raven-oil-sheen", Name: "Raven Oil & Sheen", Mode: "dark",
		Palette: []string{"#050607", "#0a0c0f", "#0f1217", "#151923", "#1c212e", "#232a3a", "#2c3447", "#364055", "#414d64", "#4d5b73", "#596a83", "#677a93", "#7689a4", "#8699b4", "#97a9c5", "#a8bcd6", "#bacce6", "#cddeef", "#deeefb", "#f0f8ff"},
		Font:    FontSpec{Recommended: "Cascadia Code", Fallback: []string{"JetBrains Mono", "Fira Code", "monospace"}, Ligatures: "code-only", Weight: 400, LineHeight: 1.35},
		Border:  "sharp", IconSet: "auto", CadenceMs: 90, LineHeight: 1.35, Recommended: "Cascadia Code", Fallbacks: []string{"JetBrains Mono", "Fira Code", "monospace"},
	},
	{
		Slug: "lichen-quartz-petrified-coral", Name: "Lichen Quartz & Petrified Coral", Mode: "dark",
		Palette: []string{"#0d0c0a", "#161411", "#1f1c18", "#292521", "#332f2a", "#3d3934", "#484540", "#545049", "#605c54", "#6d6960", "#7a766c", "#88847a", "#979288", "#a7a298", "#b7b3a8", "#c7c3b9", "#d7d4cb", "#e5e3da", "#efeee6", "#f8f7f0"},
		Font:    FontSpec{Recommended: "IBM Plex Mono", Fallback: []string{"JetBrains Mono", "Cascadia Code", "monospace"}, Ligatures: "off", Weight: 500, LineHeight: 1.45},
		Border:  "minimal", IconSet: "auto", CadenceMs: 160, LineHeight: 1.45, Recommended: "IBM Plex Mono", Fallbacks: []string{"JetBrains Mono", "Cascadia Code", "monospace"},
	},
	{
		Slug: "midnight-circuit-emerald-pulse", Name: "Midnight Circuit & Emerald Pulse", Mode: "dark",
		Palette: []string{"#040d09", "#08160f", "#0c1f16", "#11291d", "#173525", "#1e422e", "#265038", "#305e43", "#3a6c4f", "#457b5c", "#518a6a", "#5e9a7a", "#6ca88a", "#7bb799", "#8cc6a9", "#9fd5b9", "#b3e4c8", "#c8f2da", "#d9fae8", "#effff4"},
		Font:    FontSpec{Recommended: "JetBrains Mono", Fallback: []string{"IBM Plex Mono", "Fira Code", "monospace"}, Ligatures: "code-only", Weight: 500, LineHeight: 1.35},
		Border:  "sharp", IconSet: "auto", CadenceMs: 90, LineHeight: 1.35, Recommended: "JetBrains Mono", Fallbacks: []string{"IBM Plex Mono", "Fira Code", "monospace"},
	},
	{
		Slug: "solar-drift-amber-horizon", Name: "Solar Drift & Amber Horizon", Mode: "dark",
		Palette: []string{"#0f0805", "#1a0f08", "#25160c", "#301d12", "#3e2619", "#4d3020", "#5e3c28", "#6f4830", "#81553a", "#936344", "#a5724f", "#b7815b", "#c99068", "#d8a076", "#e6b085", "#f2c096", "#fad0a9", "#ffe3bd", "#ffedd2", "#fff8ec"},
		Font:    FontSpec{Recommended: "Fira Code", Fallback: []string{"JetBrains Mono", "Iosevka", "monospace"}, Ligatures: "code-only", Weight: 500, LineHeight: 1.40},
		Border:  "rounded", IconSet: "auto", CadenceMs: 90, LineHeight: 1.40, Recommended: "Fira Code", Fallbacks: []string{"JetBrains Mono", "Iosevka", "monospace"},
	},
	{
		Slug: "garden-ink-parchment-day", Name: "Garden Ink & Parchment Day", Mode: "light",
		Palette: []string{"#faf7ef", "#f4f1e6", "#edeade", "#e4e2d6", "#d5d3c6", "#c4c2b6", "#b3b1a5", "#a2a093", "#918f82", "#807e73", "#6f6e64", "#5e5d55", "#4e4d47", "#3f3e39", "#302f2c", "#232220", "#181716", "#0f0e0d", "#080807", "#000000"},
		Font:    FontSpec{Recommended: "IBM Plex Mono", Fallback: []string{"JetBrains Mono", "Cascadia Code", "monospace"}, Ligatures: "off", Weight: 500, LineHeight: 1.45},
		Border:  "rounded", IconSet: "auto", CadenceMs: 160, LineHeight: 1.45, Recommended: "IBM Plex Mono", Fallbacks: []string{"JetBrains Mono", "Cascadia Code", "monospace"},
	},
}

var currentSlug = "volcanic-ember-glacial-slate"
var currentTheme = resolveTheme(mustTheme(currentSlug))

func mustTheme(slug string) Theme {
	for _, t := range builtinThemes {
		if t.Slug == slug {
			return t
		}
	}
	return builtinThemes[0]
}

func ThemeNames() []ThemeRow {
	out := make([]ThemeRow, 0, len(builtinThemes))
	for _, t := range builtinThemes {
		out = append(out, ThemeRow{Slug: t.Slug, Name: t.Name, Mode: t.Mode, Font: t.Font.Recommended, Active: t.Slug == currentSlug})
	}
	return out
}

func ThemeBySlug(slug string) (Theme, bool) {
	for _, t := range builtinThemes {
		if t.Slug == slug {
			return t, true
		}
	}
	return Theme{}, false
}

func CurrentThemeSlug() string { return currentSlug }
func CurrentTheme() Theme      { return currentTheme.Theme }

func ApplyTheme(slug string) bool {
	t, ok := ThemeBySlug(slug)
	if !ok {
		currentTheme = resolveTheme(mustTheme(currentSlug))
		return false
	}
	currentSlug = t.Slug
	currentTheme = resolveTheme(t)
	return true
}

func resolveTheme(t Theme) resolvedTheme {
	r, ok := roleAssignments[t.Slug]
	if !ok {
		r = roleAssignments["volcanic-ember-glacial-slate"]
	}
	return resolvedTheme{
		Theme: t,
		bg:    r.Bg, surface: r.Surface, surface2: r.Surface2,
		fg: r.Fg, dim: r.Dim, muted: r.Muted,
		accent1: r.Accent1, accent2: r.Accent2,
		success: r.Success, warning: r.Warning, error: r.Error,
		addBg: r.DiffAddBg, addFg: r.DiffAddFg, removeBg: r.DiffRemoveBg, removeFg: r.DiffRemoveFg,
		thinking: r.Thinking, borders: r.ToolBorder,
		selectionBg: r.SelectionBg, selectionFg: r.SelectionFg,
		statusBg: r.StatusBg, statusFg: r.StatusFg,
		cyan: r.Accent2, varis: r.Accent1,
	}
}

type dynStyle struct {
	name string
	bold bool
}

func dynStyleToStyle(d dynStyle) lipgloss.Style {
	s := lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.fg))
	switch strings.ToLower(d.name) {
	case "dim":
		s = s.Foreground(lipgloss.Color(currentTheme.dim))
	case "faint", "muted":
		s = s.Foreground(lipgloss.Color(currentTheme.muted))
	case "cream":
		s = s.Foreground(lipgloss.Color(currentTheme.fg))
	case "blue":
		s = s.Foreground(lipgloss.Color(currentTheme.cyan))
	case "mint", "two", "teal", "cyan":
		s = s.Foreground(lipgloss.Color(currentTheme.accent2))
	case "orange", "magenta":
		s = s.Foreground(lipgloss.Color(currentTheme.accent1))
	case "good":
		s = s.Foreground(lipgloss.Color(currentTheme.success))
	case "bad":
		s = s.Foreground(lipgloss.Color(currentTheme.error))
	case "warn":
		s = s.Foreground(lipgloss.Color(currentTheme.warning))
	default:
		if strings.HasPrefix(d.name, "#") && len(d.name) == 7 {
			s = s.Foreground(lipgloss.Color(d.name))
		}
	}
	if d.bold {
		s = s.Bold(true)
	}
	return s
}

func (d dynStyle) Render(args ...string) string {
	style := dynStyleToStyle(d)
	return style.Render(args...)
}

func (d dynStyle) Bold(v bool) dynStyle   { d.bold = v; return d }
func (d dynStyle) Italic(v bool) dynStyle { _ = v; return d }

var (
	faint   = dynStyle{name: "faint"}
	dim     = dynStyle{name: "dim"}
	cream   = dynStyle{name: "cream"}
	blue    = dynStyle{name: "blue"}
	mint    = dynStyle{name: "mint"}
	orange  = dynStyle{name: "orange"}
	good    = dynStyle{name: "good"}
	bad     = dynStyle{name: "bad"}
	warn    = dynStyle{name: "warn"}
	magenta = dynStyle{name: "magenta"}
	muted   = dynStyle{name: "muted"}
)

func themeBorder() lipgloss.Border {
	switch strings.ToLower(strings.TrimSpace(currentTheme.Theme.Border)) {
	case "rounded":
		return lipgloss.RoundedBorder()
	case "minimal":
		return minimalBorder()
	case "double":
		return lipgloss.DoubleBorder()
	case "ascii":
		return lipgloss.ASCIIBorder()
	case "none":
		return lipgloss.HiddenBorder()
	default:
		return lipgloss.NormalBorder()
	}
}

func minimalBorder() lipgloss.Border {
	// A quiet, no-frame look: one rule on top and one below, no side bars.
	return lipgloss.Border{
		Top:         "─",
		Bottom:      "─",
		Left:        " ",
		Right:       " ",
		TopLeft:     " ",
		TopRight:    " ",
		BottomLeft:  " ",
		BottomRight: " ",
	}
}

func borderColor(name string) lipgloss.Color {
	switch name {
	case "shell", "write", "subtle":
		return lipgloss.Color(currentTheme.statusBg)
	case "accent":
		return lipgloss.Color(currentTheme.accent1)
	case "danger":
		return lipgloss.Color(currentTheme.error)
	default:
		return lipgloss.Color(currentTheme.statusBg)
	}
}

func readableSize(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fm", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
