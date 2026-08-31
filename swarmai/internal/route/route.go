// Package route classifies a prompt so the swarm can pick the right model: how
// hard it is (cheap small model vs. stronger large model) and which domain it
// belongs to (so a model specialized in that domain can be preferred).
//
// This is a transparent heuristic — a deliberate placeholder for a gate trained
// on real escalation logs. It is intentionally cheap so it can run locally on
// the weakest node before any model is invoked.
package route

import "strings"

// Difficulty levels.
const (
	Simple = "simple"
	Hard   = "hard"
)

// Domains.
const (
	Code    = "code"
	Math    = "math"
	General = "general"
)

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// Classify returns a difficulty and a domain for a prompt.
func Classify(prompt string) (difficulty, domain string) {
	p := strings.ToLower(prompt)

	switch {
	case containsAny(p, "func ", "def ", "import ", "class ", "```", "() {", "print(", "return ", "std::", "public "):
		domain = Code
	case containsAny(p, "prove", "integral", "derivative", "theorem", "equation", "matrix", "∑", "∫", "=", "+ ", " - "):
		domain = Math
	default:
		domain = General
	}

	hardCue := containsAny(p,
		"why", "prove", "explain", "step by step", "step-by-step", "design",
		"derive", "optimize", "trade-off", "tradeoff", "analyze", "compare",
		"reason", "algorithm", "complexity",
	)
	if len(prompt) > 400 || hardCue {
		difficulty = Hard
	} else {
		difficulty = Simple
	}
	return difficulty, domain
}

// TierRank maps a model tier to a comparable rank (small < medium < large).
func TierRank(tier string) int {
	switch tier {
	case "large":
		return 2
	case "medium":
		return 1
	default:
		return 0 // small / unknown
	}
}
