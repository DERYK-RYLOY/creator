package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// Prints the corner widget through its real renderer for visual inspection.
// Not a snapshot assertion — assertions below guard the invariants that
// matter (decode OK, no panic across the wave cycle, stable dimensions).
func TestBrailleWidgetVisual(t *testing.T) {
	if !decodeWordmark() {
		t.Fatal("wordmark decode failed — widget will never render")
	}
	// one heartbeat cycle: ring speed 38 dots/s crosses the word in ~1.1s
	for _, step := range []struct {
		name  string
		t     float64
		morph float64
	}{
		{"idle calm", 0.0, 0.0},
		{"ring mid-word", 0.55, 1.0},
		{"ring settled", 2.20, 1.0},
		{"next heartbeat", 3.60, 1.0},
	} {
		t.Logf("--- %s (t=%.2f morph=%.1f) ---\n%s", step.name, step.t, step.morph, renderBrailleWidget(step.t, step.morph))
	}
}

func TestBrailleWidgetInvariants(t *testing.T) {
	for _, morph := range []float64{0, 0.25, 0.5, 1} {
		for i := 0; i < 40; i++ {
			_ = renderBrailleWidget(float64(i)*0.35, morph) // must not panic, ever
		}
	}
	lines := strings.Split(renderBrailleWidget(1.5, 1.0), "\n")
	if len(lines) != widgetRows {
		t.Fatalf("widget rows = %d, want %d", len(lines), widgetRows)
	}
	for _, ln := range lines {
		if w := lipgloss.Width(ln); w != widgetCols {
			t.Fatalf("widget line width = %d, want %d", w, widgetCols)
		}
	}
}
