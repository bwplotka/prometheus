package prompb

import (
	"testing"

	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"

	"github.com/prometheus/prometheus/model/histogram"
)

func TestMetricTypeToMetricTypeProto(t *testing.T) {
	for _, tt := range []struct {
		desc     string
		input    model.MetricType
		expected MetricMetadata_MetricType
	}{
		{
			desc:     "with a single-word metric",
			input:    model.MetricTypeCounter,
			expected: MetricMetadata_COUNTER,
		},
		{
			desc:     "with a two-word metric",
			input:    model.MetricTypeStateset,
			expected: MetricMetadata_STATESET,
		},
		{
			desc:     "with an unknown metric",
			input:    "not-known",
			expected: MetricMetadata_UNKNOWN,
		},
	} {
		t.Run(tt.desc, func(t *testing.T) {
			require.Equal(t, tt.expected, FromMetadataType(tt.input))
		})
	}
}

func TestToHistogram_Empty(t *testing.T) {
	require.NotNilf(t, Histogram{}.ToIntHistogram(), "")
	require.NotNilf(t, Histogram{}.ToFloatHistogram(), "")
}

var (
	testIntHistogram = histogram.Histogram{
		Schema:          2,
		ZeroThreshold:   1e-128,
		ZeroCount:       0,
		Count:           0,
		Sum:             20,
		PositiveSpans:   []histogram.Span{{Offset: 0, Length: 1}},
		PositiveBuckets: []int64{1},
		NegativeSpans:   []histogram.Span{{Offset: 0, Length: 1}},
		NegativeBuckets: []int64{-1},
	}
	testFloatHistogram = histogram.Histogram{
		Schema:          2,
		ZeroThreshold:   1e-128,
		ZeroCount:       0,
		Count:           0,
		Sum:             20,
		PositiveSpans:   []histogram.Span{{Offset: 0, Length: 1}},
		PositiveBuckets: []int64{1},
		NegativeSpans:   []histogram.Span{{Offset: 0, Length: 1}},
		NegativeBuckets: []int64{-1},
	}
)

func TestFromIntToFloatOrIntHistogram(t *testing.T) {

	h := FromIntHistogram(123, &testIntHistogram)
	require.Equal(t, &testIntHistogram, h.ToIntHistogram())
	require.Equal(t, &testIntHistogram, h.ToFloatHistogram())
}

func TestFromFloatToFloatHistogram(t *testing.T) {
	//testIntHistogram := histogram.Histogram{
	//	Schema:          2,
	//	ZeroThreshold:   1e-128,
	//	ZeroCount:       0,
	//	Count:           0,
	//	Sum:             20,
	//	PositiveSpans:   []histogram.Span{{Offset: 0, Length: 1}},
	//	PositiveBuckets: []int64{1},
	//	NegativeSpans:   []histogram.Span{{Offset: 0, Length: 1}},
	//	NegativeBuckets: []int64{-1},
	//}
	//h := FromIntHistogram(123, &testIntHistogram)
	//require.Equal(t, &testIntHistogram, h.ToIntHistogram())
	//require.Equal(t, &testIntHistogram, h.ToFloatHistogram())
}
