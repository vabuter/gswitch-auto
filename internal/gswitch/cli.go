package gswitch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/arumata/gswitch/internal/gswitch/focus"
	"github.com/arumata/gswitch/internal/gswitch/layoutstate"
	gsruntime "github.com/arumata/gswitch/internal/gswitch/runtime"
)

var version = "dev"

// Execute runs gswitch CLI commands and exits with appropriate status codes.
func Execute(args []string, buildVersion string) {
	version = buildVersion
	gsruntime.SetVersion(buildVersion)

	if len(args) < 2 {
		runHelp(version)
		return
	}

	switch args[1] {
	case "-c", "--configure":
		runConfigure()
	case "-r", "--run":
		run(true)
	case "-d", "--debug":
		run(false)
	case "--write-config":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: --write-config 'key=value,...'")
			os.Exit(1)
		}
		if err := writeConfigFromArgs(args[2]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "-v", "--version":
		fmt.Printf("gswitch version %s\n", version)
	case "--test-session-env":
		runTestSessionEnv()
	case "--detect-layout-switch":
		runDetectLayoutSwitch(args[2:])
	case "--auto-check":
		os.Exit(gsruntime.RunAutoCheck(args[2:], os.Stdout, os.Stderr))
	case "--layout-state":
		os.Exit(runLayoutState(args[2:], os.Stdout, os.Stderr))
	case "--focus-state":
		os.Exit(runFocusState(args[2:], os.Stdout, os.Stderr))
	case "-h", "--help":
		runHelp(version)
	default:
		runHelp(version)
	}
}

// runTestSessionEnv tests GetActiveSessionEnv functionality.
func runTestSessionEnv() {
	fmt.Println("Testing GetActiveSessionEnv...")
	fmt.Printf("Running as UID: %d\n", os.Getuid())

	env, err := GetActiveSessionEnv()
	if err != nil {
		switch {
		case errors.Is(err, ErrNoSystemd):
			fmt.Println("Result: ErrNoSystemd - loginctl not available")
			fmt.Println("This is expected on non-systemd systems")
		case errors.Is(err, ErrNoActiveSession):
			fmt.Println("Result: ErrNoActiveSession - no active graphical session")
			fmt.Println("This is expected when no user is logged in")
		default:
			fmt.Printf("Error: %v\n", err)
		}
		return
	}

	fmt.Println("Session found:")
	fmt.Printf("  UID:           %d\n", env.UID)
	fmt.Printf("  GID:           %d\n", env.GID)
	fmt.Printf("  User:          %s\n", env.User)
	fmt.Printf("  Home:          %s\n", env.Home)
	fmt.Printf("  SessionType:   %s\n", env.SessionType)
	fmt.Printf("  Display:       %s\n", env.Display)
	fmt.Printf("  XAuthority:    %s\n", env.XAuthority)
	fmt.Printf("  DBusAddress:   %s\n", env.DBusAddress)
	fmt.Printf("  RuntimeDir:    %s\n", env.RuntimeDir)
	fmt.Printf("  XDGConfigHome: %s\n", env.XDGConfigHome)
	fmt.Printf("  IBusAddress:   %s\n", env.IBusAddress)
}

func runHelp(version string) {
	fmt.Printf("gswitch - keyboard layout switcher %s\n", version)
	fmt.Println()
	fmt.Println("Usage: gswitch [option]")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("   -c,   --configure          configure gswitch")
	fmt.Println("   -r,   --run                run")
	fmt.Println("   -d,   --debug              run in a debug mode")
	fmt.Println("         --write-config       write config (intended for pkexec)")
	fmt.Println("   -v,   --version            show version")
	fmt.Println("   -h,   --help               show this help")
	fmt.Println()
	fmt.Println("Detection:")
	fmt.Println("   --detect-layout-switch           detect layout switch keys (JSON output)")
	fmt.Println("         --source=SOURCE            use specific provider (xkb, gnome, kde)")
	fmt.Println()
	fmt.Println("Automatic correction (gswitch-auto):")
	fmt.Println("   --auto-check TEXT [--shown LAYOUT]   judge a word as typed in LAYOUT (JSON output)")
	fmt.Println("   --layout-state [--watch SECONDS]     show the layout source the daemon would use")
	fmt.Println("                  [--source auto|ibus|x11]  (IBus engine or X11 XKB group), its state,")
	fmt.Println("                                        and follow changes for SECONDS (default 10,")
	fmt.Println("                                        0 = no watch); --source forces one source")
	fmt.Println("   --focus-state [--watch SECONDS]      show the focus sources (Shell extension, X11")
	fmt.Println("                                        active window), the merged state and the")
	fmt.Println("                                        policy decision; follow changes for SECONDS")
}

// defaultDiagWatch is how long --layout-state and --focus-state follow
// changes when --watch is not given.
const defaultDiagWatch = 10 * time.Second

// Values of --layout-state --source.
const (
	diagSourceAuto = "auto"
	diagSourceIBus = layoutstate.SourceIBus
	diagSourceX11  = layoutstate.SourceX11
)

// diagArgs are the parsed arguments of --layout-state and --focus-state.
type diagArgs struct {
	watch  time.Duration
	source string // --layout-state only: diagSourceAuto (default), diagSourceIBus or diagSourceX11
}

// runLayoutState implements --layout-state [--watch SECONDS] [--source
// auto|ibus|x11]: read-only layout diagnostics that need neither root nor
// input devices. It prints the session, the GNOME Shell version and the
// source selection the daemon would make (SPEC-X11 §2.1), then runs the
// X11 and/or IBus diagnostics of the selected source (--source forces one).
// It returns the process exit code: 1 when the selected source cannot be
// reached or no source exists for this session.
func runLayoutState(args []string, stdout, stderr io.Writer) int {
	opts, err := parseDiagArgs(args, true)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\nusage: gswitch --layout-state [--watch SECONDS] [--source auto|ibus|x11]\n", err)
		return 1
	}
	layouts, err := layoutStatePair()
	if err != nil {
		fmt.Fprintf(stderr, "Warning: %v\n", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	sel, err := gsruntime.PlatformForDiagnostics(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "Warning: %v\n", err)
	}
	printPlatform(stdout, sel, stderr)
	runX11, runIBus, note := layoutStateSources(sel, opts.source)
	if note != "" {
		fmt.Fprintln(stdout, note)
	}
	if !runX11 && !runIBus {
		fmt.Fprintf(stderr, "Error: no layout source for this session (%s)\n", sel.LayoutReason)
		return 1
	}
	code := 0
	if runX11 {
		if err := layoutstate.RunX11Diagnostics(ctx, stdout, "", layouts, opts.watch); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			code = 1
		}
	}
	if runIBus {
		if err := layoutstate.RunDiagnostics(ctx, stdout, layoutstate.AddressEnvFromProcess(), layouts, opts.watch); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			code = 1
		}
	}
	return code
}

// layoutStateSources decides which diagnostics --layout-state runs: the
// daemon's selection, or the source forced with --source (with a note when
// the daemon would not use it).
func layoutStateSources(sel gsruntime.PlatformSelection, source string) (runX11, runIBus bool, note string) {
	switch source {
	case diagSourceX11:
		switch {
		case sel.LayoutX11:
		case sel.Wayland:
			note = "Note: X11 layout source forced in a Wayland session; Xwayland never sees GNOME's layout changes"
		default:
			note = "Note: X11 layout source forced; the daemon would not use it in this session"
		}
		return true, false, note
	case diagSourceIBus:
		switch {
		case sel.LayoutIBus:
		case sel.Shell.Version != "" && !layoutstate.IBusEngineTracksLayout(sel.Shell.Version):
			note = fmt.Sprintf("Note: IBus layout source disabled for GNOME Shell %s (it reports a fixed engine for XKB sources; needs 49.3+); forced for this run",
				sel.Shell.Version)
		default:
			note = "Note: IBus layout source forced; the daemon would not use it in this session"
		}
		return false, true, note
	default:
		if sel.LayoutIBusGuarded {
			note = fmt.Sprintf("Note: IBus layout source guarded: GNOME Shell version unknown (%v); the daemon keeps probing", sel.Shell.Err)
		}
		return sel.LayoutX11, sel.LayoutIBus, note
	}
}

// printPlatform prints the session facts and the daemon's source selection
// (with the effective strict-purpose value of ~/.config/gswitch) and
// returns the focus policy; a configuration problem goes to stderr.
func printPlatform(w io.Writer, sel gsruntime.PlatformSelection, stderr io.Writer) focus.Policy {
	policy, err := gsruntime.FocusPolicyForDiagnostics(sel.Wayland)
	if err != nil {
		fmt.Fprintf(stderr, "Warning: %v\n", err)
	}
	fmt.Fprintf(w, "Session: %s (DISPLAY=%q, WAYLAND_DISPLAY=%q, XDG_SESSION_TYPE=%q)\n",
		sel.SessionName(), os.Getenv("DISPLAY"), os.Getenv("WAYLAND_DISPLAY"), os.Getenv("XDG_SESSION_TYPE"))
	desktop := "other"
	if sel.GNOME {
		desktop = "GNOME"
	}
	fmt.Fprintf(w, "Desktop: %s\n", desktop)
	switch {
	case sel.Shell.Err != nil:
		fmt.Fprintf(w, "GNOME Shell: unavailable (%v)\n", sel.Shell.Err)
	case sel.Shell.Version != "":
		fmt.Fprintf(w, "GNOME Shell: %s\n", sel.Shell.Version)
	default:
		fmt.Fprintf(w, "GNOME Shell: not probed\n")
	}
	fmt.Fprintf(w, "Daemon selection: %s\n", sel.Summary(policy.RequirePurpose))
	for _, warning := range sel.Warnings {
		fmt.Fprintf(w, "Warning: %s\n", warning)
	}
	return policy
}

// runFocusState implements --focus-state [--watch SECONDS]: read-only focus
// diagnostics with the sources the daemon would start (SPEC-X11 §2.2) and the
// focus policy of ~/.config/gswitch. It returns the process exit code.
func runFocusState(args []string, stdout, stderr io.Writer) int {
	opts, err := parseDiagArgs(args, false)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\nusage: gswitch --focus-state [--watch SECONDS]\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	sel, err := gsruntime.PlatformForDiagnostics(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "Warning: %v\n", err)
	}
	policy := printPlatform(stdout, sel, stderr)
	err = focus.RunDiagnostics(ctx, stdout, focus.DiagOptions{
		ExtensionEnabled: sel.FocusExt,
		X11Enabled:       sel.FocusX11,
		Policy:           policy,
		Watch:            opts.watch,
	})
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	return 0
}

// parseDiagArgs parses "--watch SECONDS" / "--watch=SECONDS" and, when
// allowSource is true, "--source NAME" / "--source=NAME" (auto, ibus, x11).
func parseDiagArgs(args []string, allowSource bool) (diagArgs, error) {
	opts := diagArgs{watch: defaultDiagWatch, source: diagSourceAuto}
	for i := 0; i < len(args); i++ {
		name, value, err := diagArgValue(args, &i)
		if err != nil {
			return opts, err
		}
		switch name {
		case "--watch":
			seconds, err := strconv.ParseFloat(value, 64)
			if err != nil || seconds < 0 || seconds > 24*60*60 {
				return opts, fmt.Errorf("invalid --watch value %q", value)
			}
			opts.watch = time.Duration(seconds * float64(time.Second))
		case "--source":
			if !allowSource {
				return opts, errors.New("unknown argument \"--source\"")
			}
			switch value {
			case diagSourceAuto, diagSourceIBus, diagSourceX11:
				opts.source = value
			default:
				return opts, fmt.Errorf("invalid --source value %q (auto, ibus, x11)", value)
			}
		}
	}
	return opts, nil
}

// diagArgValue reads the option at args[*i] as "--name=value" or "--name
// value" (advancing *i) for the options --watch and --source.
func diagArgValue(args []string, i *int) (name, value string, err error) {
	arg := args[*i]
	for _, opt := range []string{"--watch", "--source"} {
		if v, ok := strings.CutPrefix(arg, opt+"="); ok {
			return opt, v, nil
		}
		if arg != opt {
			continue
		}
		if *i+1 >= len(args) {
			return "", "", fmt.Errorf("%s needs a value", opt)
		}
		*i++
		return opt, args[*i], nil
	}
	return "", "", fmt.Errorf("unknown argument %q", arg)
}

// layoutStatePair returns the layout pair as the daemon resolves it:
// layout1/layout2 from the system config, else the desktop's layouts.
func layoutStatePair() ([2]layoutstate.Layout, error) {
	var pair [2]layoutstate.Layout
	var layouts []LayoutSpec
	if c, err := LoadConfig(); err == nil && len(c.Layouts) == 2 {
		layouts = c.Layouts
	} else {
		detected, err := gsruntime.GetCurrentLayouts(nil)
		if err != nil {
			return pair, fmt.Errorf("layout pair unknown: %w", err)
		}
		layouts = detected
	}
	if len(layouts) != 2 {
		return pair, fmt.Errorf("layout pair unknown: detected %d layouts %v (set layout1/layout2)", len(layouts), layouts)
	}
	for i, l := range layouts {
		pair[i] = layoutstate.Layout{Name: l.Name, Variant: l.Variant}
	}
	return pair, nil
}

// runDetectLayoutSwitch handles the --detect-layout-switch command.
// Always outputs JSON to stdout (per task spec: "JSON only").
// Parses optional flag: --source=SOURCE.
func runDetectLayoutSwitch(args []string) {
	var source string
	for _, arg := range args {
		if val, ok := strings.CutPrefix(arg, "--source="); ok {
			source = val
			if source == "" {
				outputJSONError("--source requires a value (xkb, gnome, kde)")
			}
			if !isValidDetectionSource(DetectionSource(source)) {
				outputJSONError("unknown source: " + source + " (valid: xkb, gnome, kde)")
			}
		} else {
			outputJSONError("unknown flag: " + arg)
		}
	}

	var opts *DetectionOptions
	if source != "" {
		opts = &DetectionOptions{SourceOverride: DetectionSource(source)}
	}

	result, err := DetectLayoutSwitchKeys(opts)
	runDetectJSONOutput(result, err)
}

// outputJSONError outputs an error in JSON format and exits with code 2.
func outputJSONError(errMsg string) {
	output := &DetectJSONOutput{
		Schema:   1,
		Status:   "error",
		Error:    errMsg,
		Attempts: []DetectJSONAttempt{},
	}
	//nolint:errcheck // Best effort output before exit
	json.NewEncoder(os.Stdout).Encode(output)
	os.Exit(2)
}

// runDetectJSONOutput outputs detection result as JSON to stdout.
func runDetectJSONOutput(result *DetectionResult, err error) {
	output := BuildDetectJSONOutput(result, err)

	if encErr := json.NewEncoder(os.Stdout).Encode(output); encErr != nil {
		fmt.Fprintf(os.Stderr, "Failed to encode JSON: %v\n", encErr)
		os.Exit(2)
	}

	switch output.Status {
	case "found":
		os.Exit(0)
	case "not_found":
		os.Exit(1)
	default:
		os.Exit(2)
	}
}

func isValidDetectionSource(source DetectionSource) bool {
	switch source {
	case SourceXKB, SourceGNOME, SourceKDE:
		return true
	default:
		return false
	}
}
