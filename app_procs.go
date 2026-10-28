package main

import (
	"encoding/base64"
	"fmt"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"openuai/internal/logger"
	"openuai/internal/procs"
	"openuai/internal/tray"
)

// procCallbacks forwards background-process activity to the UI (Wails events)
// and wakes the agent when a process ends without it noticing.
func (a *App) procCallbacks() procs.Callbacks {
	emit := func(name string, payload any) {
		wailsRuntime.EventsEmit(a.ctx, name, payload)
		if a.apiServer != nil {
			a.apiServer.Broadcast(name, "", payload)
		}
	}
	return procs.Callbacks{
		OnStart: func(i procs.Info) {
			emit("proc_start", i)
			a.saveRunningProcs()
		},
		// Raw terminal bytes go to the user's terminal only, never to the LLM.
		OnOutput: func(id string, offset int64, data []byte) {
			wailsRuntime.EventsEmit(a.ctx, "proc_output", map[string]any{
				"id": id, "offset": offset, "data": base64.StdEncoding.EncodeToString(data),
			})
		},
		OnStatus: func(i procs.Info) {
			emit("proc_status", i)
			if i.Status == procs.StatusWaiting {
				tray.Notify("OpenUAI", fmt.Sprintf("%s is waiting for your input", i.Command))
			}
		},
		OnExit: func(i procs.Info) {
			emit("proc_exit", i)
			a.saveRunningProcs()
			if a.procs.ExitObserved(i.ID) {
				return
			}
			notification := fmt.Sprintf("[Background process %s (%s) exited with code %d. Use proc_read to see its final output, then continue with the task.]", i.ID, i.Command, i.ExitCode)
			logger.Info("Process %s exited (code %d), waking the agent", i.ID, i.ExitCode)
			go a.triggerAgentIf(notification, func() bool { return !a.procs.ExitObserved(i.ID) })
		},
	}
}

// ProcSnapshot is the buffered terminal output of a process, for the terminal
// view to replay when it (re)appears.
type ProcSnapshot struct {
	Data  string `json:"data"` // base64 of the raw bytes
	Total int64  `json:"total"`
}

// ListProcs returns every background process.
func (a *App) ListProcs() []procs.Info { return a.procs.List() }

// GetProcSnapshot returns the raw buffered output of a process.
func (a *App) GetProcSnapshot(id string) ProcSnapshot {
	b, total, err := a.procs.Snapshot(id)
	if err != nil {
		return ProcSnapshot{}
	}
	return ProcSnapshot{Data: base64.StdEncoding.EncodeToString(b), Total: total}
}

// ProcInput sends what the user typed in the terminal to the process. It is
// kept from the LLM.
func (a *App) ProcInput(id, data string) string {
	if err := a.procs.WriteUser(id, []byte(data)); err != nil {
		return err.Error()
	}
	return ""
}

// ProcResize resizes the terminal of a process.
func (a *App) ProcResize(id string, cols, rows int) string {
	if err := a.procs.Resize(id, cols, rows); err != nil {
		return err.Error()
	}
	return ""
}

// KillProc stops a process on behalf of the user.
func (a *App) KillProc(id string) string {
	if err := a.procs.Kill(id, false); err != nil {
		return err.Error()
	}
	return ""
}
