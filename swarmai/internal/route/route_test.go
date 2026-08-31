package route

import (
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		in       string
		wantDiff string
		wantDom  string
	}{
		{"def add(a,b): return a+b", Simple, Code},
		{"Prove the Pythagorean theorem step by step", Hard, Math},
		{"hello there", Simple, General},
		{strings.Repeat("word ", 100), Hard, General}, // long → hard
	}
	for _, c := range cases {
		d, dom := Classify(c.in)
		if d != c.wantDiff || dom != c.wantDom {
			t.Errorf("Classify(%q) = (%s,%s), want (%s,%s)", c.in, d, dom, c.wantDiff, c.wantDom)
		}
	}
}

func TestTierRank(t *testing.T) {
	if !(TierRank("large") > TierRank("medium") && TierRank("medium") > TierRank("small")) {
		t.Fatal("tier order must be small < medium < large")
	}
	if TierRank("unknown") != 0 {
		t.Fatal("unknown tier should rank as small")
	}
}
