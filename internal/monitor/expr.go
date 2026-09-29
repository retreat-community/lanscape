package monitor

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Expr is a parsed composite expression: "#1 && (#2 || !#3)".
type Expr interface {
	// Eval returns the value; known=false when a referenced monitor has no state yet.
	Eval(up func(id int64) (value, known bool)) (value, known bool)
	IDs() []int64
}

type ref int64
type not struct{ x Expr }
type bin struct {
	and  bool
	l, r Expr
}

func (e ref) Eval(up func(int64) (bool, bool)) (bool, bool) { return up(int64(e)) }
func (e ref) IDs() []int64                                  { return []int64{int64(e)} }

func (e not) Eval(up func(int64) (bool, bool)) (bool, bool) {
	v, k := e.x.Eval(up)
	return !v, k
}
func (e not) IDs() []int64 { return e.x.IDs() }

func (e bin) Eval(up func(int64) (bool, bool)) (bool, bool) {
	lv, lk := e.l.Eval(up)
	rv, rk := e.r.Eval(up)
	if e.and {
		// false && unknown is false; true && unknown is unknown
		if (lk && !lv) || (rk && !rv) {
			return false, true
		}
		return lv && rv, lk && rk
	}
	if (lk && lv) || (rk && rv) {
		return true, true
	}
	return lv || rv, lk && rk
}
func (e bin) IDs() []int64 { return append(e.l.IDs(), e.r.IDs()...) }

type parser struct {
	toks []string
	pos  int
}

func tokenize(s string) ([]string, error) {
	var out []string
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t':
			i++
		case c == '(' || c == ')' || c == '!':
			out = append(out, string(c))
			i++
		case strings.HasPrefix(s[i:], "&&") || strings.HasPrefix(s[i:], "||"):
			out = append(out, s[i:i+2])
			i += 2
		case c == '#':
			j := i + 1
			for j < len(s) && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			if j == i+1 {
				return nil, fmt.Errorf("monitor id expected at %d", i)
			}
			out = append(out, s[i:j])
			i = j
		default:
			return nil, fmt.Errorf("unexpected %q at %d", c, i)
		}
	}
	return out, nil
}

// ParseExpr parses a composite expression. Operators: ! (not), && (and), || (or), parentheses;
// operands are monitor ids written as #id.
func ParseExpr(s string) (Expr, error) {
	toks, err := tokenize(s)
	if err != nil {
		return nil, err
	}
	if len(toks) == 0 {
		return nil, errors.New("empty expression")
	}
	p := &parser{toks: toks}
	e, err := p.or()
	if err != nil {
		return nil, err
	}
	if p.pos != len(p.toks) {
		return nil, fmt.Errorf("unexpected %q", p.toks[p.pos])
	}
	return e, nil
}

func (p *parser) peek() string {
	if p.pos < len(p.toks) {
		return p.toks[p.pos]
	}
	return ""
}

func (p *parser) or() (Expr, error) {
	l, err := p.and()
	for err == nil && p.peek() == "||" {
		p.pos++
		var r Expr
		if r, err = p.and(); err == nil {
			l = bin{and: false, l: l, r: r}
		}
	}
	return l, err
}

func (p *parser) and() (Expr, error) {
	l, err := p.unary()
	for err == nil && p.peek() == "&&" {
		p.pos++
		var r Expr
		if r, err = p.unary(); err == nil {
			l = bin{and: true, l: l, r: r}
		}
	}
	return l, err
}

func (p *parser) unary() (Expr, error) {
	switch t := p.peek(); {
	case t == "!":
		p.pos++
		x, err := p.unary()
		return not{x}, err
	case t == "(":
		p.pos++
		x, err := p.or()
		if err != nil {
			return nil, err
		}
		if p.peek() != ")" {
			return nil, errors.New("missing )")
		}
		p.pos++
		return x, nil
	case strings.HasPrefix(t, "#"):
		p.pos++
		id, err := strconv.ParseInt(t[1:], 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("bad monitor id %s", t)
		}
		return ref(id), nil
	case t == "":
		return nil, errors.New("unexpected end of expression")
	default:
		return nil, fmt.Errorf("unexpected %q", t)
	}
}
