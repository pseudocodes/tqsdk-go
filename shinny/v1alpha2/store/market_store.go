package store

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
)

type ChartState struct {
	ChartID   string
	InsList   string
	Duration  int64
	ViewWidth int
	LeftID    int64
	RightID   int64
	Ready     bool
	MoreData  bool
	State     map[string]any
}

type tickSeries struct {
	IDs    []int64
	Items  map[int64]api.Tick
	LastID int64
}

type klineSeries struct {
	IDs     []int64
	Items   map[int64]api.Kline
	LastID  int64
	Binding map[string]map[int64]int64
}

type MarketStore struct {
	mu sync.RWMutex

	version   uint64
	updatedAt time.Time

	data map[string]any

	quotes map[string]api.Quote
	ticks  map[string]*tickSeries
	klines map[string]map[int64]*klineSeries // symbol -> duration -> series
	charts map[string]ChartState

	querySymbols map[string]map[string]any

	insList       string
	mdhisMoreData bool
}

func NewMarketStore() *MarketStore {
	return &MarketStore{
		data:         map[string]any{},
		quotes:       map[string]api.Quote{},
		ticks:        map[string]*tickSeries{},
		klines:       map[string]map[int64]*klineSeries{},
		charts:       map[string]ChartState{},
		querySymbols: map[string]map[string]any{},
	}
}

func (s *MarketStore) Version() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.version
}

func (s *MarketStore) UpdatedAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.updatedAt
}

func (s *MarketStore) InsList() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.insList
}

func (s *MarketStore) MDHisMoreData() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mdhisMoreData
}

func (s *MarketStore) HasTickData(symbol string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	series, ok := s.ticks[symbol]
	if !ok {
		return false
	}
	return series.LastID >= 0 || len(series.IDs) > 0
}

func (s *MarketStore) HasKlineData(symbol string, duration int64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	dm, ok := s.klines[symbol]
	if !ok {
		return false
	}
	series, ok := dm[duration]
	if !ok {
		return false
	}
	return series.LastID >= 0 || len(series.IDs) > 0
}

func (s *MarketStore) Quote(symbol string) (api.Quote, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	q, ok := s.quotes[symbol]
	return q, ok
}

func (s *MarketStore) Chart(chartID string) (ChartState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.charts[chartID]
	if !ok {
		return ChartState{}, false
	}
	st.State = CloneMap(st.State)
	return st, true
}

func (s *MarketStore) QuerySymbol(queryID string) (map[string]any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.querySymbols[queryID]
	if !ok {
		return nil, false
	}
	return CloneMap(v), true
}

func (s *MarketStore) TickByID(symbol string, id int64) (api.Tick, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	series, ok := s.ticks[symbol]
	if !ok {
		return api.Tick{}, false
	}
	v, ok := series.Items[id]
	return v, ok
}

func (s *MarketStore) KlineByID(symbol string, duration int64, id int64) (api.Kline, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	dm, ok := s.klines[symbol]
	if !ok {
		return api.Kline{}, false
	}
	series, ok := dm[duration]
	if !ok {
		return api.Kline{}, false
	}
	v, ok := series.Items[id]
	return v, ok
}

func (s *MarketStore) ResolveBinding(mainSymbol string, duration int64, subSymbol string, mainID int64) (int64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	dm, ok := s.klines[mainSymbol]
	if !ok {
		return 0, false
	}
	series, ok := dm[duration]
	if !ok {
		return 0, false
	}
	subMap, ok := series.Binding[subSymbol]
	if !ok {
		return 0, false
	}
	v, ok := subMap[mainID]
	return v, ok
}

func (s *MarketStore) TickFrame(symbol string, width int) []*api.Tick {
	s.mu.RLock()
	defer s.mu.RUnlock()
	series, ok := s.ticks[symbol]
	if !ok {
		return nil
	}
	return buildTickFrame(series, width)
}

func (s *MarketStore) TickFrameByRange(symbol string, leftID int64, rightID int64, width int) []*api.Tick {
	s.mu.RLock()
	defer s.mu.RUnlock()
	series, ok := s.ticks[symbol]
	if !ok {
		return nil
	}
	return buildTickFrameByRange(series, leftID, rightID, width)
}

func (s *MarketStore) KlineFrame(symbol string, duration int64, width int) []*api.Kline {
	s.mu.RLock()
	defer s.mu.RUnlock()
	dm, ok := s.klines[symbol]
	if !ok {
		return nil
	}
	series, ok := dm[duration]
	if !ok {
		return nil
	}
	return buildKlineFrame(series, width)
}

func (s *MarketStore) KlineFrameByRange(symbol string, duration int64, leftID int64, rightID int64, width int) []*api.Kline {
	s.mu.RLock()
	defer s.mu.RUnlock()
	dm, ok := s.klines[symbol]
	if !ok {
		return nil
	}
	series, ok := dm[duration]
	if !ok {
		return nil
	}
	return buildKlineFrameByRange(series, leftID, rightID, width)
}

func (s *MarketStore) KlineIDs(symbol string, duration int64) []int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	dm, ok := s.klines[symbol]
	if !ok {
		return nil
	}
	series, ok := dm[duration]
	if !ok {
		return nil
	}
	out := make([]int64, len(series.IDs))
	copy(out, series.IDs)
	return out
}

func (s *MarketStore) ApplyDiff(diff map[string]any) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	if DeepMerge(s.data, diff) {
		changed = true
	}

	if v, ok := diff["ins_list"].(string); ok {
		s.insList = v
		changed = true
	}
	if v, ok := asBool(diff["mdhis_more_data"]); ok {
		s.mdhisMoreData = v
		changed = true
	}

	if qd, ok := diff["quotes"].(map[string]any); ok {
		for symbol, patchAny := range qd {
			if patchAny == nil {
				if _, ok := s.quotes[symbol]; ok {
					delete(s.quotes, symbol)
					changed = true
				}
				continue
			}
			patch, ok := patchAny.(map[string]any)
			if !ok {
				continue
			}
			base := EnsureMap(EnsureMap(s.data, "quotes"), symbol)
			DeepMerge(base, patch)
			var q api.Quote
			decodeMap(base, &q)
			q.Symbol = symbol
			s.quotes[symbol] = q
			changed = true
		}
	}

	if td, ok := diff["ticks"].(map[string]any); ok {
		for symbol, oneAny := range td {
			if oneAny == nil {
				if _, ok := s.ticks[symbol]; ok {
					delete(s.ticks, symbol)
					changed = true
				}
				continue
			}
			one, ok := oneAny.(map[string]any)
			if !ok {
				continue
			}
			series := s.ticks[symbol]
			if series == nil {
				series = &tickSeries{Items: map[int64]api.Tick{}, LastID: -1}
				s.ticks[symbol] = series
			}
			if v, ok := asInt64(one["last_id"]); ok {
				series.LastID = v
			}
			if dataAny, exists := one["data"]; exists && dataAny == nil {
				if len(series.Items) > 0 || len(series.IDs) > 0 {
					series.Items = map[int64]api.Tick{}
					series.IDs = nil
					changed = true
				}
			}
			if dataPatch, ok := one["data"].(map[string]any); ok {
				for idStr, itemAny := range dataPatch {
					id, err := strconv.ParseInt(idStr, 10, 64)
					if err != nil {
						continue
					}
					if itemAny == nil {
						if _, ok := series.Items[id]; ok {
							delete(series.Items, id)
							series.IDs = removeID(series.IDs, id)
							changed = true
						}
						continue
					}
					item, ok := itemAny.(map[string]any)
					if !ok {
						continue
					}
					raw := EnsureMap(EnsureMap(EnsureMap(s.data, "ticks"), symbol), "data")
					base := EnsureMap(raw, idStr)
					DeepMerge(base, item)
					var tick api.Tick
					decodeMap(base, &tick)
					tick.ID = id
					series.Items[id] = tick
					if !containsID(series.IDs, id) {
						series.IDs = insertSortedID(series.IDs, id)
					}
					changed = true
				}
			}
		}
	}

	if kd, ok := diff["klines"].(map[string]any); ok {
		for symbol, byDurAny := range kd {
			if byDurAny == nil {
				if _, ok := s.klines[symbol]; ok {
					delete(s.klines, symbol)
					changed = true
				}
				continue
			}
			byDur, ok := byDurAny.(map[string]any)
			if !ok {
				continue
			}
			dm := s.klines[symbol]
			if dm == nil {
				dm = map[int64]*klineSeries{}
				s.klines[symbol] = dm
			}
			for durStr, oneAny := range byDur {
				dur, err := strconv.ParseInt(durStr, 10, 64)
				if err != nil {
					continue
				}
				if oneAny == nil {
					if _, ok := dm[dur]; ok {
						delete(dm, dur)
						changed = true
					}
					continue
				}
				one, ok := oneAny.(map[string]any)
				if !ok {
					continue
				}
				series := dm[dur]
				if series == nil {
					series = &klineSeries{Items: map[int64]api.Kline{}, LastID: -1, Binding: map[string]map[int64]int64{}}
					dm[dur] = series
				}
				if v, ok := asInt64(one["last_id"]); ok {
					series.LastID = v
				}
				if dataAny, exists := one["data"]; exists && dataAny == nil {
					if len(series.Items) > 0 || len(series.IDs) > 0 {
						series.Items = map[int64]api.Kline{}
						series.IDs = nil
						changed = true
					}
				}
				if dataPatch, ok := one["data"].(map[string]any); ok {
					for idStr, itemAny := range dataPatch {
						id, err := strconv.ParseInt(idStr, 10, 64)
						if err != nil {
							continue
						}
						if itemAny == nil {
							if _, ok := series.Items[id]; ok {
								delete(series.Items, id)
								series.IDs = removeID(series.IDs, id)
								changed = true
							}
							continue
						}
						item, ok := itemAny.(map[string]any)
						if !ok {
							continue
						}
						raw := EnsureMap(EnsureMap(EnsureMap(EnsureMap(s.data, "klines"), symbol), durStr), "data")
						base := EnsureMap(raw, idStr)
						DeepMerge(base, item)
						var k api.Kline
						decodeMap(base, &k)
						k.ID = id
						series.Items[id] = k
						if !containsID(series.IDs, id) {
							series.IDs = insertSortedID(series.IDs, id)
						}
						changed = true
					}
				}
				if bindAny, exists := one["binding"]; exists && bindAny == nil {
					if len(series.Binding) > 0 {
						series.Binding = map[string]map[int64]int64{}
						changed = true
					}
				}
				if bindPatch, ok := one["binding"].(map[string]any); ok {
					for subSymbol, subAny := range bindPatch {
						if subAny == nil {
							if _, ok := series.Binding[subSymbol]; ok {
								delete(series.Binding, subSymbol)
								changed = true
							}
							continue
						}
						subMap, ok := subAny.(map[string]any)
						if !ok {
							continue
						}
						bm := series.Binding[subSymbol]
						if bm == nil {
							bm = map[int64]int64{}
							series.Binding[subSymbol] = bm
						}
						for midStr, sidAny := range subMap {
							mid, err := strconv.ParseInt(midStr, 10, 64)
							if err != nil {
								continue
							}
							if sidAny == nil {
								if _, ok := bm[mid]; ok {
									delete(bm, mid)
									changed = true
								}
								continue
							}
							sid, ok := asInt64(sidAny)
							if !ok {
								continue
							}
							bm[mid] = sid
							changed = true
						}
						if len(bm) == 0 {
							delete(series.Binding, subSymbol)
						}
					}
				}
			}
			if len(dm) == 0 {
				delete(s.klines, symbol)
			}
		}
	}

	if cd, ok := diff["charts"].(map[string]any); ok {
		for chartID, patchAny := range cd {
			if patchAny == nil {
				if _, ok := s.charts[chartID]; ok {
					delete(s.charts, chartID)
					changed = true
				}
				continue
			}
			patch, ok := patchAny.(map[string]any)
			if !ok {
				continue
			}
			base := EnsureMap(EnsureMap(s.data, "charts"), chartID)
			DeepMerge(base, patch)
			st := s.charts[chartID]
			st.ChartID = chartID
			st.State = map[string]any{}
			if ins, ok := asString(base["ins_list"]); ok {
				st.InsList = ins
			}
			if dur, ok := asInt64(base["duration"]); ok {
				st.Duration = dur
			}
			if vw, ok := asInt(base["view_width"]); ok {
				st.ViewWidth = vw
			}
			if lid, ok := asInt64(base["left_id"]); ok {
				st.LeftID = lid
			}
			if rid, ok := asInt64(base["right_id"]); ok {
				st.RightID = rid
			}
			if ready, ok := asBool(base["ready"]); ok {
				st.Ready = ready
			}
			if md, ok := asBool(base["more_data"]); ok {
				st.MoreData = md
			}
			if stateMap, ok := base["state"].(map[string]any); ok {
				st.State = CloneMap(stateMap)
			}
			s.charts[chartID] = st
			changed = true
		}
	}

	if sd, ok := diff["symbols"].(map[string]any); ok {
		for queryID, valueAny := range sd {
			if valueAny == nil {
				if _, ok := s.querySymbols[queryID]; ok {
					delete(s.querySymbols, queryID)
					changed = true
				}
				continue
			}
			valueMap, ok := valueAny.(map[string]any)
			if !ok {
				continue
			}
			base := s.querySymbols[queryID]
			if base == nil {
				base = map[string]any{}
				s.querySymbols[queryID] = base
			}
			if DeepMerge(base, valueMap) {
				changed = true
			}
		}
	}

	if changed {
		s.version++
		s.updatedAt = time.Now()
	}
	return changed
}

func buildTickFrame(series *tickSeries, width int) []*api.Tick {
	if width <= 0 {
		return nil
	}
	frame := make([]*api.Tick, width)
	if len(series.IDs) == 0 {
		return frame
	}
	start := len(series.IDs) - width
	if start < 0 {
		start = 0
	}
	ids := series.IDs[start:]
	offset := width - len(ids)
	for i, id := range ids {
		v := series.Items[id]
		v2 := v
		frame[offset+i] = &v2
	}
	return frame
}

func buildTickFrameByRange(series *tickSeries, leftID int64, rightID int64, width int) []*api.Tick {
	if width <= 0 {
		return nil
	}
	frame := make([]*api.Tick, width)
	if rightID < leftID || len(series.IDs) == 0 {
		return frame
	}
	startID := rightID - int64(width) + 1
	if startID < leftID {
		startID = leftID
	}
	for id := startID; id <= rightID; id++ {
		v, ok := series.Items[id]
		if !ok {
			continue
		}
		idx := int(id - (rightID - int64(width) + 1))
		if idx < 0 || idx >= width {
			continue
		}
		v2 := v
		frame[idx] = &v2
	}
	return frame
}

func buildKlineFrame(series *klineSeries, width int) []*api.Kline {
	if width <= 0 {
		return nil
	}
	frame := make([]*api.Kline, width)
	if len(series.IDs) == 0 {
		return frame
	}
	start := len(series.IDs) - width
	if start < 0 {
		start = 0
	}
	ids := series.IDs[start:]
	offset := width - len(ids)
	for i, id := range ids {
		v := series.Items[id]
		v2 := v
		frame[offset+i] = &v2
	}
	return frame
}

func buildKlineFrameByRange(series *klineSeries, leftID int64, rightID int64, width int) []*api.Kline {
	if width <= 0 {
		return nil
	}
	frame := make([]*api.Kline, width)
	if rightID < leftID || len(series.IDs) == 0 {
		return frame
	}
	startID := rightID - int64(width) + 1
	if startID < leftID {
		startID = leftID
	}
	for id := startID; id <= rightID; id++ {
		v, ok := series.Items[id]
		if !ok {
			continue
		}
		idx := int(id - (rightID - int64(width) + 1))
		if idx < 0 || idx >= width {
			continue
		}
		v2 := v
		frame[idx] = &v2
	}
	return frame
}

func containsID(ids []int64, id int64) bool {
	i := sort.Search(len(ids), func(i int) bool { return ids[i] >= id })
	return i < len(ids) && ids[i] == id
}

func insertSortedID(ids []int64, id int64) []int64 {
	i := sort.Search(len(ids), func(i int) bool { return ids[i] >= id })
	if i < len(ids) && ids[i] == id {
		return ids
	}
	ids = append(ids, 0)
	copy(ids[i+1:], ids[i:])
	ids[i] = id
	return ids
}

func removeID(ids []int64, id int64) []int64 {
	i := sort.Search(len(ids), func(i int) bool { return ids[i] >= id })
	if i >= len(ids) || ids[i] != id {
		return ids
	}
	copy(ids[i:], ids[i+1:])
	return ids[:len(ids)-1]
}

func decodeMap(m map[string]any, out any) {
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, out)
}

func asInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	case json.Number:
		i, err := x.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	default:
		return 0, false
	}
}

func asInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int64:
		return x, true
	case float64:
		return int64(x), true
	case json.Number:
		i, err := x.Int64()
		if err != nil {
			return 0, false
		}
		return i, true
	case string:
		i, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		if err != nil {
			return 0, false
		}
		return i, true
	default:
		return 0, false
	}
}

func asBool(v any) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case string:
		s := strings.ToLower(strings.TrimSpace(x))
		if s == "true" {
			return true, true
		}
		if s == "false" {
			return false, true
		}
		return false, false
	default:
		return false, false
	}
}

func asString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}
