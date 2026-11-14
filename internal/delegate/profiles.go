package delegate

import (
	"sort"
	"strings"
)

// Profile is a named sub-agent role: a curated toolset plus the mindset the
// delegate should adopt for its one task.
//
// Profiles are the difference between "delegate some work" and "ask an
// explorer to map this package". The role text is part of the delegate's
// prompt, and the toolset is enforced by the registry predicate, so a
// reviewer physically cannot edit a file even if the task invites it.
type Profile struct {
	// Name is the profile slug used in delegate(profile=...).
	Name string
	// Description is the one-line summary shown by `creator profiles`.
	Description string
	// ReadOnly restricts the delegate to tools that cannot mutate state.
	ReadOnly bool
	// Tools is the profile's allowlist. Empty means the full inherited set.
	Tools []string
	// Deny removes tools even from the inherited set.
	Deny []string
	// CanSpawn lets the profile delegate further (depth permitting).
	CanSpawn bool
	// MaxSteps overrides the delegate step default when the spec has none.
	MaxSteps int
	// OutputTokens overrides the report budget when the spec has none.
	OutputTokens int
	// Guidance is appended to the delegate's task prompt.
	Guidance string
}

// profiles are intentionally small and opinionated: four roles cover the
// failure modes delegation exists to solve (not knowing the code, not knowing
// the plan, not trusting an edit, and doing the edit).
var profiles = []Profile{
	{
		Name:        "explorer",
		Description: "map unfamiliar code read-only and report file:line evidence",
		ReadOnly:    true,
		Tools: []string{
			"read", "list", "glob", "grep", "outline", "read_symbol",
			"search_files", "tree", "todos", "find_def", "repo_stats",
			"hash", "strings", "out", "jobs",
		},
		MaxSteps:     20,
		OutputTokens: 250,
		Guidance: "Role: explorer. You map unfamiliar code. Read broadly, follow call sites, and answer the question actually asked. " +
			"Report concrete evidence — file:line, symbol names, call chains — and say what you could not establish. Do not propose edits unless the task asks for them.",
	},
	{
		Name:        "planner",
		Description: "produce an ordered, verifiable implementation plan without editing",
		ReadOnly:    true,
		Tools: []string{
			"read", "list", "glob", "grep", "outline", "read_symbol",
			"search_files", "tree", "find_def", "repo_stats", "diff", "review", "out",
		},
		MaxSteps:     16,
		OutputTokens: 400,
		Guidance: "Role: planner. Produce a concrete, ordered plan that another agent could execute without asking questions. " +
			"Name the files and functions to change, the verification command for each step, and the risks. Do not write code files; a plan is your entire output.",
	},
	{
		Name:        "reviewer",
		Description: "inspect a change for defects read-only and cite the lines",
		ReadOnly:    true,
		Tools: []string{
			"read", "list", "glob", "grep", "outline", "read_symbol",
			"search_files", "tree", "find_def", "todos", "diff", "review", "out", "jobs",
		},
		MaxSteps:     20,
		OutputTokens: 300,
		Guidance: "Role: reviewer. Review the specified change for defects, not style preferences. Prioritize correctness, " +
			"edge cases, error paths, concurrency and security. Cite file:line for every finding, distinguish confirmed bugs from suspicions, and say clearly when you found nothing.",
	},
	{
		Name:         "editor",
		Description:  "implement a scoped change and verify it with the project's own commands",
		Tools:        nil, // inherit everything the delegator has
		MaxSteps:     30,
		OutputTokens: 300,
		Guidance: "Role: editor. Implement the requested change surgically. Read before editing, keep unrelated code untouched, " +
			"and run the project's verification command before reporting. If the task cannot be completed safely, stop and report exactly where it blocked.",
	},
}

var profileByName = func() map[string]Profile {
	out := make(map[string]Profile, len(profiles))
	for _, p := range profiles {
		out[p.Name] = p
	}
	return out
}()

// LookupProfile finds a profile by slug.
func LookupProfile(name string) (Profile, bool) {
	p, ok := profileByName[strings.ToLower(strings.TrimSpace(name))]
	return p, ok
}

// ProfileNames lists the available profile slugs, sorted.
func ProfileNames() []string {
	out := make([]string, 0, len(profiles))
	for _, p := range profiles {
		out = append(out, p.Name)
	}
	sort.Strings(out)
	return out
}

// Profiles returns a copy of the profile table for display.
func Profiles() []Profile {
	out := make([]Profile, len(profiles))
	copy(out, profiles)
	return out
}

// applyProfile merges p into spec. Explicit scalar fields win; allowlists
// narrow rather than widen, so a delegator can restrict a profile but never
// escape its guarantees (a read-only profile cannot be made writable).
func applyProfile(spec *Spec, p Profile) {
	if strings.TrimSpace(spec.Name) == "" {
		spec.Name = p.Name
	}
	if p.ReadOnly {
		spec.ReadOnly = true
	}
	if p.CanSpawn {
		spec.CanSpawn = true
	}
	if spec.MaxSteps <= 0 {
		spec.MaxSteps = p.MaxSteps
	}
	if spec.OutputTokens <= 0 {
		spec.OutputTokens = p.OutputTokens
	}
	spec.Tools = intersectTools(spec.Tools, p.Tools)
	spec.Deny = append(spec.Deny, p.Deny...)
	spec.role = p.Guidance
}

// intersectTools returns the allowlist a spawn should use. An empty profile
// list leaves the spawn's own list untouched; an empty spawn list adopts the
// profile's; otherwise only tools both allow survive.
func intersectTools(spawn, profile []string) []string {
	if len(profile) == 0 {
		return spawn
	}
	if len(spawn) == 0 {
		out := make([]string, len(profile))
		copy(out, profile)
		return out
	}
	allowed := make(map[string]bool, len(profile))
	for _, t := range profile {
		allowed[t] = true
	}
	var out []string
	for _, t := range spawn {
		if allowed[t] {
			out = append(out, t)
		}
	}
	return out
}
