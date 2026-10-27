package configsync

import (
	"fmt"
	"strings"
)

// diffContext is the number of unchanged lines shown around a change.
const diffContext = 3

// maxDiffCells bounds the work of the line matcher. Larger inputs fall back
// to replacing the whole text, which is still a correct, if coarse, diff.
const maxDiffCells = 16 << 20

type diffOp struct {
	kind byte // ' ', '-' or '+'
	text string
}

// unifiedDiff returns a unified diff from a to b, or "" when they are equal.
// An empty side is shown as /dev/null so that creations and deletions read
// naturally.
func unifiedDiff(fromName, toName, a, b string) string {
	if a == b {
		return ""
	}
	from, to := splitLines(a), splitLines(b)
	ops := diffLines(from, to)

	var sb strings.Builder
	oldName, newName := "a/"+fromName, "b/"+toName
	if a == "" {
		oldName = "/dev/null"
	}
	if b == "" {
		newName = "/dev/null"
	}
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", oldName, newName)

	// Walk the script and emit one hunk per group of changes whose context
	// windows touch. oldLine and newLine are the 1-based line numbers of
	// ops[i] in the two texts.
	oldLine, newLine := 1, 1
	consumed := 0
	for i := 0; i < len(ops); {
		if ops[i].kind == ' ' {
			oldLine++
			newLine++
			i++
			continue
		}
		start := max(i-diffContext, consumed)
		last := i
		for j := i; j < len(ops); {
			if ops[j].kind != ' ' {
				last = j
				j++
				continue
			}
			k := j
			for k < len(ops) && ops[k].kind == ' ' {
				k++
			}
			if k == len(ops) || k-j > 2*diffContext {
				break
			}
			j = k
		}
		end := min(last+1+diffContext, len(ops))

		var body strings.Builder
		nOld, nNew := 0, 0
		for _, op := range ops[start:end] {
			body.WriteByte(op.kind)
			body.WriteString(op.text)
			body.WriteByte('\n')
			if op.kind != '+' {
				nOld++
			}
			if op.kind != '-' {
				nNew++
			}
		}
		fmt.Fprintf(&sb, "@@ -%s +%s @@\n", hunkRange(oldLine-(i-start), nOld), hunkRange(newLine-(i-start), nNew))
		sb.WriteString(body.String())

		for _, op := range ops[i:end] {
			if op.kind != '+' {
				oldLine++
			}
			if op.kind != '-' {
				newLine++
			}
		}
		i, consumed = end, end
	}
	return sb.String()
}

func hunkRange(start, n int) string {
	if n == 0 {
		return fmt.Sprintf("%d,0", max(start-1, 0))
	}
	if n == 1 {
		return fmt.Sprintf("%d", start)
	}
	return fmt.Sprintf("%d,%d", start, n)
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// diffLines computes an edit script with a longest-common-subsequence table
// after trimming the common prefix and suffix, which is nearly all of the
// input for the small edits this is used for.
func diffLines(a, b []string) []diffOp {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	ma, mb := a[pre:len(a)-suf], b[pre:len(b)-suf]

	ops := make([]diffOp, 0, len(a)+len(b))
	for _, l := range a[:pre] {
		ops = append(ops, diffOp{' ', l})
	}
	ops = append(ops, diffMiddle(ma, mb)...)
	for _, l := range a[len(a)-suf:] {
		ops = append(ops, diffOp{' ', l})
	}
	return ops
}

func diffMiddle(a, b []string) []diffOp {
	var ops []diffOp
	if len(a) == 0 || len(b) == 0 || len(a)*len(b) > maxDiffCells {
		for _, l := range a {
			ops = append(ops, diffOp{'-', l})
		}
		for _, l := range b {
			ops = append(ops, diffOp{'+', l})
		}
		return ops
	}
	// lcs[i][j] is the length of the longest common subsequence of a[i:]
	// and b[j:].
	w := len(b) + 1
	lcs := make([]int32, (len(a)+1)*w)
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i*w+j] = lcs[(i+1)*w+j+1] + 1
			} else {
				lcs[i*w+j] = max(lcs[(i+1)*w+j], lcs[i*w+j+1])
			}
		}
	}
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{' ', a[i]})
			i++
			j++
		case lcs[(i+1)*w+j] >= lcs[i*w+j+1]:
			ops = append(ops, diffOp{'-', a[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		ops = append(ops, diffOp{'-', a[i]})
	}
	for ; j < len(b); j++ {
		ops = append(ops, diffOp{'+', b[j]})
	}
	return ops
}
