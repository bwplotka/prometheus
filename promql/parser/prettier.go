// Copyright 2022 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package parser

import (
	"bytes"
	"fmt"
	"strings"
)

// Approach
// --------
// When a PromQL query is parsed, it is converted into PromQL AST,
// which is a nested structure of nodes. Each node has a depth/level
// (distance from the root), that is passed by its parent.
//
// While prettifying, a Node considers 2 things:
// 1. Did the current Node's parent add a new line?
// 2. Does the current Node needs to be prettified?
//
// The level of a Node determines if it should be indented or not.
// The answer to the 1 is NO if the level passed is 0. This means, the
// parent Node did not apply a new line, so the current Node must not
// apply any indentation as prefix.
// If level > 1, a new line is applied by the parent. So, the current Node
// should prefix an indentation before writing any of its content. This indentation
// will be ([level/depth of current Node] * "  ").
//
// The answer to 2 is YES if the normalized length of the current Node exceeds
// the maxCharactersPerLine limit. Hence, it applies the indentation equal to
// its depth and increments the level by 1 before passing down the child.
// If the answer is NO, the current Node returns the normalized string value of itself.

var maxCharactersPerLine = 100

type PrettifyMode int

var (
	PrettifyPromQLMode      PrettifyMode = 0
	PrettifyPipedPromQLMode PrettifyMode = 2
)

func Prettify(n Node) string {
	return n.Pretty(0, PrettifyPromQLMode)
}

func PipedPrettify(n Node) string {
	return n.Pretty(0, PrettifyPipedPromQLMode)
}

func (e *AggregateExpr) Pretty(level int, mode PrettifyMode) string {
	switch mode {
	default:
		fallthrough
	case PrettifyPromQLMode:
		s := indent(level)
		if !needsSplit(e) {
			s += e.String()
			return s
		}

		s += e.ShortString()
		s += "(\n"

		if e.Op.IsAggregatorWithParam() {
			s += fmt.Sprintf("%s,\n", e.Param.Pretty(level+1, mode))
		}
		s += fmt.Sprintf("%s\n%s)", e.Expr.Pretty(level+1, mode), indent(level))
		return s
	case PrettifyPipedPromQLMode:
		b := bytes.NewBuffer(nil)

		// Render the source expression first (the subject of the pipe).
		// It will handle its own indentation based on the level.
		b.WriteString(e.Expr.Pretty(level, mode))

		b.WriteString("\n|> ")
		b.WriteString(e.Op.String())
		if e.Op.IsAggregatorWithParam() {
			b.WriteString("(")
			b.WriteString(e.Param.Pretty(0, mode))
			b.WriteString(")")
		}

		switch {
		case e.Without:
			b.WriteString(" without (")
			writeLabels(b, e.Grouping)
			b.WriteString(") ")
		case len(e.Grouping) > 0:
			b.WriteString(" by (")
			writeLabels(b, e.Grouping)
			b.WriteString(") ")
		}
		return b.String()
	}
}

func (e *BinaryExpr) Pretty(level int, mode PrettifyMode) string {
	switch mode {
	default:
		fallthrough
	case PrettifyPromQLMode:
		s := indent(level)
		if !needsSplit(e) {
			s += e.String()
			return s
		}
		returnBool := ""
		if e.ReturnBool {
			returnBool = " bool"
		}

		matching := e.getMatchingStr()
		return fmt.Sprintf("%s\n%s%s%s%s\n%s", e.LHS.Pretty(level+1, mode), indent(level), e.Op, returnBool, matching, e.RHS.Pretty(level+1, mode))
	case PrettifyPipedPromQLMode:
		b := bytes.NewBuffer(nil)

		b.WriteString(indent(level))
		b.WriteString(e.LHS.Pretty(level+1, mode))

		b.WriteString("\n")
		b.WriteString(indent(level))
		b.WriteString(e.RHS.Pretty(level+1, mode))

		b.WriteString("\n")
		b.WriteString(indent(level))
		b.WriteString("| ")
		b.WriteString(e.Op.String())
		if e.ReturnBool {
			b.WriteString(" bool ")
		}
		b.WriteString(e.getMatchingStr())
		return b.String()
	}
}

func (e *DurationExpr) Pretty(_ int, mode PrettifyMode) string {
	var s string
	fmt.Println("e.LHS", e.LHS)
	fmt.Println("e.RHS", e.RHS)
	if e.LHS == nil {
		// This is a unary duration expression.
		s = fmt.Sprintf("%s%s", e.Op, e.RHS.Pretty(0, mode))
	} else {
		s = fmt.Sprintf("%s %s %s", e.LHS.Pretty(0, mode), e.Op, e.RHS.Pretty(0, mode))
	}
	if e.Wrapped {
		s = fmt.Sprintf("(%s)", s)
	}
	return s
}

func (e *Call) Pretty(level int, mode PrettifyMode) string {
	switch mode {
	default:
		fallthrough
	case PrettifyPromQLMode:
		s := indent(level)
		if !needsSplit(e) {
			s += e.String()
			return s
		}
		s += fmt.Sprintf("%s(\n%s\n%s)", e.Func.Name, e.Args.Pretty(level+1, mode), indent(level))
		return s
	case PrettifyPipedPromQLMode:
		b := bytes.NewBuffer(nil)

		if len(e.Args) > 0 {
			b.WriteString(e.Args.Pretty(level+1, mode))
			b.WriteString(" | ")
		}
		b.WriteString(e.Func.Name)
		return b.String()
	}
}

func (e *EvalStmt) Pretty(int, PrettifyMode) string {
	return "EVAL " + e.Expr.String()
}

func (e Expressions) Pretty(level int, mode PrettifyMode) string {
	// Do not prefix the indent since respective nodes will indent itself.
	s := ""
	for i := range e {
		s += fmt.Sprintf("%s,\n", e[i].Pretty(level, mode))
	}
	return s[:len(s)-2]
}

func (e *ParenExpr) Pretty(level int, mode PrettifyMode) string {
	s := indent(level)
	if !needsSplit(e) {
		s += e.String()
		return s
	}
	return fmt.Sprintf("%s(\n%s\n%s)", s, e.Expr.Pretty(level+1, mode), indent(level))
}

func (e *StepInvariantExpr) Pretty(level int, mode PrettifyMode) string {
	return e.Expr.Pretty(level, mode)
}

func (e *MatrixSelector) Pretty(level int, _ PrettifyMode) string {
	return getCommonPrefixIndent(level, e)
}

func (e *SubqueryExpr) Pretty(level int, mode PrettifyMode) string {
	if !needsSplit(e) {
		return e.String()
	}
	return fmt.Sprintf("%s%s", e.Expr.Pretty(level, mode), e.getSubqueryTimeSuffix())
}

func (e *VectorSelector) Pretty(level int, _ PrettifyMode) string {
	return getCommonPrefixIndent(level, e)
}

func (e *NumberLiteral) Pretty(level int, _ PrettifyMode) string {
	return getCommonPrefixIndent(level, e)
}

func (e *StringLiteral) Pretty(level int, _ PrettifyMode) string {
	return getCommonPrefixIndent(level, e)
}

func (e *UnaryExpr) Pretty(level int, mode PrettifyMode) string {
	child := e.Expr.Pretty(level, mode)
	// Remove the indent prefix from child since we attach the prefix indent before Op.
	child = strings.TrimSpace(child)
	return fmt.Sprintf("%s%s%s", indent(level), e.Op, child)
}

func getCommonPrefixIndent(level int, current Node) string {
	return fmt.Sprintf("%s%s", indent(level), current.String())
}

// needsSplit normalizes the node and then checks if the node needs any split.
// This is necessary to remove any trailing whitespaces.
func needsSplit(n Node) bool {
	if n == nil {
		return false
	}
	return len(n.String()) > maxCharactersPerLine
}

const indentString = "  "

// indent adds the indentString n number of times.
func indent(n int) string {
	return strings.Repeat(indentString, n)
}
