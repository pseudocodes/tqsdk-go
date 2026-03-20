package report

// GetPunchline returns a strategy evaluation comment based on the total return rate.
// Mirrors Python tqsdk report.py _get_tqsdk_punchlines.
func GetPunchline(totalReturn float64) string {
	ror := totalReturn * 100 // convert to percentage
	switch {
	case ror <= -75:
		return "飞流直下三千尺，疑是银河落九天"
	case ror <= -50:
		return "路漫漫其修远兮，吾将上下而求索"
	case ror <= -25:
		return "策略需要改进，革命尚未成功，同志仍需努力"
	case ror <= 0:
		return "策略小亏，金钱只是身外之物"
	case ror <= 25:
		return "策略小有盈利，还需细细打磨"
	case ror <= 50:
		return "策略盈利稳健，继续保持"
	case ror <= 75:
		return "策略表现不错，向巴菲特看齐"
	default:
		return "策略看来春风得意，堪比当代索罗斯"
	}
}
