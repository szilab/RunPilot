package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/szilab/RunPilot/internal/history"
	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/plugins"
	"github.com/szilab/RunPilot/internal/scheduler"
)

func historyFailure(err error) error {
	switch {
	case errors.Is(err, history.ErrExecutionNotFound):
		return &plugins.HostFailure{Code: "not_found", Message: "unknown execution"}
	case errors.Is(err, history.ErrInvalidExecution):
		return &plugins.HostFailure{Code: "invalid_argument", Message: err.Error()}
	default:
		return err
	}
}

func decodeHistoryParams(raw json.RawMessage, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil || decoder.More() {
		return &plugins.HostFailure{Code: "invalid_argument", Message: "invalid history parameters"}
	}
	return nil
}

// History implements the generic owner-scoped execution capability. The
// runtime supplies owner from the loaded plugin ID; plugins cannot name
// another owner or a filesystem path.
func (h controllerPluginHost) History(_ context.Context, owner, operation string, raw json.RawMessage) (json.RawMessage, error) {
	store := h.controller.history
	switch operation {
	case "begin":
		var args struct {
			Kind    string `json:"kind"`
			Subject string `json:"subject"`
			Label   string `json:"label"`
		}
		if err := decodeHistoryParams(raw, &args); err != nil {
			return nil, err
		}
		if args.Kind == "" {
			args.Kind = "run"
		}
		execution, err := store.BeginExecution(owner, args.Kind, args.Subject, args.Label)
		if err != nil {
			return nil, historyFailure(err)
		}
		return json.Marshal(execution)
	case "append":
		var args struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		}
		if err := decodeHistoryParams(raw, &args); err != nil {
			return nil, err
		}
		if err := store.AppendExecutionOutput(owner, args.ID, args.Text); err != nil {
			return nil, historyFailure(err)
		}
		return nil, nil
	case "finish":
		var args struct {
			ID       string `json:"id"`
			ExitCode *int   `json:"exitCode"`
			Success  *bool  `json:"success"`
			Message  string `json:"message"`
		}
		if err := decodeHistoryParams(raw, &args); err != nil {
			return nil, err
		}
		execution, err := store.FinishExecution(owner, args.ID, args.ExitCode, args.Success, args.Message)
		if err != nil {
			return nil, historyFailure(err)
		}
		return json.Marshal(execution)
	case "list":
		var args struct {
			Subject string `json:"subject"`
			Limit   int    `json:"limit"`
		}
		if err := decodeHistoryParams(raw, &args); err != nil {
			return nil, err
		}
		executions, err := store.ListExecutions(owner, args.Subject, args.Limit)
		if err != nil {
			return nil, historyFailure(err)
		}
		return json.Marshal(map[string]any{"executions": executions})
	case "get":
		var args struct {
			ID string `json:"id"`
		}
		if err := decodeHistoryParams(raw, &args); err != nil {
			return nil, err
		}
		execution, err := store.GetExecution(owner, args.ID)
		if err != nil {
			return nil, historyFailure(err)
		}
		return json.Marshal(execution)
	case "output":
		var args struct {
			ID       string `json:"id"`
			MaxBytes int    `json:"maxBytes"`
		}
		if err := decodeHistoryParams(raw, &args); err != nil {
			return nil, err
		}
		text, size, truncated, err := store.ReadExecutionOutput(owner, args.ID, args.MaxBytes)
		if err != nil {
			return nil, historyFailure(err)
		}
		return json.Marshal(map[string]any{"id": args.ID, "output": text, "size": size, "truncated": truncated})
	default:
		return nil, fmt.Errorf("unknown history operation")
	}
}

// ScheduleValidate compiles a schedule with the same rules used by
// registration, without creating a timer.
func (h controllerPluginHost) ScheduleValidate(_ context.Context, raw json.RawMessage) error {
	var value struct {
		Schedule model.ScheduleSpec `json:"schedule"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return &plugins.HostFailure{Code: "invalid_argument", Message: "invalid schedule validation request"}
	}
	if _, err := scheduler.Expression(value.Schedule); err != nil {
		return &plugins.HostFailure{Code: "invalid_argument", Message: err.Error()}
	}
	return nil
}
