// Package headless implements scriptable commands without starting a WebView.
package headless

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bqbot/internal/control"
	"bqbot/internal/service"
	"bqbot/internal/store"
	"bqbot/internal/team"
)

// Handles identifies the commands to dispatch before starting the desktop.
func Handles(args []string) bool {
	return len(args) > 0 && (args[0] == "team" || args[0] == "run" || args[0] == "control")
}

// Connect reads the private discovery file; credentials are never command args.
func Connect() (*service.Client, error) {
	dir, err := store.DefaultDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "service.json")
	info, err := os.Lstat(path)
	if err != nil {
		return nil, errors.New("start bqbot serve or the desktop before using this command")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
		return nil, errors.New("invalid private service discovery file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var d service.Discovery
	if json.Unmarshal(data, &d) != nil {
		return nil, errors.New("invalid service discovery JSON")
	}
	api := service.NewClient(d.URL, d.Token)
	if _, err = api.Bots(); err != nil {
		return nil, err
	}
	return api, nil
}

// Execute returns 0 on success, 1 on runtime failure, 2 on usage/validation,
// 3 when approval is needed, 124 on timeout and 130 on cancellation.
func Execute(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) int {
	api, err := Connect()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return ExecuteWithClient(ctx, args, in, out, stderr, api)
}

// ExecuteWithClient permits isolated local-service tests and embedding.
func ExecuteWithClient(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer, api *service.Client) int {
	if !Handles(args) {
		fmt.Fprintln(stderr, "usage: bqbot team apply|export|presets | run | control")
		return 2
	}
	fail := func(err error) int { fmt.Fprintln(stderr, err); return 1 }
	if args[0] == "control" {
		if len(args) != 1 {
			return 2
		}
		if err := control.Serve(ctx, in, out, api); err != nil {
			return fail(err)
		}
		return 0
	}
	if args[0] == "team" {
		if len(args) < 2 {
			return 2
		}
		switch args[1] {
		case "presets":
			if len(args) != 2 {
				return 2
			}
			return writeJSON(out, team.Presets(), stderr)
		case "apply":
			fs := flag.NewFlagSet("team apply", flag.ContinueOnError)
			fs.SetOutput(stderr)
			preset := fs.String("preset", "", "apply a built-in preset by name")
			if fs.Parse(args[2:]) != nil {
				return 2
			}
			var markdown string
			if *preset != "" {
				if fs.NArg() != 0 {
					return 2
				}
				for _, p := range team.Presets() {
					if p.Name == *preset {
						var err error
						markdown, err = team.Format(p)
						if err != nil {
							return fail(err)
						}
					}
				}
				if markdown == "" {
					fmt.Fprintln(stderr, "unknown preset")
					return 2
				}
			} else {
				if fs.NArg() != 1 {
					fmt.Fprintln(stderr, "usage: bqbot team apply FILE|- or --preset NAME")
					return 2
				}
				data, err := readInput(fs.Arg(0), in, team.MaxBytes)
				if err != nil {
					return fail(err)
				}
				markdown = string(data)
			}
			if _, err := team.Parse(markdown); err != nil {
				fmt.Fprintln(stderr, err)
				return 2
			}
			v, err := api.ApplyTeam(markdown)
			if err != nil {
				return fail(err)
			}
			return writeJSON(out, v, stderr)
		case "export":
			fs := flag.NewFlagSet("team export", flag.ContinueOnError)
			fs.SetOutput(stderr)
			name := fs.String("name", "bqbot team", "export name")
			room := fs.String("room", "", "department room ID")
			path := fs.String("output", "-", "new output file, or - for stdout")
			if fs.Parse(args[2:]) != nil || fs.NArg() != 0 {
				return 2
			}
			markdown, err := api.ExportTeam(*name, *room)
			if err != nil {
				return fail(err)
			}
			if *path == "-" {
				_, err = io.WriteString(out, markdown)
			} else {
				err = writeNew(*path, []byte(markdown))
			}
			if err != nil {
				return fail(err)
			}
			return 0
		default:
			return 2
		}
	}
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	bot := fs.String("bot", "", "bot ID or unique name")
	prompt := fs.String("prompt", "", "prompt, or - for stdin")
	asJSON := fs.Bool("json", false, "print a single JSON result")
	export := fs.String("export", "", "write result JSON to a new private file")
	timeout := fs.Duration("timeout", 5*time.Minute, "maximum run duration")
	if fs.Parse(args[1:]) != nil || *bot == "" || *timeout < time.Second || *timeout > 24*time.Hour {
		return 2
	}
	if fs.NArg() > 0 {
		if *prompt != "" {
			return 2
		}
		*prompt = strings.Join(fs.Args(), " ")
	}
	if *prompt == "-" {
		data, err := readInput("-", in, 1<<20)
		if err != nil {
			return fail(err)
		}
		*prompt = string(data)
	}
	if strings.TrimSpace(*prompt) == "" {
		fmt.Fprintln(stderr, "run requires --bot and a prompt")
		return 2
	}
	runCtx, cancel := context.WithTimeout(ctx, *timeout+2*time.Second)
	defer cancel()
	v, err := api.Run(runCtx, service.RunRequest{Bot: *bot, Text: *prompt, TimeoutSeconds: int(timeout.Seconds())})
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return 130
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return 124
		}
		return fail(err)
	}
	if *export != "" {
		data, e := json.MarshalIndent(v, "", "  ")
		if e == nil {
			e = writeNew(*export, append(data, '\n'))
		}
		if e != nil {
			return fail(e)
		}
	}
	if *asJSON {
		if code := writeJSON(out, v, stderr); code != 0 {
			return code
		}
	} else {
		if v.Text != "" {
			fmt.Fprintln(out, v.Text)
		}
		if v.Error != "" {
			fmt.Fprintln(stderr, v.Error)
		}
	}
	switch v.Status {
	case "completed":
		return 0
	case "approval_required":
		return 3
	case "timeout":
		return 124
	case "canceled":
		return 130
	default:
		return 1
	}
}
func writeJSON(out io.Writer, v any, stderr io.Writer) int {
	if err := json.NewEncoder(out).Encode(v); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
func readInput(path string, in io.Reader, limit int64) ([]byte, error) {
	var r io.Reader = in
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	}
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = errors.New("input exceeds size limit")
	}
	return data, err
}
func writeNew(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	err = errors.Join(err, f.Close())
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}
