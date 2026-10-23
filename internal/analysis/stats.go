package analysis

import "math"

// mean 返回算术均值；空切片为 0（与 core statistics.Mean 同口径）。
func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

// sampleStdDev 返回样本标准差（n-1）；少于 2 个点为 0。
func sampleStdDev(values []float64) float64 {
	n := len(values)
	if n < 2 {
		return 0
	}
	m := mean(values)
	sumSq := 0.0
	for _, v := range values {
		d := v - m
		sumSq += d * d
	}
	return math.Sqrt(sumSq / float64(n-1))
}

// pearson 是 Pearson 相关系数，与 core statistics.Pearson 同式：
// 长度不等或 < 2 点、或任一序列方差为 0 ⇒ NaN。
func pearson(x, y []float64) float64 {
	if len(x) != len(y) || len(x) < 2 {
		return math.NaN()
	}
	n := float64(len(x))
	var sumX, sumY, sumXY, sumX2, sumY2 float64
	for i := range x {
		sumX += x[i]
		sumY += y[i]
		sumXY += x[i] * y[i]
		sumX2 += x[i] * x[i]
		sumY2 += y[i] * y[i]
	}
	numerator := n*sumXY - sumX*sumY
	denominator := math.Sqrt((n*sumX2 - sumX*sumX) * (n*sumY2 - sumY*sumY))
	if denominator == 0 {
		return math.NaN()
	}
	return numerator / denominator
}

// cumulativeReturn 把收益序列累乘成区间总收益：Π(1+r)-1。
func cumulativeReturn(returns []float64) float64 {
	product := 1.0
	for _, r := range returns {
		product *= 1 + r
	}
	return product - 1
}
