// Package tday 是交易日与交易时段的纯计算，逐行对应 Python tqsdk/datetime.py：
// 交易日 D 从前一工作日 18:00 开始、到 D 当天 18:00 结束；周末顺延到周一，不使用节假日表。
// 所有时间戳为 Unix 纳秒，按北京时间解释。
package tday

import (
	"strconv"
	"strings"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

const (
	day       = int64(24 * time.Hour)
	beginMark = int64(631123200) * int64(time.Second) // 1990-01-01 00:00 CST，星期一
	hour18    = 18 * int64(time.Hour)
)

// CST 是北京时间。
var CST = time.FixedZone("CST", 8*3600)

// Of 返回 ts 所属交易日（当天 00:00 CST 的时间戳）。
func Of(ts int64) int64 {
	days := floorDiv(ts-beginMark, day)
	if (ts-beginMark)-days*day >= hour18 {
		days++
	}
	if wd := days % 7; wd >= 5 {
		days += 7 - wd
	}
	return beginMark + days*day
}

// Start 返回交易日 td 的开始时间：前一工作日 18:00。
func Start(td int64) int64 {
	start := td - 6*int64(time.Hour)
	if wd := floorDiv(start-beginMark, day) % 7; wd >= 5 {
		start -= day * (wd - 4)
	}
	return start
}

// End 返回交易日 td 的结束时间：当天 18:00 前 1 纳秒。
func End(td int64) int64 { return td + hour18 - 1 }

// Format 返回交易日的 YYYYMMDD 文本。
func Format(td int64) string { return time.Unix(0, td).In(CST).Format("20060102") }

// Datetime 返回 Python 行情时间格式 "2006-01-02 15:04:05.000000"。
func Datetime(ts int64) string { return time.Unix(0, ts).In(CST).Format("2006-01-02 15:04:05.000000") }

// InTradingTime 报告 now 是否落在合约当前交易日的交易时段内（Python _is_in_trading_time）。
func InTradingTime(tt model.TradingTime, now int64) bool {
	cur := Of(now)
	last := Of(Start(cur) - 1)
	return inPeriods(cur, tt.Day, now) || inPeriods(last, tt.Night, now)
}

func inPeriods(date int64, periods [][]string, now int64) bool {
	for _, p := range periods {
		if len(p) == 2 && date+clock(p[0]) <= now && now < date+clock(p[1]) {
			return true
		}
	}
	return false
}

// clock 把 "HH:MM:SS"（小时可超过 24）转为纳秒偏移。
func clock(s string) int64 {
	var sec int64
	for _, part := range strings.Split(s, ":") {
		n, _ := strconv.ParseInt(part, 10, 64)
		sec = sec*60 + n
	}
	return sec * int64(time.Second)
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}
