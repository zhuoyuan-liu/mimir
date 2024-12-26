// SPDX-License-Identifier: AGPL-3.0-only
// Provenance-includes-location: https://github.com/thanos-io/thanos/blob/main/pkg/store/storepb/custom.go
// Provenance-includes-license: Apache-2.0
// Provenance-includes-copyright: The Thanos Authors.

package storepb

import (
	fmt "fmt"
	io "io"

	"github.com/gogo/protobuf/types"
	"github.com/pkg/errors"
	"github.com/prometheus/prometheus/model/labels"

	"github.com/grafana/mimir/pkg/mimirpb"
	"github.com/grafana/mimir/pkg/storage/chunk"
)

func NewSeriesResponse(series CustomSeries) *SeriesResponse {
	return &SeriesResponse{
		Result: &SeriesResponse_Series{
			Series: &series,
		},
	}
}

func NewHintsSeriesResponse(hints *types.Any) *SeriesResponse {
	return &SeriesResponse{
		Result: &SeriesResponse_Hints{
			Hints: hints,
		},
	}
}

func NewStatsResponse(indexBytesFetched int) *SeriesResponse {
	return &SeriesResponse{
		Result: &SeriesResponse_Stats{
			Stats: &Stats{FetchedIndexBytes: uint64(indexBytesFetched)},
		},
	}
}

func NewStreamingSeriesResponse(series *StreamingSeriesBatch) *SeriesResponse {
	return &SeriesResponse{
		Result: &SeriesResponse_StreamingSeries{
			StreamingSeries: series,
		},
	}
}

func NewStreamingChunksResponse(series *StreamingChunksBatch) *SeriesResponse {
	return &SeriesResponse{
		Result: &SeriesResponse_StreamingChunks{
			StreamingChunks: series,
		},
	}
}

func NewStreamingChunksEstimate(estimatedChunks uint64) *SeriesResponse {
	return &SeriesResponse{
		Result: &SeriesResponse_StreamingChunksEstimate{
			StreamingChunksEstimate: &StreamingChunksEstimate{
				EstimatedChunkCount: estimatedChunks,
			},
		},
	}
}

type emptySeriesSet struct{}

func (emptySeriesSet) Next() bool                       { return false }
func (emptySeriesSet) At() (labels.Labels, []AggrChunk) { return labels.EmptyLabels(), nil }
func (emptySeriesSet) Err() error                       { return nil }

// EmptySeriesSet returns a new series set that contains no series.
func EmptySeriesSet() SeriesSet {
	return emptySeriesSet{}
}

// SeriesSet is a set of series and their corresponding chunks.
// The set is sorted by the label sets. Chunks may be overlapping or expected of order.
type SeriesSet interface {
	Next() bool
	At() (labels.Labels, []AggrChunk)
	Err() error
}

// PromMatchersToMatchers returns proto matchers from Prometheus matchers.
// NOTE: It allocates memory.
func PromMatchersToMatchers(ms ...*labels.Matcher) ([]LabelMatcher, error) {
	res := make([]LabelMatcher, 0, len(ms))
	for _, m := range ms {
		var t LabelMatcher_Type

		switch m.Type {
		case labels.MatchEqual:
			t = LabelMatcher_EQ
		case labels.MatchNotEqual:
			t = LabelMatcher_NEQ
		case labels.MatchRegexp:
			t = LabelMatcher_RE
		case labels.MatchNotRegexp:
			t = LabelMatcher_NRE
		default:
			return nil, errors.Errorf("unrecognized matcher type %d", m.Type)
		}
		res = append(res, LabelMatcher{Type: t, Name: m.Name, Value: m.Value})
	}
	return res, nil
}

// MatchersToPromMatchers returns Prometheus matchers from proto matchers.
// NOTE: It allocates memory.
func MatchersToPromMatchers(ms ...LabelMatcher) ([]*labels.Matcher, error) {
	res := make([]*labels.Matcher, 0, len(ms))
	for _, m := range ms {
		var t labels.MatchType

		switch m.Type {
		case LabelMatcher_EQ:
			t = labels.MatchEqual
		case LabelMatcher_NEQ:
			t = labels.MatchNotEqual
		case LabelMatcher_RE:
			t = labels.MatchRegexp
		case LabelMatcher_NRE:
			t = labels.MatchNotRegexp
		default:
			return nil, errors.Errorf("unrecognized label matcher type %d", m.Type)
		}
		m, err := labels.NewMatcher(t, m.Name, m.Value)
		if err != nil {
			return nil, err
		}
		res = append(res, m)
	}
	return res, nil
}

func (c AggrChunk) GetChunkEncoding() (chunk.Encoding, bool) {
	switch c.Raw.Type {
	case Chunk_XOR:
		return chunk.PrometheusXorChunk, true
	case Chunk_Histogram:
		return chunk.PrometheusHistogramChunk, true
	case Chunk_FloatHistogram:
		return chunk.PrometheusFloatHistogramChunk, true
	default:
		return 0, false
	}
}

type CustomSeries struct {
	*Series
}

func (*CustomSeries) isSeriesResponse_Result() {}

func (m *CustomSeries) Unmarshal(data []byte) error {
	// TODO: Get m.Series from pool.
	m.Series = &Series{}
	l := len(data)
	index := 0
	for index < l {
		preIndex := index
		var wire uint64
		for shift := uint(0); ; shift += 7 {
			if shift >= 64 {
				return ErrIntOverflowTypes
			}
			if index >= l {
				return io.ErrUnexpectedEOF
			}
			b := data[index]
			index++
			wire |= uint64(b&0x7F) << shift
			if b < 0x80 {
				break
			}
		}

		fieldNum := int32(wire >> 3)
		wireType := int(wire & 0x7)
		if wireType == 4 {
			return fmt.Errorf("proto: Series: wiretype end group for non-group")
		}
		if fieldNum <= 0 {
			return fmt.Errorf("proto: Series: illegal tag %d (wire type %d)", fieldNum, wire)
		}

		switch fieldNum {
		case 1:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field Labels", wireType)
			}

			var msglen int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowTypes
				}
				if index >= l {
					return io.ErrUnexpectedEOF
				}

				b := data[index]
				index++
				msglen |= int(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			if msglen < 0 {
				return ErrInvalidLengthTypes
			}

			postIndex := index + msglen
			if postIndex < 0 {
				return ErrInvalidLengthTypes
			}
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}

			var la mimirpb.LabelAdapter
			if err := unmarshalLabelAdapter(&la, data[index:postIndex]); err != nil {
				return err
			}
			m.Labels = append(m.Labels, la)
			index = postIndex
		case 2:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field Chunks", wireType)
			}
			var msglen int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowTypes
				}
				if index >= l {
					return io.ErrUnexpectedEOF
				}
				b := data[index]
				index++
				msglen |= int(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			if msglen < 0 {
				return ErrInvalidLengthTypes
			}
			postIndex := index + msglen
			if postIndex < 0 {
				return ErrInvalidLengthTypes
			}
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			var chk AggrChunk
			if err := unmarshalAggrChunk(&chk, data[index:postIndex]); err != nil {
				return err
			}
			m.Chunks = append(m.Chunks, chk)
			index = postIndex
		default:
			index = preIndex
			skippy, err := skipTypes(data[index:])
			if err != nil {
				return err
			}
			if skippy < 0 {
				return ErrInvalidLengthTypes
			}
			if (index + skippy) < 0 {
				return ErrInvalidLengthTypes
			}
			if (index + skippy) > l {
				return io.ErrUnexpectedEOF
			}
			index += skippy
		}
	}

	if index > l {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func (m *SeriesResponse) GetSeries() *CustomSeries {
	if x, ok := m.GetResult().(*SeriesResponse_Series); ok {
		return x.Series
	}
	return nil
}

func unmarshalLabelAdapter(la *mimirpb.LabelAdapter, data []byte) error {
	l := len(data)
	index := 0
	for index < l {
		preIndex := index
		var wire uint64
		for shift := uint(0); ; shift += 7 {
			if shift >= 64 {
				return mimirpb.ErrIntOverflowMimir
			}
			if index >= l {
				return io.ErrUnexpectedEOF
			}
			b := data[index]
			index++
			wire |= uint64(b&0x7F) << shift
			if b < 0x80 {
				break
			}
		}
		fieldNum := int32(wire >> 3)
		wireType := int(wire & 0x7)
		if wireType == 4 {
			return fmt.Errorf("proto: LabelPair: wiretype end group for non-group")
		}
		if fieldNum <= 0 {
			return fmt.Errorf("proto: LabelPair: illegal tag %d (wire type %d)", fieldNum, wire)
		}
		switch fieldNum {
		case 1:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field Name", wireType)
			}
			var byteLen int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return mimirpb.ErrIntOverflowMimir
				}
				if index >= l {
					return io.ErrUnexpectedEOF
				}
				b := data[index]
				index++
				byteLen |= int(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			if byteLen < 0 {
				return mimirpb.ErrInvalidLengthMimir
			}
			postIndex := index + byteLen
			if postIndex < 0 {
				return mimirpb.ErrInvalidLengthMimir
			}
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			// TODO: Get byte slice from pool, copy the data to it, and take a yoloString.
			la.Name = string(data[index:postIndex])
			index = postIndex
		case 2:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field Value", wireType)
			}
			var byteLen int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return mimirpb.ErrIntOverflowMimir
				}
				if index >= l {
					return io.ErrUnexpectedEOF
				}
				b := data[index]
				index++
				byteLen |= int(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			if byteLen < 0 {
				return mimirpb.ErrInvalidLengthMimir
			}
			postIndex := index + byteLen
			if postIndex < 0 {
				return mimirpb.ErrInvalidLengthMimir
			}
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			// TODO: Get byte slice from pool, copy the data to it, and take a yoloString.
			la.Value = string(data[index:postIndex])
			index = postIndex
		default:
			index = preIndex
			skippy, err := skipMimir(data[index:])
			if err != nil {
				return err
			}
			if skippy < 0 {
				return mimirpb.ErrInvalidLengthMimir
			}
			if (index + skippy) < 0 {
				return mimirpb.ErrInvalidLengthMimir
			}
			if (index + skippy) > l {
				return io.ErrUnexpectedEOF
			}
			index += skippy
		}
	}
	if index > l {
		return io.ErrUnexpectedEOF
	}

	return nil
}

func skipMimir(data []byte) (n int, err error) {
	l := len(data)
	index := 0
	for index < l {
		var wire uint64
		for shift := uint(0); ; shift += 7 {
			if shift >= 64 {
				return 0, mimirpb.ErrIntOverflowMimir
			}
			if index >= l {
				return 0, io.ErrUnexpectedEOF
			}
			b := data[index]
			index++
			wire |= (uint64(b) & 0x7F) << shift
			if b < 0x80 {
				break
			}
		}
		wireType := int(wire & 0x7)
		switch wireType {
		case 0:
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return 0, mimirpb.ErrIntOverflowMimir
				}
				if index >= l {
					return 0, io.ErrUnexpectedEOF
				}
				index++
				if data[index-1] < 0x80 {
					break
				}
			}
			return index, nil
		case 1:
			index += 8
			return index, nil
		case 2:
			var length int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return 0, mimirpb.ErrIntOverflowMimir
				}
				if index >= l {
					return 0, io.ErrUnexpectedEOF
				}
				b := data[index]
				index++
				length |= (int(b) & 0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			if length < 0 {
				return 0, mimirpb.ErrInvalidLengthMimir
			}
			index += length
			if index < 0 {
				return 0, mimirpb.ErrInvalidLengthMimir
			}
			return index, nil
		case 3:
			for {
				var innerWire uint64
				var start int = index
				for shift := uint(0); ; shift += 7 {
					if shift >= 64 {
						return 0, mimirpb.ErrIntOverflowMimir
					}
					if index >= l {
						return 0, io.ErrUnexpectedEOF
					}
					b := data[index]
					index++
					innerWire |= (uint64(b) & 0x7F) << shift
					if b < 0x80 {
						break
					}
				}
				innerWireType := int(innerWire & 0x7)
				if innerWireType == 4 {
					break
				}
				next, err := skipMimir(data[start:])
				if err != nil {
					return 0, err
				}
				index = start + next
				if index < 0 {
					return 0, mimirpb.ErrInvalidLengthMimir
				}
			}
			return index, nil
		case 4:
			return index, nil
		case 5:
			index += 4
			return index, nil
		default:
			return 0, fmt.Errorf("proto: illegal wireType %d", wireType)
		}
	}
	panic("unreachable")
}

func unmarshalAggrChunk(chk *AggrChunk, data []byte) error {
	// TODO: Customize, to use pooling.
	return chk.Unmarshal(data)
}
