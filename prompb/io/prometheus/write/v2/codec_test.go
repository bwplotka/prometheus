package writev2

import (
	"testing"

	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"
)

func TestMetricTypeToMetricTypeProto(t *testing.T) {
	for _, tt := range []struct {
		desc     string
		input    model.MetricType
		expected Metadata_MetricType
	}{
		{
			desc:     "with a single-word metric",
			input:    model.MetricTypeCounter,
			expected: Metadata_METRIC_TYPE_COUNTER,
		},
		{
			desc:     "with a two-word metric",
			input:    model.MetricTypeStateset,
			expected: Metadata_METRIC_TYPE_STATESET,
		},
		{
			desc:     "with an unknown metric",
			input:    "not-known",
			expected: Metadata_METRIC_TYPE_UNSPECIFIED,
		},
	} {
		t.Run(tt.desc, func(t *testing.T) {
			require.Equal(t, tt.expected, FromMetadataType(tt.input))
		})
	}
}
