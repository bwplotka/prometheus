package writev2

import (
	"fmt"
	"testing"

	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"

	"github.com/prometheus/prometheus/prompb"
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

//type rwTimeSeries interface {
//	prompb.TimeSeries | TimeSeries
//}
//
//func sendSamples[T rwTimeSeries](series []T) error {
//	for _, s := range series {
//		for _, sample := range s.g {
//			append(sample.GetValue(), sample.GetTimestamp())
//		}
//	}
//	return nil
//}

type rwSample interface {
	GetValue2() float64
	GetTimestamp2() int64
}

type rwTimeSeries1[S rwSample, E any] interface {
	GetSamples2() []S
	GetExemplar() []E
	GetMetadata()
	CreatedTimestamp()
}

func TestName(t *testing.T) {
	v1 := []prompb.TimeSeries{{Samples: []prompb.Sample{{Value: 1, Timestamp: 2}}}}
	sendSamples1[prompb.Sample, prompb.TimeSeries](v1)

	v2 := []TimeSeries{{Samples: []Sample{{Value: 1, Timestamp: 22}}}}
	sendSamples1[Sample, TimeSeries](v2)
}
func sendSamples1[S rwSample, T rwTimeSeries1[S]](series []T) error {
	for _, s := range series {
		for _, sample := range s.GetSamples2() {
			append2(sample.GetValue2(), sample.GetTimestamp2())
		}
	}
	return nil
}

func append2(v float64, t int64) { fmt.Println(v, t) }

func sendSamples2(series []prompb.TimeSeries) error {
	for _, s := range series {
		for _, sample := range s.GetSamples() {
			append2(sample.GetValue(), sample.GetTimestamp())
		}
	}
	return nil
}
