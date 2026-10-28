package filtereddiff

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// An author bringing their branch up to date merges the base branch into it,
// and a diff from the baseline to the head then carries everything the base
// branch changed in between, which is none of the PR's work. GitHub can't
// leave it out: it diffs commits, and the merge put the base branch's changes
// in the head as much as the author's. WithoutMerged does, line by line, from
// the PR's own diff (against its merge base) at the baseline and at the head.

// MergedAfter returns the commits merged into the PR from outside it after
// sha, in the order the PR's commits merged them: the parents of the commits
// sha doesn't reach that aren't PR commits themselves. Those are the base
// branch commits a merge brought in — the PR's commits are what its base
// branch doesn't have, so a merged-in branch of its own is among them. known
// is false when the commits can't tell: sha or the head is missing from them,
// or one of the commits after sha was cached without its parents.
func (in Inputs) MergedAfter(sha string) (merged []string, known bool) {
	if !in.ContainsCommit(sha) || !in.ContainsCommit(in.HeadSHA) {
		return nil, false
	}
	bySHA := make(map[string]Commit, len(in.Commits))
	for _, c := range in.Commits {
		bySHA[c.SHA] = c
	}
	// Walk sha's ancestry rather than trusting the list's order to say what
	// came before it.
	reached := map[string]bool{}
	for stack := []string{sha}; len(stack) > 0; {
		s := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		c, ours := bySHA[s]
		if !ours || reached[s] {
			continue
		}
		if c.Parents == nil {
			return nil, false
		}
		reached[s] = true
		stack = append(stack, c.Parents...)
	}
	seen := map[string]bool{}
	for _, c := range in.Commits {
		if reached[c.SHA] {
			continue
		}
		if c.Parents == nil {
			return nil, false
		}
		for _, p := range c.Parents {
			if _, ours := bySHA[p]; !ours && !seen[p] {
				seen[p] = true
				merged = append(merged, p)
			}
		}
	}
	return merged, true
}

// WithoutMerged narrows rangeDiff, the diff from the baseline B to the head
// H, to the PR's own changes since B, leaving out what merging the base branch
// in brought. before is the PR's diff at the baseline (from its merge base M1
// to B) and after its diff at the head (from the newer merge base M2 to H):
// GitHub's diff of the PR then and now. What is left is the diff from X, the
// baseline with the same base branch commits merged in, to H.
//
// The diffs are read in the head's lines, the baseline's related to them
// through rangeDiff, and each line that differs is the PR's or the merge's:
//
//   - A line of H that B lacks (rangeDiff adds it) is the PR's when M2 lacks
//     it too (after adds it), or when before removed it: the PR putting back
//     a line of the base it had taken out. Otherwise the merge brought it.
//   - A line of H that B has is the PR's when after adds it and before
//     didn't: the PR keeping a line the base branch removed.
//   - A line of B that H lacks (rangeDiff removes it) is the PR's when before
//     added it, the PR taking out a line of its own, or when after removes it
//     too. Otherwise the base branch removed it.
//   - A line of M2 that neither B nor H has (after alone removes it) is the
//     PR's unless before removed it too: a line the merge brought in that the
//     PR took out.
//
// A removed line is matched across diffs by its text and where it sits among
// the lines both diffs share. The PR's lines are cut into hunks with
// diffContext lines of context, as git does; a file left with none is
// dropped, and one the merge added or renamed reads as changed in place.
func WithoutMerged(rangeDiff, before, after string) string {
	ranged := parseUnified(rangeDiff)
	if len(ranged) == 0 && strings.TrimSpace(rangeDiff) != "" {
		return rangeDiff
	}
	was := byKey(parseUnified(before))
	nowFiles := parseUnified(after)
	now := byKey(nowFiles)

	var out strings.Builder
	inRange := map[string]bool{}
	for _, r := range ranged {
		inRange[r.key()] = true
		writeNarrowed(&out, r, now[r.key()], was)
	}
	// A file the range diff doesn't touch is one the PR left as the merge
	// brought it — or put back the way the baseline had it, undoing the
	// base branch's change.
	for _, n := range nowFiles {
		if !inRange[n.key()] {
			writeNarrowed(&out, nil, n, was)
		}
	}
	return out.String()
}

// writeNarrowed writes one file of WithoutMerged's diff, from its range diff
// r and its head diff n, either of which may be missing, and was, the
// baseline diff's files.
func writeNarrowed(out *strings.Builder, r, n *fileDiff, was map[string]*fileDiff) {
	// The file as the baseline has it: the range diff's old side, or, with
	// the file untouched since the baseline, the head's.
	baselineKey := ""
	switch {
	case r == nil:
		baselineKey = n.key()
	case r.oldPath != "":
		baselineKey = r.oldPath
	default:
		baselineKey = deletedKey(r.newPath)
	}
	o := was[baselineKey]

	ownRename := r != nil && r.renamed() && n != nil && n.renamed()
	if r != nil && len(r.hunks) == 0 {
		// A binary, mode-only or rename-only change: the PR's when its own
		// diffs touch the file.
		if ownRename || (!r.renamed() && (n != nil || o != nil)) {
			writeLines(out, r.header)
		}
		return
	}

	var header []string
	switch {
	case r != nil:
		header = r.header
		// M2 has the file when the head diff doesn't add it, and then so
		// does the baseline with M2 merged in.
		mergeAdded := r.oldPath == "" && (n == nil || n.oldPath != "")
		if mergeAdded || (r.renamed() && !ownRename) {
			header = asModified(header, r.newPath)
		}
	case n.newPath != "":
		// The baseline has the file as the head does, whatever M2 had.
		header = asModified(n.header, n.newPath)
	default:
		header = n.header
	}
	hunks := narrow(r, n, o).cut(r, n)
	if len(hunks) == 0 && !ownRename {
		return
	}
	writeLines(out, header)
	for _, h := range hunks {
		h.write(out)
	}
}

// fileDiff is one file's section of a unified diff.
type fileDiff struct {
	// header holds the lines before the first hunk, from "diff --git" on.
	header []string
	// oldPath and newPath name the file on each side; empty for /dev/null.
	oldPath, newPath string
	hunks            []hunk
}

func (f *fileDiff) renamed() bool {
	return f.oldPath != "" && f.newPath != "" && f.oldPath != f.newPath
}

type hunk struct {
	// oldBefore and newBefore count the lines of each side before the hunk.
	oldBefore, newBefore int
	section              string
	lines                []diffLine
}

type diffLine struct {
	op   byte // ' ', '+' or '-'
	text string
	// noEOL is set when "\ No newline at end of file" follows the line.
	noEOL bool
}

// parseUnified reads a git unified diff. Hunks are read by their line counts,
// so a removed line that reads "-- x" is not taken for a file header; a line
// it doesn't recognize is skipped.
func parseUnified(diff string) []*fileDiff {
	lines := strings.Split(diff, "\n")
	var files []*fileDiff
	var f *fileDiff
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		switch {
		case strings.HasPrefix(l, "diff --git "):
			f = &fileDiff{header: []string{l}}
			f.oldPath, f.newPath = gitDiffPaths(l)
			files = append(files, f)
		case f == nil:
			// Nothing before the first file is part of one.
		case strings.HasPrefix(l, "@@ "):
			h, oldLeft, newLeft, ok := parseHunkHeader(l)
			if !ok {
				continue
			}
			for i+1 < len(lines) && (oldLeft > 0 || newLeft > 0) {
				i++
				l := lines[i]
				op := byte(' ')
				if l != "" {
					op = l[0]
				}
				switch op {
				case ' ':
					oldLeft--
					newLeft--
				case '-':
					oldLeft--
				case '+':
					newLeft--
				case '\\':
					markNoEOL(&h)
					continue
				default:
					// Not a hunk line: the counts were wrong. Read it again
					// as what it is.
					i--
					oldLeft, newLeft = 0, 0
					continue
				}
				text := ""
				if l != "" {
					text = l[1:]
				}
				h.lines = append(h.lines, diffLine{op: op, text: text})
			}
			if i+1 < len(lines) && strings.HasPrefix(lines[i+1], `\`) {
				i++
				markNoEOL(&h)
			}
			f.hunks = append(f.hunks, h)
		case len(f.hunks) > 0:
			// Past a file's hunks, only another file starts something.
		default:
			f.header = append(f.header, l)
			switch {
			case l == "--- /dev/null", strings.HasPrefix(l, "new file mode"):
				f.oldPath = ""
			case l == "+++ /dev/null", strings.HasPrefix(l, "deleted file mode"):
				f.newPath = ""
			case strings.HasPrefix(l, "--- a/"):
				f.oldPath = strings.TrimSuffix(strings.TrimPrefix(l, "--- a/"), "\t")
			case strings.HasPrefix(l, "+++ b/"):
				f.newPath = strings.TrimSuffix(strings.TrimPrefix(l, "+++ b/"), "\t")
			case strings.HasPrefix(l, "rename from "):
				f.oldPath = strings.TrimPrefix(l, "rename from ")
			case strings.HasPrefix(l, "rename to "):
				f.newPath = strings.TrimPrefix(l, "rename to ")
			}
		}
	}
	return files
}

func markNoEOL(h *hunk) {
	if n := len(h.lines); n > 0 {
		h.lines[n-1].noEOL = true
	}
}

// gitDiffPaths reads the paths from a "diff --git a/X b/Y" line.
func gitDiffPaths(line string) (oldPath, newPath string) {
	rest, ok := strings.CutPrefix(line, "diff --git a/")
	if !ok {
		return "", ""
	}
	oldPath, newPath, _ = strings.Cut(rest, " b/")
	return oldPath, newPath
}

// parseHunkHeader reads "@@ -a,b +c,d @@ section": the lines before the
// hunk on each side and the lines in it. A range of no lines names the line it
// comes after, any other its first line.
func parseHunkHeader(line string) (h hunk, oldLen, newLen int, ok bool) {
	ranges, section, ok := strings.Cut(strings.TrimPrefix(line, "@@ "), " @@")
	fields := strings.Fields(ranges)
	if !ok || len(fields) != 2 || fields[0][0] != '-' || fields[1][0] != '+' {
		return hunk{}, 0, 0, false
	}
	parse := func(r string) (before, length int, ok bool) {
		start, count, hasCount := strings.Cut(r[1:], ",")
		s, err := strconv.Atoi(start)
		if err != nil {
			return 0, 0, false
		}
		length = 1
		if hasCount {
			if length, err = strconv.Atoi(count); err != nil {
				return 0, 0, false
			}
		}
		if length == 0 {
			return s, 0, true
		}
		return s - 1, length, true
	}
	h.section = strings.TrimPrefix(section, " ")
	var okOld, okNew bool
	h.oldBefore, oldLen, okOld = parse(fields[0])
	h.newBefore, newLen, okNew = parse(fields[1])
	return h, oldLen, newLen, okOld && okNew
}

// key names a file across the three diffs: by its head path, or, for a file
// the diff deletes, by the path it had.
func (f *fileDiff) key() string {
	if f.newPath == "" {
		return deletedKey(f.oldPath)
	}
	return f.newPath
}

func deletedKey(path string) string { return "\x00" + path }

func byKey(files []*fileDiff) map[string]*fileDiff {
	m := make(map[string]*fileDiff, len(files))
	for _, f := range files {
		m[f.key()] = f
	}
	return m
}

// sideOf indexes a file's diff by its new side: the lines it adds, and the
// lines it removes by how many new-side lines come before them.
func sideOf(f *fileDiff) (added map[int]bool, removed *removals) {
	added, removed = map[int]bool{}, &removals{at: map[removal][]int{}}
	if f == nil {
		return added, removed
	}
	for _, h := range f.hunks {
		newLine := h.newBefore
		for i := 0; i < len(h.lines); {
			if op := h.lines[i].op; op != '-' && op != '+' {
				newLine++
				i++
				continue
			}
			// A change: git lists its removals, then its additions, so a
			// removal belongs anywhere among them.
			first := len(removed.lines)
			for ; i < len(h.lines) && h.lines[i].op == '-'; i++ {
				removed.add(newLine, h.lines[i])
			}
			end := newLine
			for ; i < len(h.lines) && h.lines[i].op == '+'; i++ {
				end++
				added[end] = true
			}
			for j := first; j < len(removed.lines); j++ {
				removed.lines[j].end = end
			}
			newLine = end
		}
	}
	return added, removed
}

// removals are a diff's removed lines, each to be matched once.
type removals struct {
	lines []removedLine
	at    map[removal][]int
}

type removedLine struct {
	// gap and end bound where the line goes: after gap new-side lines, and
	// after no more than end, the additions of its change between them.
	gap, end int
	line     diffLine
	taken    bool
}

// removal is a removed line's text and the count of new-side lines before it.
type removal struct {
	gap  int
	text string
}

// removalSlack is how far apart two diffs may place the same removed line:
// diffing a different old side against the same new one can slide a run of
// removals by a line or two when its ends repeat.
const removalSlack = 3

func (rs *removals) add(gap int, l diffLine) {
	k := removal{gap, l.text}
	rs.at[k] = append(rs.at[k], len(rs.lines))
	rs.lines = append(rs.lines, removedLine{gap: gap, end: gap, line: l})
}

// take matches a removal of text at gap, or the nearest within
// removalSlack, and returns its index in lines.
func (rs *removals) take(gap int, text string) (int, bool) {
	for d := 0; d <= removalSlack; d++ {
		for _, g := range []int{gap - d, gap + d} {
			for _, i := range rs.at[removal{g, text}] {
				if !rs.lines[i].taken {
					rs.lines[i].taken = true
					return i, true
				}
			}
			if d == 0 {
				break
			}
		}
	}
	return 0, false
}

// narrowing is a file's diff from X to H, in H's lines.
type narrowing struct {
	// text holds the lines of H the diffs show.
	text map[int]diffLine
	// added holds the lines of H that X lacks.
	added map[int]bool
	// removed holds the lines of X that H lacks, by how many lines of H
	// come before them.
	removed map[int][]diffLine
}

// narrow sorts a file's changes into the PR's and the merge's (see
// WithoutMerged), from its range diff r, head diff n and baseline diff o.
func narrow(r, n, o *fileDiff) *narrowing {
	nr := &narrowing{text: map[int]diffLine{}, added: map[int]bool{}, removed: map[int][]diffLine{}}
	ownAdded, ownRemoved := sideOf(o)
	nowAdded, nowRemoved := sideOf(n)
	for _, f := range []*fileDiff{r, n} {
		if f == nil {
			continue
		}
		for _, h := range f.hunks {
			newLine := h.newBefore
			for _, l := range h.lines {
				if l.op == '-' {
					continue
				}
				newLine++
				l.noEOL = l.noEOL || nr.text[newLine].noEOL
				nr.text[newLine] = l
			}
		}
	}

	// Removals at one point of H go in the order the head diff has them,
	// which is X's there; one of the PR's own lines, which it doesn't show,
	// follows the line before it in B.
	type keyedRemoval struct {
		key  int
		line diffLine
	}
	pending := map[int][]keyedRemoval{}
	place := func(gap, key int, l diffLine) { pending[gap] = append(pending[gap], keyedRemoval{key, l}) }
	lastKey := -1

	rangeAdded := map[int]bool{}
	if r != nil {
		type rangeRemoval struct {
			line diffLine
			// at is the line's number in B, gap the lines of H before it.
			at, gap int
		}
		for _, h := range r.hunks {
			oldLine, newLine := h.oldBefore, h.newBefore
			for i := 0; i < len(h.lines); {
				if op := h.lines[i].op; op != '-' && op != '+' {
					oldLine++
					newLine++
					i++
					continue
				}
				// A change: git lists its removals, then its additions.
				var gone []rangeRemoval
				for ; i < len(h.lines) && h.lines[i].op == '-'; i++ {
					oldLine++
					gone = append(gone, rangeRemoval{h.lines[i], oldLine, newLine})
				}
				firstOurs := 0
				for ; i < len(h.lines) && h.lines[i].op == '+'; i++ {
					newLine++
					rangeAdded[newLine] = true
					ours := nowAdded[newLine]
					if !ours {
						_, ours = ownRemoved.take(oldLine, h.lines[i].text)
					}
					if ours {
						nr.added[newLine] = true
						if firstOurs == 0 {
							firstOurs = newLine
						}
					}
				}
				// The head diff places a line of the base; one of the PR's
				// own, which it doesn't show, goes before the PR's first
				// addition here.
				ownGap := newLine
				if firstOurs > 0 {
					ownGap = firstOurs - 1
				}
				for _, g := range gone {
					if ownAdded[g.at] {
						place(ownGap, lastKey+1, g.line)
					} else if j, ok := nowRemoved.take(g.gap, g.line.text); ok {
						// Each diff bounds the line by its own change's
						// additions; it goes first where both allow.
						match := nowRemoved.lines[j]
						lastKey = 2 * j
						place(min(max(g.gap, match.gap), newLine, match.end), lastKey, g.line)
					}
				}
			}
		}
	}

	for h := range nowAdded {
		if rangeAdded[h] {
			continue
		}
		if b := baselineLine(r, h); b == 0 || !ownAdded[b] {
			nr.added[h] = true
		}
	}
	for j, rm := range nowRemoved.lines {
		if rm.taken {
			continue
		}
		if _, pre := ownRemoved.take(baselineGap(r, rm.gap), rm.line.text); !pre {
			place(rm.gap, 2*j, rm.line)
		}
	}
	for gap, rms := range pending {
		sort.SliceStable(rms, func(a, b int) bool { return rms[a].key < rms[b].key })
		for _, rm := range rms {
			nr.removed[gap] = append(nr.removed[gap], rm.line)
		}
	}
	return nr
}

// baselineLine returns the line of B that line h of H is, through the range
// diff r, or 0 when r adds it. With no range diff, B has the file as H does.
func baselineLine(r *fileDiff, h int) int {
	if r == nil {
		return h
	}
	shift := 0
	for _, hk := range r.hunks {
		if h <= hk.newBefore {
			break
		}
		oldLine, newLine := hk.oldBefore, hk.newBefore
		for _, l := range hk.lines {
			switch l.op {
			case '-':
				oldLine++
			case '+':
				if newLine++; newLine == h {
					return 0
				}
			default:
				oldLine++
				if newLine++; newLine == h {
					return oldLine
				}
			}
		}
		shift = oldLine - newLine
	}
	return h + shift
}

// baselineGap returns how many lines of B come before the point after line g
// of H, through the range diff r.
func baselineGap(r *fileDiff, g int) int {
	if r == nil {
		return g
	}
	shift := 0
	for _, hk := range r.hunks {
		if g <= hk.newBefore {
			break
		}
		oldLine, newLine := hk.oldBefore, hk.newBefore
		for _, l := range hk.lines {
			if newLine == g {
				return oldLine
			}
			if l.op != '+' {
				oldLine++
			}
			if l.op != '-' {
				newLine++
			}
		}
		if newLine == g {
			return oldLine
		}
		shift = oldLine - newLine
	}
	return g + shift
}

// diffContext is how many unchanged lines a hunk keeps around its changes, as
// git does.
const diffContext = 3

// cut cuts the PR's changes into hunks. A change's context is the lines of H
// around it that the diffs show, up to diffContext each side, and changes
// whose context meets share a hunk. A hunk starting where one of r's or n's
// does keeps its section heading.
func (nr *narrowing) cut(r, n *fileDiff) []hunk {
	known := func(h int) bool {
		_, ok := nr.text[h]
		return h >= 1 && ok
	}
	// A span is a hunk's lines of H, first to last, with the removals after
	// each of first-1 to last; a hunk of removals alone has last = first-1.
	type span struct{ first, last int }
	var spans []span
	around := func(first, last int) {
		for k := 0; k < diffContext && known(first-1); k++ {
			first--
		}
		for k := 0; k < diffContext && known(last+1); k++ {
			last++
		}
		spans = append(spans, span{first, last})
	}
	for h := range nr.added {
		around(h, h)
	}
	for g := range nr.removed {
		around(g+1, g)
	}
	if len(spans) == 0 {
		return nil
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].first != spans[j].first {
			return spans[i].first < spans[j].first
		}
		return spans[i].last < spans[j].last
	})
	merged := spans[:1]
	for _, s := range spans[1:] {
		if cur := &merged[len(merged)-1]; s.first <= cur.last+1 {
			cur.last = max(cur.last, s.last)
		} else {
			merged = append(merged, s)
		}
	}

	sections := map[int]string{}
	for _, f := range []*fileDiff{n, r} {
		if f == nil {
			continue
		}
		for _, h := range f.hunks {
			if h.section != "" {
				sections[h.newBefore] = h.section
			}
		}
	}

	hunks := make([]hunk, 0, len(merged))
	for _, s := range merged {
		h := hunk{oldBefore: s.first - 1, newBefore: s.first - 1, section: sections[s.first-1]}
		for a := range nr.added {
			if a < s.first {
				h.oldBefore--
			}
		}
		for g, lines := range nr.removed {
			if g < s.first-1 {
				h.oldBefore += len(lines)
			}
		}
		for g := s.first - 1; g <= s.last; g++ {
			if g >= s.first {
				l := nr.text[g]
				l.op = ' '
				if nr.added[g] {
					l.op = '+'
				}
				h.lines = append(h.lines, l)
			}
			for _, l := range nr.removed[g] {
				l.op = '-'
				h.lines = append(h.lines, l)
			}
		}
		hunks = append(hunks, h)
	}
	return hunks
}

func (h hunk) write(out *strings.Builder) {
	oldLen, newLen := 0, 0
	for _, l := range h.lines {
		if l.op != '+' {
			oldLen++
		}
		if l.op != '-' {
			newLen++
		}
	}
	fmt.Fprintf(out, "@@ -%s +%s @@", hunkRange(h.oldBefore, oldLen), hunkRange(h.newBefore, newLen))
	if h.section != "" {
		out.WriteString(" " + h.section)
	}
	out.WriteByte('\n')
	for _, l := range h.lines {
		out.WriteByte(l.op)
		out.WriteString(l.text)
		out.WriteByte('\n')
		if l.noEOL {
			out.WriteString("\\ No newline at end of file\n")
		}
	}
}

// hunkRange writes one side of a hunk header as git does: a range of no lines
// names the line it comes after, and a count of one is left out.
func hunkRange(before, length int) string {
	switch length {
	case 0:
		return fmt.Sprintf("%d,0", before)
	case 1:
		return strconv.Itoa(before + 1)
	}
	return fmt.Sprintf("%d,%d", before+1, length)
}

// asModified rewrites a file's header as a change in place at path: the
// merge added or renamed the file, so the old side has it there already.
func asModified(header []string, path string) []string {
	out := make([]string, 0, len(header))
	for _, l := range header {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			l = "diff --git a/" + path + " b/" + path
		case strings.HasPrefix(l, "new file mode"), strings.HasPrefix(l, "rename "),
			strings.HasPrefix(l, "similarity index"), strings.HasPrefix(l, "dissimilarity index"):
			continue
		case strings.HasPrefix(l, "--- "):
			l = "--- a/" + path
		}
		out = append(out, l)
	}
	return out
}

func writeLines(out *strings.Builder, lines []string) {
	for _, l := range lines {
		out.WriteString(l)
		out.WriteByte('\n')
	}
}
