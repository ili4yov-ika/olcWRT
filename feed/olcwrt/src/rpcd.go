package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// rpcdMethods describes the luci.olcwrt ubus object for rpcd. The values are
// type hints: strings for string arguments, numbers for integers.
var rpcdMethods = map[string]map[string]any{
	"status":    {},
	"logs":      {"lines": 0},
	"import":    {"text": ""},
	"export":    {},
	"job_start": {"kind": "", "arg": ""},
	"job_get":   {"id": ""},
	"reload":    {},
}

// cmdRpcd implements the rpcd executable plugin protocol:
// "list" prints the method signatures, "call <method>" reads JSON arguments
// from stdin and prints a JSON object.
func cmdRpcd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: olcwrt rpcd list|call <method>")
	}
	switch args[0] {
	case "list":
		return json.NewEncoder(os.Stdout).Encode(rpcdMethods)
	case "call":
		if len(args) < 2 {
			return errors.New("missing method")
		}
		var in map[string]any
		data, _ := io.ReadAll(io.LimitReader(os.Stdin, fetchLimit))
		if len(strings.TrimSpace(string(data))) > 0 {
			if err := json.Unmarshal(data, &in); err != nil {
				return json.NewEncoder(os.Stdout).Encode(map[string]any{"error": "invalid arguments"})
			}
		}
		out, err := rpcdCall(args[1], in)
		if err != nil {
			out = map[string]any{"error": err.Error()}
		}
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	return fmt.Errorf("unknown rpcd action %q", args[0])
}

func argString(in map[string]any, key string) string {
	if v, ok := in[key].(string); ok {
		return v
	}
	return ""
}

func argInt(in map[string]any, key string, def int) int {
	if v, ok := in[key].(float64); ok && v > 0 {
		return int(v)
	}
	return def
}

func rpcdCall(method string, in map[string]any) (any, error) {
	switch method {
	case "status":
		return collectStatus(), nil
	case "logs":
		lines := tailLog(logPath, min(argInt(in, "lines", 300), 2000))
		if lines == nil {
			lines = []string{}
		}
		return map[string]any{"lines": lines}, nil
	case "import":
		return rpcdImport(argString(in, "text"))
	case "export":
		return rpcdExport()
	case "job_start":
		id, err := startJob(argString(in, "kind"), argString(in, "arg"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": id}, nil
	case "job_get":
		return getJob(argString(in, "id"))
	case "reload":
		if err := signalDaemonReload(); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	}
	return nil, fmt.Errorf("unknown method %q", method)
}

func rpcdImport(text string) (any, error) {
	var res *ImportResult
	err := withConfigLock(func(cfg *Config) error {
		var err error
		res, err = importText(cfg, text)
		return err
	})
	if res == nil {
		return nil, err
	}
	out := map[string]any{"result": res}
	if err != nil {
		out["error"] = err.Error()
	}
	if len(res.Subscriptions) > 0 {
		if id, jerr := startJob("sub_update", strings.Join(res.Subscriptions, ",")); jerr == nil {
			out["job"] = id
		}
	} else if err == nil {
		reloadIfRunning()
	}
	return out, nil
}

func rpcdExport() (any, error) {
	cfg, err := loadConfigLenient()
	if err != nil {
		return nil, err
	}
	uris := map[string]string{}
	for _, s := range cfg.Servers {
		if s.Validate() == nil {
			uris[s.ID] = FormatURI(s)
		}
	}
	return map[string]any{"uris": uris}, nil
}

// Job is a long-running action started from LuCI and polled by its id.
type Job struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Arg      string `json:"arg,omitempty"`
	State    string `json:"state"`
	Started  int64  `json:"started"`
	Finished int64  `json:"finished,omitempty"`
	Result   any    `json:"result,omitempty"`
	Error    string `json:"error,omitempty"`
}

var (
	jobKinds = []string{"sub_update", "lists_update", "check"}
	jobArgRe = regexp.MustCompile(`^[A-Za-z0-9_,-]*$`)
	jobIDRe  = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

func jobPath(id string) string { return filepath.Join(jobsDir(), id+".json") }

// startJob spawns `olcwrt job <id> <kind> <arg>` detached from rpcd.
func startJob(kind, arg string) (string, error) {
	if !contains(jobKinds, kind) {
		return "", fmt.Errorf("unknown job kind %q", kind)
	}
	if !jobArgRe.MatchString(arg) {
		return "", errors.New("invalid job argument")
	}
	if err := os.MkdirAll(jobsDir(), 0o755); err != nil {
		return "", err
	}
	pruneJobs()
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	id := hex.EncodeToString(buf)
	job := &Job{ID: id, Kind: kind, Arg: arg, State: "running", Started: time.Now().Unix()}
	if err := writeJSON(jobPath(id), job, 0o644); err != nil {
		return "", err
	}
	self, err := os.Executable()
	if err != nil {
		self = "/usr/bin/olcwrt"
	}
	cmd := exec.Command(self, "job", id, kind, arg)
	cmd.SysProcAttr = detachedSysProcAttr()
	if err := cmd.Start(); err != nil {
		return "", err
	}
	_ = cmd.Process.Release()
	return id, nil
}

func getJob(id string) (*Job, error) {
	if !jobIDRe.MatchString(id) {
		return nil, errors.New("invalid job id")
	}
	job := &Job{}
	if err := readJSON(jobPath(id), job); err != nil {
		return nil, errors.New("no such job")
	}
	return job, nil
}

func pruneJobs() {
	entries, _ := os.ReadDir(jobsDir())
	for _, e := range entries {
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > time.Hour {
			_ = os.Remove(filepath.Join(jobsDir(), e.Name()))
		}
	}
}

// cmdJob is the detached worker started by startJob.
func cmdJob(args []string) error {
	if len(args) < 2 || !jobIDRe.MatchString(args[0]) {
		return errors.New("usage: olcwrt job <id> <kind> [arg]")
	}
	id, kind, arg := args[0], args[1], ""
	if len(args) > 2 {
		arg = args[2]
	}
	job := &Job{ID: id, Kind: kind, Arg: arg, State: "running", Started: time.Now().Unix()}
	_ = readJSON(jobPath(id), job)

	result, err := runJob(kind, arg)
	job.State, job.Finished, job.Result = "done", time.Now().Unix(), result
	if err != nil {
		job.Error = err.Error()
	}
	return writeJSON(jobPath(id), job, 0o644)
}

func runJob(kind, arg string) (any, error) {
	var ids []string
	for _, s := range strings.Split(arg, ",") {
		if s = strings.TrimSpace(s); s != "" {
			ids = append(ids, s)
		}
	}
	switch kind {
	case "sub_update":
		var res []*SubResult
		err := withConfigLock(func(*Config) error {
			res = updateSubsLocked(ids)
			return nil
		})
		reloadIfRunning()
		return res, err
	case "lists_update":
		var idx map[string]*ListMeta
		err := withConfigLock(func(cfg *Config) error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			idx, _ = updateLists(ctx, cfg, false)
			return nil
		})
		reloadIfRunning()
		return idx, err
	case "check":
		cfg, err := loadConfigLenient()
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if len(ids) == 0 {
			return checkRunning(ctx, cfg), nil
		}
		s := cfg.ServerByID[ids[0]]
		if s == nil {
			return nil, fmt.Errorf("no such server %q", ids[0])
		}
		return checkServer(ctx, cfg, s), nil
	}
	return nil, fmt.Errorf("unknown job kind %q", kind)
}
