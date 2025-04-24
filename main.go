package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	modulev1 "github.com/mfridman/bsr-remotever/gen/buf/registry/module/v1"
	"github.com/mfridman/cli"
	"golang.org/x/sync/errgroup"
)

var (
	DEBUG = os.Getenv("DEBUG") == "1"
)

const (
	authTokenEnv  = "BUF_TOKEN"
	defaultRemote = "buf.build"
)

func main() {
	root := &cli.Command{
		Name:      "bsr-remotever",
		Usage:     "bsr-remotever [command] [flags]",
		ShortHelp: "A glorified and experimental Buf Schema Registry client for Generated SDKs",
		Flags: cli.FlagsFunc(func(f *flag.FlagSet) {
			f.String("remote", defaultRemote, "The remote to use")
		}),
		SubCommands: []*cli.Command{
			sdk,
		},
		Exec: func(ctx context.Context, s *cli.State) error {
			return errors.New("must specify a subcommand, see 'bsr-remotever --help'")
		},
	}
	if err := cli.Parse(root, os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(os.Stdout, "%s\n", cli.DefaultUsage(root))
			return
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if err := cli.Run(context.Background(), root, nil); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

var (
	sdk = &cli.Command{
		Name:      "sdk",
		Usage:     "bsr-remotever sdk [command] [flags]",
		ShortHelp: "Commands for doing extra stuff with the Generated SDKs",
		SubCommands: []*cli.Command{
			sdkResolve,
		},
		Exec: func(ctx context.Context, s *cli.State) error {
			return errors.New("must specify a subcommand, see 'bsr-remotever sdk --help'")
		},
	}
	sdkResolve = &cli.Command{
		Name:      "resolve",
		Usage:     "bsr-remotever sdk resolve [flags]",
		ShortHelp: "Resolve an arbitrary version to a well-known module and plugin",
		Exec: func(ctx context.Context, s *cli.State) error {
			var (
				remote = cli.GetFlag[string](s, "remote")
			)

			info, err := parse(remote, strings.Fields(strings.Join(s.Args, " ")))
			if err != nil {
				return fmt.Errorf("failed to parse command: %w", err)
			}

			// This is wild, this should be fixed. Should be _much_ simpler to resolve a short
			// commit name to a full commit name.
			client := newClient(remote)
			listResp, err := client.module.LabelService.ListLabels(ctx, &modulev1.ListLabelsRequest{
				ResourceRef: &modulev1.ResourceRef{
					Value: &modulev1.ResourceRef_Name_{
						Name: &modulev1.ResourceRef_Name{
							Owner:  info.ModuleOwner,
							Module: info.ModuleName,
						},
					},
				},
				ArchiveFilter: modulev1.ListLabelsRequest_ARCHIVE_FILTER_UNARCHIVED_ONLY,
				PageSize:      250,
				Order:         modulev1.ListLabelsRequest_ORDER_CREATE_TIME_DESC,
			})
			if err != nil {
				return fmt.Errorf("failed to list labels: %w", err)
			}
			var mu sync.Mutex
			var foundCommitName string
			var g errgroup.Group
			for _, label := range listResp.GetLabels() {
				// Sorry BSR :(
				g.Go(func() error {
					commits, err := client.module.LabelService.ListLabelHistory(ctx, &modulev1.ListLabelHistoryRequest{
						PageSize: 250,
						LabelRef: &modulev1.LabelRef{
							Value: &modulev1.LabelRef_Id{Id: label.GetId()},
						},
						Order: modulev1.ListLabelHistoryRequest_ORDER_DESC,
					})
					if err != nil {
						return fmt.Errorf("failed to list label history: %w", err)
					}
					for _, commit := range commits.GetValues() {
						if strings.HasPrefix(commit.GetCommit().GetId(), info.ModuleShort) {
							mu.Lock()
							defer mu.Unlock()
							foundCommitName = commit.GetCommit().GetId()
							return nil
						}
					}
					return nil
				})
			}
			if err := g.Wait(); err != nil {
				return fmt.Errorf("failed to list label history: %w", err)
			}
			if foundCommitName == "" {
				return fmt.Errorf("could not find a commit name for %q", info.ModuleShort)
			}
			printFinalOutput(info, foundCommitName)

			return nil
		},
	}
)

type registryType int

const (
	registryTypeNPM registryType = iota + 1
	registryTypeGo
)

func (r registryType) String() string {
	switch r {
	case registryTypeNPM:
		return "npm"
	case registryTypeGo:
		return "go"
	default:
		return "unknown"
	}
}

func ensurePrefix(s, prefix string) string {
	if strings.HasPrefix(s, prefix) {
		return s
	}
	return prefix + s
}

var (
	npmScopedPattern = regexp.MustCompile(`^@[\w-]+\/[a-z0-9_-]+\.[a-z0-9_-]+$`)
)

func determineRegistry(name string) registryType {
	switch {
	case (strings.Contains(name, "@buf/") || strings.Contains(name, "@bufteam/")) && npmScopedPattern.MatchString(name):
		return registryTypeNPM
	case strings.Contains(name, "/gen/go/"):
		// Easy peasy way to check for Go packages!
		return registryTypeGo
	default:
		return 0 // unknown
	}
}

type PackageInfo struct {
	Remote         string
	RegistryType   registryType
	RawPackageName string
	RawVersion     string

	// Module
	ModuleOwner, ModuleName, ModuleShort string

	// Plugin
	PluginOwner, PluginName       string
	PluginVersion, PluginRevision string
}

func parse(remote string, args []string) (*PackageInfo, error) {
	if len(args) != 2 {
		return nil, fmt.Errorf("invalid input: expected 2 arguments, got %d in %q", len(args), args)
	}
	packageName, version := args[0], args[1]
	registry := determineRegistry(packageName)
	if registry == 0 {
		return nil, fmt.Errorf("could not determine registry from package name: %q", packageName)
	}
	pluginVersion, pluginRevision, moduleShort := parseVersion(version)

	info := &PackageInfo{
		Remote:         remote,
		RegistryType:   registry,
		RawPackageName: packageName,
		RawVersion:     version,
		PluginVersion:  ensurePrefix(pluginVersion, "v"),
		PluginRevision: pluginRevision,
		ModuleShort:    moduleShort,
	}

	switch registry {
	case registryTypeGo:
		segments := strings.Split(packageName, "/")
		if len(segments) >= 7 {
			info.ModuleOwner = segments[3]
			info.ModuleName = segments[4]
			info.PluginOwner = segments[5]
			info.PluginName = segments[6]
		}

	case registryTypeNPM:
		clean := strings.TrimPrefix(packageName, "@buf/")
		clean = strings.TrimPrefix(clean, "@bufteam/")
		parts := strings.Split(clean, ".")
		if len(parts) == 2 {
			moduleParts := strings.Split(parts[0], "_")
			pluginParts := strings.Split(parts[1], "_")

			if len(moduleParts) == 2 && len(pluginParts) == 2 {
				info.ModuleOwner = moduleParts[0]
				info.ModuleName = moduleParts[1]
				info.PluginOwner = pluginParts[0]
				info.PluginName = pluginParts[1]
			}
		}
	}
	if info.ModuleOwner == "" || info.ModuleName == "" || info.PluginOwner == "" || info.PluginName == "" {
		return nil, fmt.Errorf("could not parse package name: %q", packageName)
	}

	return info, nil
}

func parseVersion(version string) (pluginVersion, pluginRevision, moduleShort string) {
	lastDot := strings.LastIndex(version, ".")
	if lastDot == -1 || lastDot == len(version)-1 {
		return version, "", ""
	}

	pluginRevision = version[lastDot+1:]
	versionPart := version[:lastDot]

	dashParts := strings.Split(versionPart, "-")
	if len(dashParts) < 3 {
		pluginVersion = versionPart
		return
	}

	pluginVersion = dashParts[0]
	moduleShort = dashParts[2]
	return
}

type client struct {
	module *modulev1.Client
}

func newClient(remote string) *client {
	token := os.Getenv(authTokenEnv)
	if token == "" {
		var err error
		token, err = parseNetrc(remote)
		if err != nil && DEBUG {
			log.Println("failed to parse .netrc:", err)
		}
	}
	return &client{
		module: modulev1.NewClient(prependHTTPS(remote), modulev1.WithModifyRequest(func(r *http.Request) error {
			r.Header.Set("Authorization", "Bearer "+token)
			return nil
		})),
	}
}

func prependHTTPS(url string) string {
	url = strings.TrimPrefix(url, "https://")
	url = strings.TrimPrefix(url, "http://")
	return "https://" + url
}

func printFinalOutput(info *PackageInfo, commitName string) {
	header := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("36"))
	label := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Width(12).Align(lipgloss.Right)
	value := lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	divider := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(strings.Repeat("─", 40))

	moduleURL := fmt.Sprintf("https://%s/%s/%s/docs/%s",
		info.Remote, info.ModuleOwner, info.ModuleName, commitName,
	)
	pluginURL := fmt.Sprintf("https://%s/%s/%s?version=%s",
		info.Remote, info.PluginOwner, info.PluginName, info.PluginVersion,
	)
	homepageURL := fmt.Sprintf("https://%s/%s/%s/sdks/%s:%s/%s?version=%s",
		info.Remote, info.ModuleOwner, info.ModuleName, commitName, info.PluginOwner, info.PluginName, info.PluginVersion,
	)

	fmt.Println(header.Render("Package Info"))
	fmt.Println(divider)
	fmt.Println(label.Render("Registry:  ") + value.Render(info.RegistryType.String()))
	fmt.Println(label.Render("Package:   ") + value.Render(info.RawPackageName))
	fmt.Println(label.Render("Version:   ") + value.Render(info.RawVersion))

	// Add spinner here to make it looks super faaaaaaancty! Mainly just messing around with all the
	// charm stuff
	runSpinner()

	fmt.Println()
	fmt.Println(header.Render("Resolved to the following"))
	fmt.Println(divider)
	fmt.Println(label.Render("Module:   ") + value.Render(moduleURL))
	fmt.Println(label.Render("Plugin:   ") + value.Render(pluginURL))
	fmt.Println(label.Render("Homepage: ") + value.Render(homepageURL))
	fmt.Println()
}

type model struct {
	spinner  spinner.Model
	quitting bool
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, tea.Tick(time.Second*2, func(t time.Time) tea.Msg {
		return quitMsg{}
	}))
}

type quitMsg struct{}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case quitMsg:
		m.quitting = true
		return m, tea.Quit
	case tea.KeyMsg:
		if msg.String() == "q" {
			return m, tea.Quit
		}
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m model) View() string {
	if m.quitting {
		return ""
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("Loading... ") + m.spinner.View()
}

func runSpinner() {
	s := spinner.New()
	s.Spinner = spinner.Dot
	p := tea.NewProgram(model{spinner: s})
	if _, err := p.Run(); err != nil {
		fmt.Println("could not start spinner:", err)
		os.Exit(1)
	}
}

func parseNetrc(machineName string) (string, error) {
	dir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	r, err := os.Open(filepath.Join(dir, ".netrc"))
	if err != nil {
		return "", err
	}
	sc := bufio.NewScanner(r)
	var gotcha bool
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "machine" && len(fields) > 1 && fields[1] == machineName {
			gotcha = true
			continue
		}
		if gotcha {
			if fields[0] == "password" && len(fields) > 1 {
				return fields[1], nil
			}
			if fields[0] == "machine" {
				// Reached the end of the machine block without finding a password.
				break
			}
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("no password found for %s", machineName)
}
