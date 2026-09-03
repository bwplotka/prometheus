// Copyright The Prometheus Authors
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

package remote

import (
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/prometheus/common/model"
	"github.com/prometheus/common/version"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"

	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/util/compression"
)

type resourceGroupKey struct {
	job      string
	instance string
}

func populateDataPointAttributes(attr pcommon.Map, lbls labels.Labels) {
	lbls.Range(func(l labels.Label) {
		if l.Name == labels.MetricName || l.Name == "job" || l.Name == "instance" {
			return
		}
		attr.PutStr(l.Name, l.Value)
	})
}

func populateExponentialHistogramBuckets(target pmetric.ExponentialHistogramDataPointBuckets, spans []histogram.Span, buckets []int64) {
	if len(spans) == 0 || len(buckets) == 0 {
		return
	}
	target.SetOffset(spans[0].Offset)
	bc := target.BucketCounts()
	var currentCount int64
	var bIdx int
	for i, s := range spans {
		if i > 0 {
			for g := int32(0); g < s.Offset; g++ {
				bc.Append(0)
			}
		}
		for l := uint32(0); l < s.Length && bIdx < len(buckets); l++ {
			currentCount += buckets[bIdx]
			bIdx++
			if currentCount < 0 {
				bc.Append(0)
			} else {
				bc.Append(uint64(currentCount))
			}
		}
	}
}

func populateFloatExponentialHistogramBuckets(target pmetric.ExponentialHistogramDataPointBuckets, spans []histogram.Span, buckets []float64) {
	if len(spans) == 0 || len(buckets) == 0 {
		return
	}
	target.SetOffset(spans[0].Offset)
	bc := target.BucketCounts()
	var bIdx int
	for i, s := range spans {
		if i > 0 {
			for g := int32(0); g < s.Offset; g++ {
				bc.Append(0)
			}
		}
		for l := uint32(0); l < s.Length && bIdx < len(buckets); l++ {
			c := buckets[bIdx]
			bIdx++
			if c < 0 {
				bc.Append(0)
			} else {
				bc.Append(uint64(math.Round(c)))
			}
		}
	}
}

func buildOTLPWriteRequest(
	logger *slog.Logger,
	batch []timeSeries,
	sendExemplars bool,
	sendNativeHistograms bool,
	pBuf *[]byte,
	filter func(timeSeries) bool,
	buf compression.EncodeBuffer,
	compr compression.Type,
) (compressed []byte, highest, lowest int64, sampleCount, exemplarCount, histogramCount, metadataCount int, err error) {
	highest = -1
	lowest = math.MaxInt64

	resourceGroups := make(map[resourceGroupKey][]timeSeries)
	var resourceOrder []resourceGroupKey

	for _, d := range batch {
		if filter != nil && !filter(d) {
			continue
		}
		if d.timestamp > highest {
			highest = d.timestamp
		}
		if d.timestamp < lowest {
			lowest = d.timestamp
		}

		key := resourceGroupKey{
			job:      d.seriesLabels.Get("job"),
			instance: d.seriesLabels.Get("instance"),
		}
		if _, ok := resourceGroups[key]; !ok {
			resourceOrder = append(resourceOrder, key)
		}
		resourceGroups[key] = append(resourceGroups[key], d)
	}

	if highest == -1 {
		highest = 0
		lowest = 0
	}

	md := pmetric.NewMetrics()

	for _, key := range resourceOrder {
		rm := md.ResourceMetrics().AppendEmpty()
		if key.job != "" {
			rm.Resource().Attributes().PutStr("service.name", key.job)
			rm.Resource().Attributes().PutStr("job", key.job)
		}
		if key.instance != "" {
			rm.Resource().Attributes().PutStr("service.instance.id", key.instance)
			rm.Resource().Attributes().PutStr("instance", key.instance)
		}
		sm := rm.ScopeMetrics().AppendEmpty()
		sm.Scope().SetName("prometheus")
		sm.Scope().SetVersion(version.Version)

		for _, d := range resourceGroups[key] {
			metricName := d.seriesLabels.Get(labels.MetricName)
			if metricName == "" {
				continue
			}

			switch d.sType {
			case tSample:
				sampleCount++
				isCounter := false
				if d.metadata != nil && d.metadata.Type == model.MetricTypeCounter {
					isCounter = true
				} else if strings.HasSuffix(metricName, "_total") || strings.HasSuffix(metricName, "_count") {
					isCounter = true
				}

				m := sm.Metrics().AppendEmpty()
				m.SetName(metricName)
				if d.metadata != nil {
					m.SetDescription(d.metadata.Help)
					m.SetUnit(d.metadata.Unit)
					metadataCount++
				}

				if isCounter {
					sum := m.SetEmptySum()
					sum.SetIsMonotonic(true)
					sum.SetAggregationTemporality(pmetric.AggregationTemporalityCumulative)
					dp := sum.DataPoints().AppendEmpty()
					dp.SetDoubleValue(d.value)
					dp.SetTimestamp(pcommon.Timestamp(d.timestamp * int64(time.Millisecond)))
					if d.startTimestamp > 0 {
						dp.SetStartTimestamp(pcommon.Timestamp(d.startTimestamp * int64(time.Millisecond)))
					}
					populateDataPointAttributes(dp.Attributes(), d.seriesLabels)
				} else {
					gauge := m.SetEmptyGauge()
					dp := gauge.DataPoints().AppendEmpty()
					dp.SetDoubleValue(d.value)
					dp.SetTimestamp(pcommon.Timestamp(d.timestamp * int64(time.Millisecond)))
					populateDataPointAttributes(dp.Attributes(), d.seriesLabels)
				}

			case tHistogram:
				if !sendNativeHistograms || d.histogram == nil {
					continue
				}
				histogramCount++
				m := sm.Metrics().AppendEmpty()
				m.SetName(metricName)
				if d.metadata != nil {
					m.SetDescription(d.metadata.Help)
					m.SetUnit(d.metadata.Unit)
					metadataCount++
				}
				eh := m.SetEmptyExponentialHistogram()
				eh.SetAggregationTemporality(pmetric.AggregationTemporalityCumulative)
				dp := eh.DataPoints().AppendEmpty()
				dp.SetTimestamp(pcommon.Timestamp(d.timestamp * int64(time.Millisecond)))
				if d.startTimestamp > 0 {
					dp.SetStartTimestamp(pcommon.Timestamp(d.startTimestamp * int64(time.Millisecond)))
				}
				dp.SetCount(d.histogram.Count)
				dp.SetSum(d.histogram.Sum)
				dp.SetScale(d.histogram.Schema)
				dp.SetZeroCount(d.histogram.ZeroCount)
				dp.SetZeroThreshold(d.histogram.ZeroThreshold)
				populateExponentialHistogramBuckets(dp.Positive(), d.histogram.PositiveSpans, d.histogram.PositiveBuckets)
				populateExponentialHistogramBuckets(dp.Negative(), d.histogram.NegativeSpans, d.histogram.NegativeBuckets)
				populateDataPointAttributes(dp.Attributes(), d.seriesLabels)

			case tFloatHistogram:
				if !sendNativeHistograms || d.floatHistogram == nil {
					continue
				}
				histogramCount++
				m := sm.Metrics().AppendEmpty()
				m.SetName(metricName)
				if d.metadata != nil {
					m.SetDescription(d.metadata.Help)
					m.SetUnit(d.metadata.Unit)
					metadataCount++
				}
				eh := m.SetEmptyExponentialHistogram()
				eh.SetAggregationTemporality(pmetric.AggregationTemporalityCumulative)
				dp := eh.DataPoints().AppendEmpty()
				dp.SetTimestamp(pcommon.Timestamp(d.timestamp * int64(time.Millisecond)))
				if d.startTimestamp > 0 {
					dp.SetStartTimestamp(pcommon.Timestamp(d.startTimestamp * int64(time.Millisecond)))
				}
				dp.SetCount(uint64(d.floatHistogram.Count))
				dp.SetSum(d.floatHistogram.Sum)
				dp.SetScale(d.floatHistogram.Schema)
				dp.SetZeroCount(uint64(d.floatHistogram.ZeroCount))
				dp.SetZeroThreshold(d.floatHistogram.ZeroThreshold)
				populateFloatExponentialHistogramBuckets(dp.Positive(), d.floatHistogram.PositiveSpans, d.floatHistogram.PositiveBuckets)
				populateFloatExponentialHistogramBuckets(dp.Negative(), d.floatHistogram.NegativeSpans, d.floatHistogram.NegativeBuckets)
				populateDataPointAttributes(dp.Attributes(), d.seriesLabels)

			case tExemplar:
				if !sendExemplars {
					continue
				}
				exemplarCount++
				m := sm.Metrics().AppendEmpty()
				m.SetName(metricName)
				gauge := m.SetEmptyGauge()
				dp := gauge.DataPoints().AppendEmpty()
				dp.SetDoubleValue(d.value)
				dp.SetTimestamp(pcommon.Timestamp(d.timestamp * int64(time.Millisecond)))
				populateDataPointAttributes(dp.Attributes(), d.seriesLabels)
				ex := dp.Exemplars().AppendEmpty()
				ex.SetDoubleValue(d.value)
				ex.SetTimestamp(pcommon.Timestamp(d.timestamp * int64(time.Millisecond)))
				d.exemplarLabels.Range(func(l labels.Label) {
					ex.FilteredAttributes().PutStr(l.Name, l.Value)
				})

			case tMetadata:
				metadataCount++
			}
		}
	}

	req := pmetricotlp.NewExportRequestFromMetrics(md)
	protoBytes, err := req.MarshalProto()
	if err != nil {
		return nil, 0, 0, 0, 0, 0, 0, fmt.Errorf("marshal OTLP export request: %w", err)
	}

	if pBuf != nil {
		*pBuf = append((*pBuf)[:0], protoBytes...)
	}

	compressed, err = compression.Encode(compr, protoBytes, buf)
	if err != nil {
		return nil, 0, 0, 0, 0, 0, 0, fmt.Errorf("compress OTLP export request: %w", err)
	}

	return compressed, highest, lowest, sampleCount, exemplarCount, histogramCount, metadataCount, nil
}
