package resp

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
)

// guardrailCmd handles GUARDRAIL.* — composable safety pipeline.
//
//   GUARDRAIL.DEFINE pipeline-id stage-spec
//        e.g. "inject:0.8,redact,length:8000"
//   GUARDRAIL.RUN pipeline-id text
//        [OUTPUT text] [SOURCE text [SOURCE text...]]
//        [ALL_STAGES 1] [CUSTOM stage_name 0|1 ...]
//        -> [pass, stages[], final_text]
//   GUARDRAIL.LIST                 -> defined pipelines
//   GUARDRAIL.FORGET pipeline-id   -> int
//   GUARDRAIL.STATS
func (c *conn) guardrailCmd(sub string, args []string) {
	switch sub {
	case "DEFINE":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'guardrail.define'")
			return
		}
		if err := c.eng.Guardrail.Define(args[0], args[1]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "RUN":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'guardrail.run'")
			return
		}
		opts := llmstack.RunOpts{CustomPass: map[string]bool{}}
		i := 2
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "OUTPUT":
				opts.Output = val
				i += 2
			case "SOURCE":
				opts.Sources = append(opts.Sources, val)
				i += 2
			case "ALL_STAGES":
				opts.AllStages = val == "1" || strings.EqualFold(val, "true")
				i += 2
			case "CUSTOM":
				if i+2 >= len(args) {
					writeError(c.bw, "CUSTOM needs <name> <0|1>")
					return
				}
				stage := val
				v := args[i+2]
				opts.CustomPass[stage] = v == "1" || strings.EqualFold(v, "true")
				i += 3
			default:
				writeError(c.bw, "unknown GUARDRAIL.RUN option: "+key)
				return
			}
		}
		r, ok := c.eng.Guardrail.Run(args[0], args[1], opts)
		if !ok {
			writeTypedError(c.bw, "UNKNOWNPIPELINE", "no pipeline registered for that id")
			return
		}
		stagesAny := make([]any, 0, len(r.Stages))
		for _, s := range r.Stages {
			passInt := "0"
			if s.Pass {
				passInt = "1"
			}
			row := []any{
				"name", s.Name,
				"kind", s.Kind,
				"pass", passInt,
				"details", s.Details,
			}
			if s.Token != "" {
				row = append(row, "token", s.Token)
			}
			stagesAny = append(stagesAny, row)
		}
		passInt := "0"
		if r.Pass {
			passInt = "1"
		}
		writeValue(c.bw, []any{
			"pass", passInt,
			"stages", stagesAny,
			"final_text", r.FinalText,
		})
	case "LIST":
		rows := c.eng.Guardrail.Pipelines()
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			stagesAny := make([]any, 0, len(r.Stage))
			for _, s := range r.Stage {
				stagesAny = append(stagesAny, s)
			}
			out = append(out, []any{
				"id", r.ID,
				"spec", r.Spec,
				"stages", stagesAny,
			})
		}
		writeValue(c.bw, out)
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'guardrail.forget'")
			return
		}
		if c.eng.Guardrail.Forget(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "STATS":
		s := c.eng.Guardrail.Stats()
		writeArray(c.bw, []string{
			"total_runs", strconv.FormatInt(s.TotalRuns, 10),
			"total_pass", strconv.FormatInt(s.TotalPass, 10),
			"total_fail", strconv.FormatInt(s.TotalFail, 10),
			"pipelines", strconv.Itoa(s.Pipelines),
		})
	default:
		writeError(c.bw, "unknown GUARDRAIL subcommand: "+sub)
	}
}

// structCmd handles STRUCT.* — JSON schema validation + repair prompts.
//
//   STRUCT.SCHEMA.SET schema-id <json-schema>
//   STRUCT.SCHEMA.GET schema-id        -> raw schema string
//   STRUCT.SCHEMA.LIST                 -> array of schema-ids
//   STRUCT.VALIDATE schema-id text     -> [valid, errors[]]
//   STRUCT.REPAIR_PROMPT schema-id text -> bulk-string remediation prompt
//   STRUCT.FORGET schema-id            -> int
//   STRUCT.STATS
func (c *conn) structCmd(sub string, args []string) {
	switch sub {
	case "SCHEMA.SET":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'struct.schema.set'")
			return
		}
		if err := c.eng.Struct.SetSchema(args[0], args[1]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "SCHEMA.GET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'struct.schema.get'")
			return
		}
		s, ok := c.eng.Struct.GetSchema(args[0])
		if !ok {
			writeNil(c.bw)
			return
		}
		// Re-marshal so output is a clean canonical JSON string.
		writeBulk(c.bw, marshalSchemaJSON(s))
	case "SCHEMA.LIST":
		writeArray(c.bw, c.eng.Struct.Schemas())
	case "VALIDATE":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'struct.validate'")
			return
		}
		r, ok := c.eng.Struct.Validate(args[0], args[1])
		if !ok {
			writeTypedError(c.bw, "UNKNOWNSCHEMA", "no schema registered for that id")
			return
		}
		validInt := "0"
		if r.Valid {
			validInt = "1"
		}
		errs := make([]any, 0, len(r.Errors))
		for _, e := range r.Errors {
			errs = append(errs, []any{
				"path", e.Path,
				"message", e.Message,
			})
		}
		writeValue(c.bw, []any{
			"valid", validInt,
			"errors", errs,
		})
	case "REPAIR_PROMPT":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'struct.repair_prompt'")
			return
		}
		p, ok := c.eng.Struct.RepairPrompt(args[0], args[1])
		if !ok {
			writeTypedError(c.bw, "UNKNOWNSCHEMA", "no schema registered for that id")
			return
		}
		writeBulk(c.bw, p)
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'struct.forget'")
			return
		}
		if c.eng.Struct.Forget(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "STATS":
		s := c.eng.Struct.Stats()
		writeArray(c.bw, []string{
			"total_validates", strconv.FormatInt(s.TotalValidates, 10),
			"total_valid", strconv.FormatInt(s.TotalValid, 10),
			"total_invalid", strconv.FormatInt(s.TotalInvalid, 10),
			"schemas", strconv.Itoa(s.Schemas),
		})
	default:
		writeError(c.bw, "unknown STRUCT subcommand: "+sub)
	}
}

// coalesceCmd handles COALESCE.* — single-flight thundering-herd protection.
//
//   COALESCE.LOCK key timeout-ms      -> [owner, token]
//   COALESCE.PUBLISH key token result -> int (1 if published)
//   COALESCE.WAIT key timeout-ms      -> [got, result]
//   COALESCE.STATUS key               -> snapshot
//   COALESCE.KEYS                     -> active keys
//   COALESCE.FORGET key               -> int
//   COALESCE.STATS
func (c *conn) coalesceCmd(sub string, args []string) {
	switch sub {
	case "LOCK":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'coalesce.lock'")
			return
		}
		ms, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || ms <= 0 {
			writeError(c.bw, "timeout-ms must be a positive integer")
			return
		}
		r := c.eng.Coalesce.Lock(args[0], ms)
		ownerInt := "0"
		if r.Owner {
			ownerInt = "1"
		}
		writeArray(c.bw, []string{
			"owner", ownerInt,
			"token", r.Token,
		})
	case "PUBLISH":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'coalesce.publish'")
			return
		}
		if c.eng.Coalesce.Publish(args[0], args[1], args[2]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "WAIT":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'coalesce.wait'")
			return
		}
		ms, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || ms <= 0 {
			writeError(c.bw, "timeout-ms must be a positive integer")
			return
		}
		r := c.eng.Coalesce.Wait(args[0], time.Duration(ms)*time.Millisecond)
		gotInt := "0"
		if r.Got {
			gotInt = "1"
		}
		writeArray(c.bw, []string{
			"got", gotInt,
			"result", r.Result,
		})
	case "STATUS":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'coalesce.status'")
			return
		}
		s, ok := c.eng.Coalesce.Status(args[0])
		if !ok {
			writeNilArray(c.bw)
			return
		}
		hasResult := "0"
		if s.HasResult {
			hasResult = "1"
		}
		writeArray(c.bw, []string{
			"key", s.Key,
			"state", s.State,
			"locked_at", strconv.FormatInt(s.LockedAt, 10),
			"published_at", strconv.FormatInt(s.PublishedAt, 10),
			"timeout_ms", strconv.FormatInt(s.TimeoutMS, 10),
			"has_result", hasResult,
		})
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'coalesce.forget'")
			return
		}
		if c.eng.Coalesce.Forget(args[0]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "KEYS":
		writeArray(c.bw, c.eng.Coalesce.Keys())
	case "STATS":
		s := c.eng.Coalesce.Stats()
		writeArray(c.bw, []string{
			"active", strconv.Itoa(s.Active),
			"total_locks", strconv.FormatInt(s.TotalLocks, 10),
			"total_acquires", strconv.FormatInt(s.TotalAcquires, 10),
			"total_contended", strconv.FormatInt(s.TotalContended, 10),
			"total_publishes", strconv.FormatInt(s.TotalPublishes, 10),
			"total_waits", strconv.FormatInt(s.TotalWaits, 10),
			"total_wait_hits", strconv.FormatInt(s.TotalWaitHits, 10),
			"total_wait_misses", strconv.FormatInt(s.TotalWaitMisses, 10),
			"save_rate", strconv.FormatFloat(s.SaveRate, 'f', 4, 64),
		})
	default:
		writeError(c.bw, "unknown COALESCE subcommand: "+sub)
	}
}

// marshalSchemaJSON serializes the parsed schema map back to JSON
// for STRUCT.SCHEMA.GET. Errors are unlikely (we just unmarshalled
// from the same dialect) but we fall back to "{}" if they happen.
func marshalSchemaJSON(schema map[string]any) string {
	b, err := json.Marshal(schema)
	if err != nil {
		return "{}"
	}
	return string(b)
}
