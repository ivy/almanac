package views

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"slices"
	"strings"

	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/vocab"
)

// Options change what a page shows.
type Options struct {
	// Old is the version the page compares w with, or nil for none. With one, the page lists
	// the outcome diff and marks what was added and changed on the flow.
	Old *Summary
}

//go:embed page.html.tmpl
var pageSource string

var pageTemplate = template.Must(template.New("page").Parse(pageSource))

// Page draws w as one self-contained HTML page: the flow, every way a run can end, where
// hostile facts go and, given an old version, what changed. It has no script and loads
// nothing. Everything on it is a name or label from the model and its vocabulary.
func Page(v *vocab.Vocabulary, w model.Workflow, opts Options) ([]byte, error) {
	s, err := Outcomes(v, w)
	if err != nil {
		return nil, err
	}
	p := page{Summary: s}
	if opts.Old != nil {
		c := Compare(*opts.Old, s)
		p.Changes = &c
	}
	p.Stats = stats(s)
	p.Flow = layoutFlow(s, p.Changes)
	p.Taint = layoutHostile(s)
	var out bytes.Buffer
	if err := pageTemplate.Execute(&out, p); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// page is what the template draws.
type page struct {
	Summary
	Changes *Changes
	Stats   []string
	Flow    flowLayout
	Taint   hostileLayout
}

// ShortDigest is enough of a digest to tell versions apart.
func (p page) ShortDigest(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 12 {
		return d[:12]
	}
	return d
}

// Declared is each kind of effect, whether the workflow may produce it, and whether an ending
// or a hold does.
func (p page) Declared() []declared {
	used := map[effect.Kind]bool{}
	for _, o := range p.Endings {
		for _, e := range slices.Concat(o.Along, o.Effects) {
			used[e.Kind] = true
		}
	}
	var out []declared
	for _, k := range effect.Kinds() {
		out = append(out, declared{Kind: k.String(), May: slices.Contains(p.May, k), Used: used[k]})
	}
	return out
}

type declared struct {
	Kind      string
	May, Used bool
}

// EndingState is how an ending changed, for the outcomes table: "added", "changed", "path" or
// empty.
func (p page) EndingState(o Outcome) string {
	return endingState(p.Changes, o.ID())
}

func endingState(c *Changes, id string) string {
	if c == nil {
		return ""
	}
	for _, o := range c.Added {
		if o.ID() == id {
			return Added
		}
	}
	for _, ch := range c.Changed {
		if ch.Ending == id {
			return Changed
		}
	}
	for _, ch := range c.PathOnly {
		if ch.Ending == id {
			return "path"
		}
	}
	return ""
}

func stepState(c *Changes, id string) string {
	if c == nil {
		return ""
	}
	for _, s := range c.Steps {
		if s.ID == id && s.Change != Removed {
			return s.Change
		}
	}
	return ""
}

func stats(s Summary) []string {
	count := func(f func(Outcome) bool) int {
		n := 0
		for _, o := range s.Endings {
			if f(o) {
				n++
			}
		}
		return n
	}
	has := func(k effect.Kind) func(Outcome) bool {
		return func(o Outcome) bool {
			return slices.ContainsFunc(o.Effects, func(e Effect) bool { return e.Kind == k })
		}
	}
	return []string{
		fmt.Sprintf("%d ways to end", len(s.Endings)),
		fmt.Sprintf("an agent is called before %d", count(func(o Outcome) bool { return len(o.Agents) > 0 })),
		fmt.Sprintf("asks a person in %d", count(has(effect.KindAsk))),
		fmt.Sprintf("pushes in %d", count(has(effect.KindPush))),
		fmt.Sprintf("ends with no effects in %d", count(func(o Outcome) bool { return len(o.Effects) == 0 })),
	}
}

// The flow's geometry, in SVG user units. The spine of steps runs down the left; each ending
// sits to the right of the step that ends the run there.
const (
	flowWidth   = 900
	spineX      = 16
	spineWidth  = 340
	endX        = 420
	endWidth    = 464
	lineHeight  = 18
	smallLine   = 16
	boxPad      = 12
	gap         = 30
	endGap      = 10
	labelChars  = 40
	endChars    = 56
	smallChars  = 70
	stepTopText = 20
)

type flowLayout struct {
	Width, Height int
	Trigger       box
	Steps         []stepBox
	Spine         []edge
	Endings       []endBox
	// Hover highlights the path to an ending while the pointer is over it.
	Hover template.CSS
}

type box struct {
	X, Y, W, H int
	// Head is the small line at the top of the box.
	Head  string
	Lines []textLine
	// Title is the box's tooltip.
	Title string
}

// TextX is where the box's text starts.
func (b box) TextX() int { return b.X + boxPad }

// HeadY is the baseline of the box's head line.
func (b box) HeadY() int { return b.Y + stepTopText }

// FlagX is where a flag at the right of the head line ends.
func (b box) FlagX() int { return b.X + b.W - boxPad }

type textLine struct {
	Y     int
	Text  string
	Class string
}

type stepBox struct {
	Box   box
	Class string
	// Hostile is whether the step reads a hostile fact.
	Hostile bool
	State   string
}

type endBox struct {
	Box   box
	Class string
	Edge  string
	State string
	// Shows is whether the ending shows hostile text to a person.
	Shows bool
}

type edge struct {
	X1, Y1, X2, Y2 int
	Class          string
}

func layoutFlow(s Summary, c *Changes) flowLayout {
	f := flowLayout{Width: flowWidth}
	reads, shows := hostileSteps(s.Hostile)

	trigger := []string{"On " + s.On.Label}
	if s.When != nil {
		trigger = append(trigger, "when "+s.When.Text)
	}
	f.Trigger = textBox(spineX, 16, flowWidth-2*spineX, "trigger · "+s.On.Name, trigger, labelChars*2, "")
	y := f.Trigger.Y + f.Trigger.H + gap

	ended := map[string][]Outcome{}
	for _, o := range s.Endings {
		ended[o.Step] = append(ended[o.Step], o)
	}
	var hover strings.Builder
	prevBottom := f.Trigger.Y + f.Trigger.H
	for i, step := range s.Steps {
		// A step is on the path to every ending at or after it.
		var on []string
		for k := i; k < len(s.Steps); k++ {
			on = append(on, fmt.Sprintf("u%d", k))
		}
		f.Spine = append(f.Spine, edge{X1: spineX + spineWidth/2, Y1: prevBottom, X2: spineX + spineWidth/2, Y2: y - 2, Class: "spine " + strings.Join(on, " ")})
		b := textBox(spineX, y, spineWidth, string(step.Kind)+" · "+step.ID, []string{step.Label}, labelChars, step.Label+"\n"+step.Does)
		state := stepState(c, step.ID)
		f.Steps = append(f.Steps, stepBox{Box: b, Class: strings.Join(slices.Concat([]string{"step"}, strings.Fields(state), on), " "), Hostile: reads[step.ID], State: state})
		bottom := y + b.H

		ey := y
		for _, o := range ended[step.ID] {
			e := endingBox(o, ey)
			from := y + stepTopText
			to := ey + stepTopText
			e.Edge = fmt.Sprintf("M %d %d H %d V %d H %d", spineX+spineWidth, from, (spineX+spineWidth+endX)/2, to, endX-2)
			e.State = endingState(c, o.ID())
			e.Shows = shows[o.ID()]
			e.Class = strings.TrimSpace(fmt.Sprintf("end at%d %s", i, e.State))
			f.Endings = append(f.Endings, e)
			ey += e.Box.H + endGap
			bottom = max(bottom, ey-endGap)
		}
		if len(ended[step.ID]) > 0 {
			fmt.Fprintf(&hover, ".flow:has(.at%d:hover) .u%d{--edge:var(--accent);--box:var(--accent)}\n", i, i)
		}
		prevBottom = y + b.H
		y = bottom + gap
	}
	f.Height = y - gap + 16
	// Built only from step indexes, so it holds nothing from the model.
	f.Hover = template.CSS(hover.String())
	return f
}

func endingBox(o Outcome, y int) endBox {
	var briefs []string
	for _, e := range o.Effects {
		briefs = append(briefs, e.Brief)
	}
	effects := "no effects"
	if len(briefs) > 0 {
		effects = strings.Join(briefs, " · ")
	}
	title := o.Label
	if o.When != nil {
		title += "\n" + o.When.String()
	}
	for _, e := range o.Effects {
		title += "\n→ " + e.Text
	}
	b := textBox(endX, y, endWidth, o.ID(), []string{o.Label}, endChars, title)
	b.below(wrap(effects, smallChars), "effects")
	return endBox{Box: b}
}

// textBox is a box at x, y holding a head line and paragraphs wrapped to chars.
func textBox(x, y, w int, head string, paras []string, chars int, title string) box {
	b := box{X: x, Y: y, W: w, Head: head, Title: title}
	ty := y + stepTopText + lineHeight
	for i, para := range paras {
		class := "label"
		if i > 0 {
			class = "sub"
		}
		for _, line := range wrap(para, chars) {
			b.Lines = append(b.Lines, textLine{Y: ty, Text: line, Class: class})
			ty += lineHeight
		}
	}
	b.H = ty - y - lineHeight + boxPad
	return b
}

// below adds small lines of text under the box's last line, and grows the box to hold them.
func (b *box) below(lines []string, class string) {
	y := b.Y + stepTopText
	if len(b.Lines) > 0 {
		y = b.Lines[len(b.Lines)-1].Y
	}
	for _, line := range lines {
		y += smallLine
		b.Lines = append(b.Lines, textLine{Y: y, Text: line, Class: class})
	}
	b.H = y - b.Y + boxPad
}

// wrap breaks text into lines of at most width characters, at spaces.
func wrap(text string, width int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		switch {
		case line == "":
			line = word
		case len([]rune(line))+1+len([]rune(word)) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// hostileSteps are the steps that read a hostile fact, and the endings that show one to a
// person.
func hostileSteps(hs []Hostile) (reads, shows map[string]bool) {
	reads, shows = map[string]bool{}, map[string]bool{}
	for _, h := range hs {
		for _, u := range h.Uses {
			reads[u.Step] = true
			if u.Rule != "" && (u.As == AsMap || u.As == AsText) {
				shows[u.Step+"/"+u.Rule] = true
			}
		}
	}
	return reads, shows
}

// The hostile view's geometry: hostile facts on the left, each beside the places it is read.
const (
	factX     = 16
	factWidth = 300
	useX      = 440
	useWidth  = 444
	factChars = 36
	useChars  = 56
)

type hostileLayout struct {
	Width, Height int
	Facts         []box
	Uses          []useBox
}

type useBox struct {
	Box   box
	Class string
	Edge  string
}

func layoutHostile(s Summary) hostileLayout {
	l := hostileLayout{Width: flowWidth}
	labels := map[string]string{}
	for _, st := range s.Steps {
		labels[st.ID] = st.Label
	}
	for _, o := range s.Endings {
		labels[o.ID()] = o.Label
	}
	y := 16
	for _, h := range s.Hostile {
		from := "from " + h.FromLabel
		fact := textBox(factX, y, factWidth, "hostile "+h.Type, []string{string(h.Ref), from}, factChars, "")
		uy := y
		for _, u := range h.Uses {
			where := u.Step
			if u.Rule != "" {
				where += "/" + u.Rule
			}
			head := string(u.As) + " · " + where
			if label := labels[where]; label != "" {
				head += " · " + label
			}
			b := textBox(useX, uy, useWidth, head, []string{u.Label}, useChars, "")
			from := y + stepTopText
			to := uy + stepTopText
			mid := (factX + factWidth + useX) / 2
			edge := fmt.Sprintf("M %d %d C %d %d %d %d %d %d", factX+factWidth, from, mid, from, mid, to, useX-2, to)
			l.Uses = append(l.Uses, useBox{Box: b, Class: "use as-" + string(u.As), Edge: edge})
			uy += b.H + endGap
		}
		if len(h.Uses) == 0 {
			fact.below([]string{"read by nothing"}, "effects")
		}
		l.Facts = append(l.Facts, fact)
		y = max(y+fact.H, uy-endGap) + gap
	}
	l.Height = y - gap + 16
	return l
}
