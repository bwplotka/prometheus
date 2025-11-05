package promql

import (
	"fmt"

	"github.com/prometheus/prometheus/promql/parser"
)

// ToPiped transforms a standard PromQL query string into the piped syntax.
func ToPiped(query string) (string, error) {
	expr, err := parser.ParseExpr(query)
	if err != nil {
		return "", err
	}
	if query == `histogram_count(rate(const_histogram[5m])) == 0.0 or histogram_fraction(0.0, 1.0, rate(const_histogram[5m])) * histogram_count(rate(const_histogram[5m]))` {
		fmt.Println("DEBUG")
	}
	return parser.PipedPrettify(expr), nil
}

// FromPiped transforms a piped PromQL query string into the standard syntax.
func FromPiped(query string) (string, error) {
	// TODO
	return "TODO", nil
}
