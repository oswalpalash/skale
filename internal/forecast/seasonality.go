package forecast

import (
	"math"
	"time"
)

type SeasonalityDetection struct {
	Detected    bool
	Period      time.Duration
	Confidence  float64
	Correlation float64
	Message     string
}

type SeasonalityDetectionOptions struct {
	MinPeriod      time.Duration
	MaxPeriod      time.Duration
	MinCycles      int
	MinCorrelation float64
}

func DetectSeasonality(series []Point, options SeasonalityDetectionOptions) SeasonalityDetection {
	step := inferStep(series)
	if len(series) < 6 || step <= 0 {
		return SeasonalityDetection{Message: "no seasonality detected: insufficient evenly spaced history"}
	}

	minPeriod := options.MinPeriod
	if minPeriod <= 0 {
		minPeriod = 2 * step
	}
	maxPeriod := options.MaxPeriod
	if maxPeriod <= 0 {
		maxPeriod = time.Duration(len(series)/3) * step
	}
	if maxPeriod < minPeriod {
		return SeasonalityDetection{Message: "no seasonality detected: history does not cover enough candidate periods"}
	}

	minCycles := options.MinCycles
	if minCycles < 2 {
		minCycles = 3
	}
	minCorrelation := options.MinCorrelation
	if minCorrelation <= 0 {
		minCorrelation = 0.75
	}

	minLag := int(math.Ceil(float64(minPeriod) / float64(step)))
	if minLag < 2 {
		minLag = 2
	}
	maxLag := int(math.Floor(float64(maxPeriod) / float64(step)))
	maxLag = min(maxLag, len(series)/minCycles)
	if maxLag < minLag {
		return SeasonalityDetection{Message: "no seasonality detected: history does not cover enough repeated periods"}
	}

	values := values(series)
	persistenceMAE := lagMeanAbsoluteError(values, 1)
	bestLag := 0
	bestCorrelation := -1.0
	for lag := minLag; lag <= maxLag; lag++ {
		lagMAE := lagMeanAbsoluteError(values, lag)
		if persistenceMAE > 0 && lagMAE >= persistenceMAE*0.8 {
			continue
		}
		correlation := lagCorrelation(values, lag)
		if correlation > bestCorrelation {
			bestCorrelation = correlation
			bestLag = lag
		}
	}
	if bestLag == 0 || bestCorrelation < minCorrelation {
		return SeasonalityDetection{
			Correlation: bestCorrelation,
			Message:     "no seasonality detected with enough evidence",
		}
	}

	return SeasonalityDetection{
		Detected:    true,
		Period:      time.Duration(bestLag) * step,
		Confidence:  clamp((bestCorrelation-minCorrelation)/(1-minCorrelation), 0, 1),
		Correlation: bestCorrelation,
		Message:     "seasonality detected from demand autocorrelation",
	}
}

func lagMeanAbsoluteError(values []float64, lag int) float64 {
	if lag <= 0 || len(values) <= lag {
		return 0
	}
	var sum float64
	var count int
	for index := lag; index < len(values); index++ {
		sum += math.Abs(values[index] - values[index-lag])
		count++
	}
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}

func lagCorrelation(values []float64, lag int) float64 {
	if lag <= 0 || len(values) <= lag {
		return 0
	}

	left := values[lag:]
	right := values[:len(values)-lag]
	leftMean := averageFloat64(left)
	rightMean := averageFloat64(right)

	var numerator float64
	var leftDenominator float64
	var rightDenominator float64
	for index := range left {
		leftCentered := left[index] - leftMean
		rightCentered := right[index] - rightMean
		numerator += leftCentered * rightCentered
		leftDenominator += leftCentered * leftCentered
		rightDenominator += rightCentered * rightCentered
	}
	denominator := math.Sqrt(leftDenominator * rightDenominator)
	if denominator <= 0 {
		return 0
	}
	return numerator / denominator
}

func averageFloat64(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}
