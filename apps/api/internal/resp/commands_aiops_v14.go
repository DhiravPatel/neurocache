package resp

import (
	"strconv"
	"strings"
)

// confidenceCmd handles CONFIDENCE.* — calibration.
//
//   CONFIDENCE.RECORD model-id predicted actual
//   CONFIDENCE.CURVE model-id [BINS n]
//   CONFIDENCE.ECE model-id [BINS n]
//   CONFIDENCE.CALIBRATE model-id raw-conf [BINS n]
//   CONFIDENCE.RESET model-id
//   CONFIDENCE.MODELS
//   CONFIDENCE.STATS
func (c *conn) confidenceCmd(sub string, args []string) {
	switch sub {
	case "RECORD":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'confidence.record'")
			return
		}
		pred, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			writeError(c.bw, "predicted must be a float")
			return
		}
		act, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			writeError(c.bw, "actual must be a float")
			return
		}
		if err := c.eng.Confidence.Record(args[0], pred, act); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "CURVE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'confidence.curve'")
			return
		}
		bins := 0
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "BINS" {
				writeError(c.bw, "unknown CONFIDENCE.CURVE option: "+key)
				return
			}
			n, err := strconv.Atoi(val)
			if err != nil || n <= 0 {
				writeError(c.bw, "BINS must be a positive integer")
				return
			}
			bins = n
			i += 2
		}
		curve, ok := c.eng.Confidence.Curve(args[0], bins)
		if !ok {
			writeTypedError(c.bw, "UNKNOWNMODEL", "no samples recorded for that model")
			return
		}
		out := make([]any, 0, len(curve))
		for _, b := range curve {
			out = append(out, []any{
				"bin_lo", strconv.FormatFloat(b.BinLo, 'f', 4, 64),
				"bin_hi", strconv.FormatFloat(b.BinHi, 'f', 4, 64),
				"predicted_avg", strconv.FormatFloat(b.PredictedAvg, 'f', 4, 64),
				"actual_rate", strconv.FormatFloat(b.ActualRate, 'f', 4, 64),
				"count", strconv.Itoa(b.Count),
				"gap_abs", strconv.FormatFloat(b.GapAbs, 'f', 4, 64),
			})
		}
		writeValue(c.bw, out)
	case "ECE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'confidence.ece'")
			return
		}
		bins := 0
		i := 1
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "BINS" {
				writeError(c.bw, "unknown CONFIDENCE.ECE option: "+key)
				return
			}
			n, err := strconv.Atoi(val)
			if err != nil || n <= 0 {
				writeError(c.bw, "BINS must be a positive integer")
				return
			}
			bins = n
			i += 2
		}
		ece, samples, ok := c.eng.Confidence.ECE(args[0], bins)
		if !ok {
			writeTypedError(c.bw, "UNKNOWNMODEL", "no samples recorded")
			return
		}
		writeArray(c.bw, []string{
			"ece", strconv.FormatFloat(ece, 'f', 6, 64),
			"samples", strconv.Itoa(samples),
		})
	case "CALIBRATE":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'confidence.calibrate'")
			return
		}
		raw, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			writeError(c.bw, "raw-conf must be a float")
			return
		}
		bins := 0
		i := 2
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "BINS" {
				writeError(c.bw, "unknown CONFIDENCE.CALIBRATE option: "+key)
				return
			}
			n, err := strconv.Atoi(val)
			if err != nil || n <= 0 {
				writeError(c.bw, "BINS must be a positive integer")
				return
			}
			bins = n
			i += 2
		}
		calibrated, _ := c.eng.Confidence.Calibrate(args[0], raw, bins)
		writeBulk(c.bw, strconv.FormatFloat(calibrated, 'f', 6, 64))
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'confidence.reset'")
			return
		}
		if c.eng.Confidence.Reset(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "MODELS":
		rows := c.eng.Confidence.Models()
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"model_id", r.ModelID,
				"samples", strconv.Itoa(r.Samples),
				"cap", strconv.Itoa(r.Cap),
			})
		}
		writeValue(c.bw, out)
	case "STATS":
		s := c.eng.Confidence.Stats()
		writeArray(c.bw, []string{
			"models", strconv.Itoa(s.Models),
			"total_records", strconv.FormatInt(s.TotalRecords, 10),
			"total_curves", strconv.FormatInt(s.TotalCurves, 10),
			"total_calibrates", strconv.FormatInt(s.TotalCals, 10),
		})
	default:
		writeError(c.bw, "unknown CONFIDENCE subcommand: "+sub)
	}
}

// driftCmd handles DRIFT.* — input distribution drift detection.
//
//   DRIFT.BASELINE tracker-id [WINDOW n] sample1 sample2 sample3 ...
//   DRIFT.OBSERVE tracker-id text
//   DRIFT.SCORE tracker-id
//   DRIFT.RESET tracker-id
//   DRIFT.FORGET tracker-id
//   DRIFT.TRACKERS
//   DRIFT.STATS
func (c *conn) driftCmd(sub string, args []string) {
	switch sub {
	case "BASELINE":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'drift.baseline'")
			return
		}
		window := 0
		samples := args[1:]
		// Parse optional leading [WINDOW n]
		if len(samples) >= 2 && strings.EqualFold(samples[0], "WINDOW") {
			n, err := strconv.Atoi(samples[1])
			if err != nil || n <= 0 {
				writeError(c.bw, "WINDOW must be a positive integer")
				return
			}
			window = n
			samples = samples[2:]
		}
		if err := c.eng.Drift.SetBaseline(args[0], samples, window); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "OBSERVE":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'drift.observe'")
			return
		}
		r, ok := c.eng.Drift.Observe(args[0], args[1])
		if !ok {
			writeTypedError(c.bw, "UNKNOWNTRACKER", "no baseline set for that tracker")
			return
		}
		writeArray(c.bw, []string{
			"samples", strconv.Itoa(r.Samples),
			"score", strconv.FormatFloat(r.Score, 'f', 4, 64),
			"verdict", r.Verdict,
		})
	case "SCORE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'drift.score'")
			return
		}
		r, ok := c.eng.Drift.Score(args[0])
		if !ok {
			writeTypedError(c.bw, "UNKNOWNTRACKER", "no baseline set for that tracker")
			return
		}
		writeArray(c.bw, []string{
			"tracker_id", r.TrackerID,
			"samples", strconv.Itoa(r.Samples),
			"score", strconv.FormatFloat(r.Score, 'f', 4, 64),
			"verdict", r.Verdict,
		})
	case "RESET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'drift.reset'")
			return
		}
		if c.eng.Drift.Reset(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'drift.forget'")
			return
		}
		if c.eng.Drift.Forget(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "TRACKERS":
		rows := c.eng.Drift.Trackers()
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"tracker_id", r.TrackerID,
				"baseline_size", strconv.Itoa(r.BaselineSize),
				"samples", strconv.Itoa(r.Samples),
				"score", strconv.FormatFloat(r.Score, 'f', 4, 64),
				"verdict", r.Verdict,
			})
		}
		writeValue(c.bw, out)
	case "STATS":
		s := c.eng.Drift.Stats()
		writeArray(c.bw, []string{
			"trackers", strconv.Itoa(s.Trackers),
			"total_baselines", strconv.FormatInt(s.TotalBaselines, 10),
			"total_observes", strconv.FormatInt(s.TotalObserves, 10),
			"total_scores", strconv.FormatInt(s.TotalScores, 10),
		})
	default:
		writeError(c.bw, "unknown DRIFT subcommand: "+sub)
	}
}

// watermarkCmd handles WATERMARK.* — AI-generated text detection.
//
//   WATERMARK.SCORE text                       -> [score, verdict, signals]
//   WATERMARK.PATTERN.ADD name regex weight
//   WATERMARK.PATTERN.REMOVE name
//   WATERMARK.PATTERN.LIST
//   WATERMARK.STATS
func (c *conn) watermarkCmd(sub string, args []string) {
	switch sub {
	case "SCORE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'watermark.score'")
			return
		}
		r := c.eng.Watermark.Score(args[0])
		sigsAny := make([]any, 0, len(r.Signals))
		for _, s := range r.Signals {
			sigsAny = append(sigsAny, []any{
				"name", s.Name,
				"contribution", strconv.FormatFloat(s.Contribution, 'f', 4, 64),
				"weight", strconv.FormatFloat(s.Weight, 'f', 4, 64),
			})
		}
		writeValue(c.bw, []any{
			"score", strconv.FormatFloat(r.Score, 'f', 4, 64),
			"verdict", r.Verdict,
			"words", strconv.Itoa(r.Words),
			"signals", sigsAny,
		})
	case "PATTERN.ADD":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'watermark.pattern.add'")
			return
		}
		weight, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			writeError(c.bw, "weight must be a float")
			return
		}
		if err := c.eng.Watermark.AddPattern(args[0], args[1], weight); err != nil {
			writeError(c.bw, "bad pattern: "+err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "PATTERN.REMOVE":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'watermark.pattern.remove'")
			return
		}
		if c.eng.Watermark.RemovePattern(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "PATTERN.LIST":
		rows := c.eng.Watermark.Patterns()
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				"name", r.Name,
				"source", r.Source,
				"weight", strconv.FormatFloat(r.Weight, 'f', 4, 64),
			})
		}
		writeValue(c.bw, out)
	case "STATS":
		s := c.eng.Watermark.Stats()
		writeArray(c.bw, []string{
			"custom_patterns", strconv.Itoa(s.CustomPatterns),
			"total_scores", strconv.FormatInt(s.TotalScores, 10),
			"total_likely_ai", strconv.FormatInt(s.TotalLikelyAI, 10),
		})
	default:
		writeError(c.bw, "unknown WATERMARK subcommand: "+sub)
	}
}
