package promql_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prometheus/prometheus/promql"
)

func TestToPiped(t *testing.T) {
	for _, tc := range pipedCases {
		t.Run(tc.query, func(t *testing.T) {
			got, err := promql.ToPiped(tc.query)
			require.NoError(t, err)
			require.Equal(t, got, tc.piped)
		})
	}
}

func TestFromPiped(t *testing.T) {
	for _, tc := range pipedCases {
		t.Run(tc.piped, func(t *testing.T) {
			got, err := promql.FromPiped(tc.piped)
			require.NoError(t, err)
			require.Equal(t, got, tc.query)
		})
	}
}
