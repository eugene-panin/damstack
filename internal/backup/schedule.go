package backup

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Job is a pull that runs by itself, every Every, with Args and Env.
type Job struct {
	Project string
	Args    []string
	Env     map[string]string
	Every   time.Duration
}

// Label names the scheduled pull of a project to launchd and systemd.
func Label(project string) string { return "dev.damstack.backup." + project }

// Plist is the launchd agent of j, its output in log.
func Plist(j Job, log string) []byte {
	var b bytes.Buffer
	esc := func(s string) string {
		var out bytes.Buffer
		xml.EscapeText(&out, []byte(s))
		return out.String()
	}
	b.WriteString(xml.Header)
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	fmt.Fprintf(&b, "\t<key>Label</key>\n\t<string>%s</string>\n", esc(Label(j.Project)))
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, a := range j.Args {
		fmt.Fprintf(&b, "\t\t<string>%s</string>\n", esc(a))
	}
	b.WriteString("\t</array>\n\t<key>EnvironmentVariables</key>\n\t<dict>\n")
	for _, k := range slices.Sorted(maps.Keys(j.Env)) {
		fmt.Fprintf(&b, "\t\t<key>%s</key>\n\t\t<string>%s</string>\n", esc(k), esc(j.Env[k]))
	}
	b.WriteString("\t</dict>\n")
	fmt.Fprintf(&b, "\t<key>StartInterval</key>\n\t<integer>%d</integer>\n", int(j.Every.Seconds()))
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	fmt.Fprintf(&b, "\t<key>StandardOutPath</key>\n\t<string>%s</string>\n", esc(log))
	fmt.Fprintf(&b, "\t<key>StandardErrorPath</key>\n\t<string>%s</string>\n", esc(log))
	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes()
}

// Units are the systemd user service and timer of j.
func Units(j Job) (service, timer []byte) {
	var env strings.Builder
	for _, k := range slices.Sorted(maps.Keys(j.Env)) {
		fmt.Fprintf(&env, "Environment=%s\n", strconv.Quote(k+"="+j.Env[k]))
	}
	args := make([]string, len(j.Args))
	for i, a := range j.Args {
		args[i] = strconv.Quote(a)
	}
	service = fmt.Appendf(nil, "[Unit]\nDescription=Pull the backups of %s to this machine\n\n[Service]\nType=oneshot\n%sExecStart=%s\n",
		j.Project, env.String(), strings.Join(args, " "))
	timer = fmt.Appendf(nil, "[Unit]\nDescription=Pull the backups of %s every %s\n\n[Timer]\nOnBootSec=5min\nOnUnitActiveSec=%ds\nPersistent=true\n\n[Install]\nWantedBy=timers.target\n",
		j.Project, FormatEvery(j.Every), int(j.Every.Seconds()))
	return service, timer
}

// Where says where the scheduled pull of a project writes what it does.
func Where(project string) string {
	if runtime.GOOS == "linux" {
		return "journalctl --user -u " + Label(project)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Logs", "damstack", "backup-"+project+".log")
}

// Install schedules j, or removes the schedule of its project when Every is
// zero. It changes nothing when the schedule is as j says already, and says
// whether it changed anything.
func Install(ctx context.Context, j Job) (bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	label := Label(j.Project)
	switch runtime.GOOS {
	case "darwin":
		plist := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
		domain := "gui/" + strconv.Itoa(os.Getuid())
		if j.Every == 0 {
			if _, err := os.Stat(plist); errors.Is(err, fs.ErrNotExist) {
				return false, nil
			}
			exec.CommandContext(ctx, "launchctl", "bootout", domain+"/"+label).Run()
			return true, os.Remove(plist)
		}
		log := Where(j.Project)
		want := Plist(j, log)
		loaded := exec.CommandContext(ctx, "launchctl", "print", domain+"/"+label).Run() == nil
		if have, err := os.ReadFile(plist); err == nil && bytes.Equal(have, want) && loaded {
			return false, nil
		}
		for _, dir := range []string{filepath.Dir(plist), filepath.Dir(log)} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return false, err
			}
		}
		if err := os.WriteFile(plist, want, 0o644); err != nil {
			return false, err
		}
		exec.CommandContext(ctx, "launchctl", "bootout", domain+"/"+label).Run()
		if out, err := exec.CommandContext(ctx, "launchctl", "bootstrap", domain, plist).CombinedOutput(); err != nil {
			return false, fmt.Errorf("launchctl bootstrap %s: %s", plist, strings.TrimSpace(string(out)))
		}
		return true, nil

	case "linux":
		units := filepath.Join(home, ".config", "systemd", "user")
		service, timer := filepath.Join(units, label+".service"), filepath.Join(units, label+".timer")
		systemctl := func(args ...string) error {
			out, err := exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
			if err != nil {
				return fmt.Errorf("systemctl --user %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
			}
			return nil
		}
		if j.Every == 0 {
			if _, err := os.Stat(timer); errors.Is(err, fs.ErrNotExist) {
				return false, nil
			}
			systemctl("disable", "--now", label+".timer")
			if err := errors.Join(os.Remove(service), os.Remove(timer)); err != nil {
				return true, err
			}
			return true, systemctl("daemon-reload")
		}
		wantService, wantTimer := Units(j)
		haveService, _ := os.ReadFile(service)
		haveTimer, _ := os.ReadFile(timer)
		active := systemctl("is-active", "--quiet", label+".timer") == nil
		if bytes.Equal(haveService, wantService) && bytes.Equal(haveTimer, wantTimer) && active {
			return false, nil
		}
		if err := os.MkdirAll(units, 0o755); err != nil {
			return false, err
		}
		if err := os.WriteFile(service, wantService, 0o644); err != nil {
			return false, err
		}
		if err := os.WriteFile(timer, wantTimer, 0o644); err != nil {
			return false, err
		}
		if err := systemctl("daemon-reload"); err != nil {
			return false, err
		}
		return true, systemctl("enable", "--now", label+".timer")
	}
	if j.Every == 0 {
		return false, nil
	}
	return false, fmt.Errorf("damstack cannot schedule pulls on %s; run damstack backup pull %s by hand", runtime.GOOS, j.Project)
}

// Scheduled is whether the pulls of a project run by themselves.
func Scheduled(ctx context.Context, project string) bool {
	label := Label(project)
	switch runtime.GOOS {
	case "darwin":
		return exec.CommandContext(ctx, "launchctl", "print", "gui/"+strconv.Itoa(os.Getuid())+"/"+label).Run() == nil
	case "linux":
		return exec.CommandContext(ctx, "systemctl", "--user", "is-active", "--quiet", label+".timer").Run() == nil
	}
	return false
}
