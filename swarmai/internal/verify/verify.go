// Package verify implements the draft→verify pattern (M2): a weak node produces
// a draft answer locally with its small model, and a stronger node verifies or
// corrects it in a single pass. Over a WAN this amortizes latency — one
// round-trip checks a whole draft instead of one round-trip per token, which is
// the structural win token-level speculative decoding cannot give across the
// public internet.
package verify

import "strings"

// Protocol is the libp2p stream protocol id for draft verification.
const Protocol = "/swarmai/verify/1.0.0"

// Request carries a prompt and the weak node's local draft to a verifier.
type Request struct {
	Prompt    string `json:"prompt"`
	Draft     string `json:"draft"`
	Model     string `json:"model,omitempty"`
	MaxTokens int    `json:"max_tokens,omitempty"`
}

// Verdict is the verifier's answer: either the draft is accepted, or a
// correction is returned. Answer is always the final text to use.
type Verdict struct {
	Accepted   bool   `json:"accepted"`
	Answer     string `json:"answer"`
	Note       string `json:"note"`
	VerifiedBy string `json:"verified_by,omitempty"`
	Err        string `json:"error,omitempty"`
}

// acceptToken is the sentinel a verifier emits when the draft needs no change.
const acceptToken = "ACCEPT"

// BuildVerifyPrompt frames a (prompt, draft) pair as a single verification task
// for the big model: confirm the draft or return a correction.
func BuildVerifyPrompt(prompt, draft string) string {
	var b strings.Builder
	b.WriteString("You verify a draft answer produced by a smaller model.\n\n")
	b.WriteString("Question:\n")
	b.WriteString(prompt)
	b.WriteString("\n\nDraft answer:\n")
	b.WriteString(draft)
	b.WriteString("\n\nIf the draft is correct and complete, reply with exactly one word: ")
	b.WriteString(acceptToken)
	b.WriteString("\nOtherwise reply with the corrected answer only, nothing else.")
	return b.String()
}

// Interpret turns the verifier model's raw output into a Verdict, given the
// original draft it was checking.
func Interpret(draft, out string) Verdict {
	trimmed := strings.TrimSpace(out)
	if trimmed == acceptToken || strings.HasPrefix(trimmed, acceptToken+"\n") || strings.HasPrefix(trimmed, acceptToken+" ") {
		return Verdict{Accepted: true, Answer: draft, Note: "draft accepted by verifier"}
	}
	return Verdict{Accepted: false, Answer: trimmed, Note: "draft corrected by verifier"}
}
