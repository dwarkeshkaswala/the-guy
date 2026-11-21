// Command guy-desktop is The Guy Desktop control center (Wails v3).
//
// It embeds the exact same Go core as the `guy` CLI: the application service
// (internal/application) delegates to the shared store/scheduler/adapters/
// gitops/review packages. The headless CLI remains fully independent — this
// binary adds a native window, not a second orchestrator.
//
// Lifecycle: runs are owned by this process (same ownership model as
// `guy run`). Closing the window interrupts active agent runs; on the next
// start the scheduler reconciles interrupted runs exactly like a CLI crash
// recovery. No background daemon is spawned and none is claimed.
package main

import (
	"embed"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"

	"github.com/dwarkeshkaswala/the-guy/internal/agents"
	"github.com/dwarkeshkaswala/the-guy/internal/application"
	"github.com/dwarkeshkaswala/the-guy/internal/cli"
	"github.com/dwarkeshkaswala/the-guy/internal/config"
	"github.com/dwarkeshkaswala/the-guy/internal/scheduler"
	"github.com/dwarkeshkaswala/the-guy/internal/store"
	wails "github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

// sink adapts application.Service events to the Wails event manager. It
// resolves the app lazily because the service is constructed before the app.
type sink struct{}

func (sink) Emit(name string, data ...any) {
	if app := wails.Get(); app != nil {
		app.Event.Emit(name, data...)
	}
}

// DesktopService carries the platform integration that has no place in the
// headless application service: native folder picker, revealing paths in the
// OS file manager, and window-level queries.
type DesktopService struct{}

// PickRepositoryFolder opens the platform-native directory picker and returns
// the chosen path ("" when cancelled). Only the folder path crosses the
// boundary; the application service revalidates it as a Git root.
func (d *DesktopService) PickRepositoryFolder() (string, error) {
	app := wails.Get()
	dialog := app.Dialog.OpenFileWithOptions(&wails.OpenFileDialogOptions{
		Title:                "Choose a Git repository",
		CanChooseDirectories: true,
		CanChooseFiles:       false,
		CanCreateDirectories: false,
	})
	return dialog.PromptForSingleSelection()
}

// RevealPath opens the containing folder (selecting the file when the OS
// supports it) in the platform file manager.
func (d *DesktopService) RevealPath(path string) string {
	if path == "" {
		return "nothing to reveal"
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			cmd = exec.Command("explorer", "/select,", path)
		} else {
			cmd = exec.Command("explorer", path)
		}
	case "darwin":
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			cmd = exec.Command("open", "-R", path)
		} else {
			cmd = exec.Command("open", path)
		}
	default:
		cmd = exec.Command("xdg-open", path)
	}
	if err := cmd.Start(); err != nil {
		return "could not open: " + err.Error()
	}
	return ""
}

func (d *DesktopService) AppVersion() string { return cli.Version }

// CloseWarning reports whether closing the window would interrupt active
// agent runs (the UI shows a disclosure before quitting).
func (d *DesktopService) CloseWarning() (int, []string) {
	st, err := store.Open(config.DBPath())
	if err != nil {
		return 0, nil
	}
	defer st.Close()
	runs, err := st.ActiveRuns()
	if err != nil {
		return 0, nil
	}
	var ids []string
	for _, r := range runs {
		ids = append(ids, r.TaskID)
	}
	return len(ids), ids
}

func main() {
	// Hidden internal subcommand: the built-in mock agent (same as the CLI),
	// so the desktop binary is self-sufficient for demos and tests.
	if len(os.Args) > 1 && os.Args[1] == "mock-agent" {
		os.Exit(cli.RunMockAgent(os.Args[2:]))
	}
	// The desktop app takes no arguments; anything else gets a hint instead
	// of silently opening a window (also keeps `--help` honest).
	if len(os.Args) > 1 {
		fmt.Fprintln(os.Stderr, "the-guy desktop "+cli.Version+" — this binary opens the desktop GUI and takes no arguments.")
		fmt.Fprintln(os.Stderr, "For the CLI, run: guy --help")
		os.Exit(2)
	}
	if err := config.EnsureDirs(); err != nil {
		log.Fatalf("initializing the-guy directories: %v", err)
	}
	st, err := store.Open(config.DBPath())
	if err != nil {
		log.Fatalf("opening the-guy state: %v", err)
	}
	defer st.Close()
	agents.SetBuildVersion(cli.Version)

	// Restart recovery: mark runs owned by dead processes interrupted (same
	// semantics as the CLI) and surface what was recovered to the UI.
	recovered := scheduler.Reconcile(st)

	svc := application.New(st, sink{})

	app := wails.New(wails.Options{
		Name:        "The Guy",
		Description: "One guy. Every agent. Any repo.",
		Services: []wails.Service{
			wails.NewService(svc),
			wails.NewService(&DesktopService{}),
		},
		Assets: wails.AssetOptions{
			Handler: wails.AssetFileServerFS(assets),
		},
		SingleInstance: &wails.SingleInstanceOptions{
			UniqueID: "dev.theguy.desktop",
			OnSecondInstanceLaunch: func(_ wails.SecondInstanceData) {
				if w := wails.Get(); w != nil {
					for _, win := range w.Window.GetAll() {
						win.Show()
					}
				}
			},
		},
		Mac: wails.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	app.Window.NewWithOptions(wails.WebviewWindowOptions{
		Title:            "The Guy — One guy. Every agent. Any repo.",
		Width:            1440,
		Height:           900,
		MinWidth:         1024,
		MinHeight:        640,
		BackgroundColour: wails.NewRGB(15, 17, 21),
		URL:              "/",
	})

	stop := make(chan struct{})
	defer close(stop)
	go svc.StartEventStream(stop)
	if len(recovered) > 0 {
		sink{}.Emit("guy:reconciled", map[string]any{"recovered": recovered})
	}

	if err := app.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "the guy desktop exited with an error: %v\n", err)
		os.Exit(1)
	}
}
