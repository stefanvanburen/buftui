package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"time"

	"buf.build/gen/go/bufbuild/registry/connectrpc/go/buf/registry/module/v1beta1/modulev1beta1connect"
	modulev1beta1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/module/v1beta1"
	ownerv1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/owner/v1"
	"connectrpc.com/connect"
	"github.com/bufbuild/httplb"
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jdx/go-netrc"
	"github.com/peterbourgon/ff/v4"
	"github.com/peterbourgon/ff/v4/ffhelp"
)

// TODO:
// * search for user / organization
// * module view
// * commit view
// * docs view?

type modelState string

const (
	modelStateLoadingModules             modelState = "loading-modules"
	modelStateBrowsingModules            modelState = "browsing-modules"
	modelStateLoadingCommits             modelState = "loading-commits"
	modelStateBrowsingCommits            modelState = "browsing-commits"
	modelStateLoadingCommitContents      modelState = "loading-commit-contents"
	modelStateBrowsingCommitContents     modelState = "browsing-commit-contents"
	modelStateBrowsingCommitFileContents modelState = "browsing-commit-file-contents"
	modelStateSearching                  modelState = "searching"
)

type model struct {
	state modelState

	spinner spinner.Model

	// TODO: Share a single table and just hold on to the messages and
	// re-render?
	moduleTable        table.Model
	noOwnerModules     bool
	commitsTable       table.Model
	noModuleCommits    bool
	commitFilesTable   table.Model
	currentModule      string
	currentCommit      string
	currentCommitFiles []*modulev1beta1.File
	fileViewport       viewport.Model
	searchInput        textinput.Model
	help               help.Model

	keys keyMap

	hostname    string
	login       string
	token       string
	moduleOwner string

	err error

	tableStyles table.Styles

	httpClient connect.HTTPClient
}

const (
	bufBlue = lipgloss.Color("#151fd5")
	bufTeal = lipgloss.Color("#91dffb")
)

// keyMap defines a set of keybindings. To work for help it must satisfy
// key.Map. It could also very easily be a map[string]key.Binding.
type keyMap struct {
	Up     key.Binding
	Down   key.Binding
	Left   key.Binding
	Right  key.Binding
	Search key.Binding
	Enter  key.Binding
	Help   key.Binding
	Quit   key.Binding
}

func (m model) ShortHelp() []key.Binding {
	var shortHelp []key.Binding
	switch m.state {
	case modelStateBrowsingModules:
		// Can't go Left while browsing modules; already at the "top".
		if m.noOwnerModules {
			// Can't go Right when no modules exist.
			shortHelp = []key.Binding{keys.Up, keys.Down}
		} else {
			shortHelp = []key.Binding{keys.Up, keys.Down, keys.Right}
		}
	case modelStateBrowsingCommits, modelStateBrowsingCommitContents:
		if m.noModuleCommits {
			// Can't go Right when no commits exist.
			shortHelp = []key.Binding{keys.Up, keys.Down, keys.Left}
		} else {
			shortHelp = []key.Binding{keys.Up, keys.Down, keys.Right, keys.Left}
		}
	case modelStateBrowsingCommitFileContents:
		// Can't go Right while browsing file contents; already at the "bottom".
		shortHelp = []key.Binding{keys.Up, keys.Down, keys.Left}
	case modelStateSearching:
		shortHelp = []key.Binding{keys.Enter}
	default:
		// In the other states, just show Help and Quit.
		return []key.Binding{keys.Help, keys.Quit}
	}
	// Always show the Help key.
	return append(shortHelp, keys.Help)
}

func (m model) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		m.ShortHelp(),
		{keys.Search, keys.Help, keys.Quit},
	}
}

var keys = keyMap{
	// TODO: Ideally we'd pull these from the viewing model KeyMap defaults
	// (e.g. m.fileViewport.KeyMap); for now just give some basics.
	Up: key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "move up"),
	),
	Down: key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "move down"),
	),
	Left: key.NewBinding(
		key.WithKeys("left", "h"),
		key.WithHelp("←/h", "go out"),
	),
	Right: key.NewBinding(
		key.WithKeys("right", "l"),
		key.WithHelp("→/l", "go in"),
	),
	Enter: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "search"),
	),
	Search: key.NewBinding(
		key.WithKeys("s"),
		key.WithHelp("s", "search for owner"),
	),
	Help: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "toggle help"),
	),
	Quit: key.NewBinding(
		key.WithKeys("q", "esc", "ctrl+c"),
		key.WithHelp("q", "quit"),
	),
}

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Printf("error: %v\n", err)
		os.Exit(1)
	}
}

func run(_ context.Context) error {
	fs := ff.NewFlagSet("buftui")
	var (
		hostFlag       = fs.String('r', "registry", "buf.build", "BSR registry")
		fullscreenFlag = fs.Bool('f', "fullscreen", "Enable fullscreen display")
	)

	if err := ff.Parse(fs, os.Args[1:]); err != nil {
		fmt.Printf("%s\n", ffhelp.Flags(fs))
		return err
	}

	usr, err := user.Current()
	if err != nil {
		return fmt.Errorf("getting current user: %s", err)
	}
	n, err := netrc.Parse(filepath.Join(usr.HomeDir, ".netrc"))
	if err != nil {
		return fmt.Errorf("parsing netrc: %s", err)
	}
	// TODO: What should we do with neither set, or just one set?
	login := n.Machine(*hostFlag).Get("login")
	token := n.Machine(*hostFlag).Get("password")
	moduleOwner := login

	tableStyles := table.DefaultStyles()
	tableStyles.Header = tableStyles.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(bufBlue).
		BorderBottom(true).
		Bold(false)
	tableStyles.Selected = tableStyles.Selected.
		Foreground(bufTeal).
		Background(bufBlue).
		Bold(false)

	httpClient := httplb.NewClient()
	defer httpClient.Close()

	model := model{
		state:       modelStateLoadingModules,
		hostname:    *hostFlag,
		login:       login,
		token:       token,
		moduleOwner: moduleOwner,
		spinner:     spinner.New(),
		tableStyles: tableStyles,
		httpClient:  httpClient,
		help:        help.New(),
		keys:        keys,
	}

	var options []tea.ProgramOption
	if *fullscreenFlag {
		options = append(options, tea.WithAltScreen())
	}
	if _, err := tea.NewProgram(model, options...).Run(); err != nil {
		return err
	}
	return nil
}

func (m model) Init() tea.Cmd {
	return m.getModules()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// If we set a width on the help menu it can gracefully truncate
		// its view as needed.
		m.help.Width = msg.Width

	case modulesMsg:
		m.state = modelStateBrowsingModules
		if len(msg) == 0 {
			m.noOwnerModules = true
			return m, nil
		}
		// Reset.
		m.noOwnerModules = false
		columns := []table.Column{
			// TODO: adjust these dynamically?
			// NOTE: It seems like module.{Description,Url} are not
			// currently widely populated; leaving those out
			// deliberately.
			{Title: "ID", Width: 12},
			{Title: "Name", Width: 20},
			{Title: "Create Time", Width: 19},
			{Title: "Visibility", Width: 10},
			{Title: "State", Width: 10},
		}
		tableHeight := len(msg)
		rows := make([]table.Row, len(msg))
		for i, module := range msg {
			var visibility string
			switch module.Visibility {
			case modulev1beta1.ModuleVisibility_MODULE_VISIBILITY_PRIVATE:
				visibility = "private"
			case modulev1beta1.ModuleVisibility_MODULE_VISIBILITY_PUBLIC:
				visibility = "public"
			default:
				visibility = "unknown"
			}
			var state string
			switch module.State {
			case modulev1beta1.ModuleState_MODULE_STATE_ACTIVE:
				state = "active"
			case modulev1beta1.ModuleState_MODULE_STATE_DEPRECATED:
				state = "deprecated"
			default:
				state = "unknown"
			}
			rows[i] = table.Row{
				module.Id,
				module.Name,
				module.CreateTime.AsTime().Format(time.DateTime),
				visibility,
				state,
			}
		}
		m.moduleTable = table.New(
			table.WithColumns(columns),
			table.WithRows(rows),
			table.WithFocused(true),
			table.WithHeight(tableHeight),
			table.WithStyles(m.tableStyles),
		)
		return m, nil

	case commitsMsg:
		m.state = modelStateBrowsingCommits
		if len(msg) == 0 {
			m.noModuleCommits = true
			return m, nil
		}
		// Reset.
		m.noModuleCommits = false

		columns := []table.Column{
			// TODO: adjust these dynamically?
			{Title: "ID", Width: 12},
			{Title: "Create Time", Width: 19},
			// TODO: What makes sense?
			{Title: "Digest", Width: 19},
			// TODO: What else is useful here?
		}
		rows := make([]table.Row, len(msg))
		for i, commit := range msg {
			rows[i] = table.Row{
				commit.Id,
				commit.CreateTime.AsTime().Format(time.DateTime),
				fmt.Sprintf("%s:%s", commit.Digest.Type.String(), commit.Digest.Value),
			}
		}
		m.commitsTable = table.New(
			table.WithColumns(columns),
			table.WithRows(rows),
			table.WithFocused(true),
			table.WithHeight(len(msg)),
			table.WithStyles(m.tableStyles),
		)
		return m, nil

	case contentsMsg:
		// TODO: This is a stupid way to display files - eventually, improve it.
		columns := []table.Column{
			{Title: "Path", Width: 50},
		}
		rows := make([]table.Row, len(msg.Files))
		// TODO: Cap this at something sane based on the terminal size?
		tableHeight := len(msg.Files)
		// This is probably not even possible?
		if len(msg.Files) == 0 {
			rows = append(rows, table.Row{
				"No files found for commit",
			})
			tableHeight = 1
		} else {
			for i, file := range msg.Files {
				rows[i] = table.Row{file.Path}
			}
		}
		m.commitFilesTable = table.New(
			table.WithColumns(columns),
			table.WithRows(rows),
			table.WithFocused(true),
			table.WithHeight(tableHeight),
			table.WithStyles(m.tableStyles),
		)
		m.state = modelStateBrowsingCommitContents
		m.currentCommitFiles = msg.Files
		m.fileViewport = viewport.New(100, max(tableHeight, 30))
		for _, file := range m.currentCommitFiles {
			if file.Path == m.commitFilesTable.SelectedRow()[0] {
				m.fileViewport.SetContent(string(file.Content))
			}
		}
		return m, nil

	case errMsg:
		m.err = msg.err
		return m, nil

	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, m.keys.Help):
			m.help.ShowAll = !m.help.ShowAll
		case key.Matches(msg, m.keys.Search):
			// From anywhere other than the search state, "s"
			// enters a search state.
			if m.state != modelStateSearching {
				// "s" -> "search"
				m.state = modelStateSearching
				searchInput := textinput.New()
				// Style the input.
				// TODO: This is probably too much.
				searchStyle := lipgloss.NewStyle().Foreground(bufBlue).Background(bufTeal)
				searchInput.PromptStyle = searchStyle
				searchInput.PlaceholderStyle = searchStyle
				searchInput.TextStyle = searchStyle
				searchInput.Cursor.Style = searchStyle
				searchInput.Focus()
				searchInput.Placeholder = "bufbuild"
				searchInput.Width = 20
				m.searchInput = searchInput
				return m, nil
			}
		case key.Matches(msg, m.keys.Enter):
			switch m.state {
			case modelStateSearching:
				m.moduleOwner = m.searchInput.Value()
				// TODO: Clear search input?
				return m, m.getModules()
			}
			// enter or l are equivalent for all the cases below.
			fallthrough
		case key.Matches(msg, m.keys.Right):
			switch m.state {
			case modelStateBrowsingModules:
				if m.noOwnerModules {
					// Don't do anything.
					return m, nil
				}
				m.state = modelStateLoadingCommits
				m.currentModule = m.moduleTable.SelectedRow()[1] // module name row
				return m, m.listCommits()
			case modelStateBrowsingCommits:
				if m.noModuleCommits {
					// Don't do anything.
					return m, nil
				}
				m.state = modelStateLoadingCommitContents
				m.currentCommit = m.commitsTable.SelectedRow()[0] // commit name row
				return m, m.getCommitContent(m.currentCommit)
			case modelStateBrowsingCommitContents:
				m.state = modelStateBrowsingCommitFileContents
				return m, nil
			default:
				// Don't do anything, yet, in other states.
			}
		case key.Matches(msg, m.keys.Left):
			// "h" -> "Go out"
			switch m.state {
			case modelStateBrowsingCommitFileContents:
				m.state = modelStateBrowsingCommitContents
				return m, nil
			case modelStateBrowsingCommitContents:
				m.state = modelStateBrowsingCommits
				return m, nil
			case modelStateBrowsingCommits:
				m.state = modelStateBrowsingModules
				return m, nil
			}
		}
	}

	var cmd tea.Cmd
	switch m.state {
	case modelStateBrowsingModules:
		m.moduleTable, cmd = m.moduleTable.Update(msg)
	case modelStateBrowsingCommits:
		m.commitsTable, cmd = m.commitsTable.Update(msg)
	case modelStateBrowsingCommitContents:
		m.commitFilesTable, cmd = m.commitFilesTable.Update(msg)
		for _, file := range m.currentCommitFiles {
			if file.Path == m.commitFilesTable.SelectedRow()[0] {
				m.fileViewport.SetContent(string(file.Content))
			}
		}
	case modelStateBrowsingCommitFileContents:
		m.fileViewport, cmd = m.fileViewport.Update(msg)
	case modelStateSearching:
		m.searchInput, cmd = m.searchInput.Update(msg)
	}
	return m, cmd
}

func (m model) View() string {
	if m.err != nil {
		return fmt.Sprintf("error: %v", m.err)
	}
	var view string
	switch m.state {
	case modelStateLoadingModules:
		view = m.spinner.View()
	case modelStateBrowsingModules:
		header := fmt.Sprintf("Modules (Owner: %s)\n", m.moduleOwner)
		view = header
		if m.noOwnerModules {
			view += fmt.Sprintf("No modules found for owner; use %s to search for another owner", keys.Search.Keys())
		} else {
			view += m.moduleTable.View()
		}
	case modelStateLoadingCommits:
		view = m.spinner.View()
	case modelStateBrowsingCommits:
		header := fmt.Sprintf("Commits (Module: %s/%s)\n", m.moduleOwner, m.currentModule)
		view = header
		if m.noModuleCommits {
			view += "No commits found for module"
		} else {
			view += m.commitsTable.View()
		}
	case modelStateLoadingCommitContents:
		view = m.spinner.View()
	case modelStateBrowsingCommitContents, modelStateBrowsingCommitFileContents:
		fileView := m.fileViewport.View()
		if m.state == modelStateBrowsingCommitFileContents {
			fileViewStyle := lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder(), true).
				BorderForeground(bufBlue)
			fileView = fileViewStyle.Render(fileView)
		}
		view = lipgloss.JoinVertical(
			lipgloss.Left,
			fmt.Sprintf("Commit %s (Module: %s/%s)\n", m.currentCommit, m.moduleOwner, m.currentModule),
			lipgloss.JoinHorizontal(
				lipgloss.Top,
				m.commitFilesTable.View(),
				fileView,
			),
		)
	case modelStateSearching:
		header := "Search for an owner (user or organization)"
		view = header + "\n\n" + m.searchInput.View()
	default:
		return fmt.Sprintf("unaccounted state: %v", m.state)
	}
	view += "\n\n" + m.help.View(m)
	return view
}

type modulesMsg []*modulev1beta1.Module

func (m model) getModules() tea.Cmd {
	return func() tea.Msg {
		moduleServiceClient := modulev1beta1connect.NewModuleServiceClient(
			m.httpClient,
			fmt.Sprintf("https://%s", m.hostname),
		)
		req := connect.NewRequest(&modulev1beta1.ListModulesRequest{
			OwnerRefs: []*ownerv1.OwnerRef{
				{
					Value: &ownerv1.OwnerRef_Name{
						Name: m.moduleOwner,
					},
				},
			},
		})
		req.Header().Set(
			"Authorization",
			"Basic "+base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", m.login, m.token))),
		)
		resp, err := moduleServiceClient.ListModules(context.Background(), req)
		if err != nil {
			return errMsg{fmt.Errorf("listing modules: %s", err)}
		}
		return modulesMsg(resp.Msg.Modules)
	}
}

type commitsMsg []*modulev1beta1.Commit

func (m model) listCommits() tea.Cmd {
	return func() tea.Msg {
		commitServiceClient := modulev1beta1connect.NewCommitServiceClient(
			m.httpClient,
			fmt.Sprintf("https://%s", m.hostname),
		)
		req := connect.NewRequest(&modulev1beta1.ListCommitsRequest{
			ResourceRef: &modulev1beta1.ResourceRef{
				Value: &modulev1beta1.ResourceRef_Name_{
					Name: &modulev1beta1.ResourceRef_Name{
						Owner:  m.moduleOwner,
						Module: m.currentModule,
					},
				},
			},
		})
		req.Header().Set(
			"Authorization",
			"Basic "+base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", m.login, m.token))),
		)
		resp, err := commitServiceClient.ListCommits(context.Background(), req)
		if err != nil {
			return errMsg{fmt.Errorf("getting commits: %s", err)}
		}
		return commitsMsg(resp.Msg.Commits)
	}
}

type contentsMsg *modulev1beta1.DownloadResponse_Content

func (m model) getCommitContent(commitName string) tea.Cmd {
	return func() tea.Msg {
		client := httplb.NewClient()
		defer client.Close()
		commitServiceClient := modulev1beta1connect.NewDownloadServiceClient(
			m.httpClient,
			fmt.Sprintf("https://%s", m.hostname),
		)
		req := connect.NewRequest(&modulev1beta1.DownloadRequest{
			Values: []*modulev1beta1.DownloadRequest_Value{
				{
					ResourceRef: &modulev1beta1.ResourceRef{
						Value: &modulev1beta1.ResourceRef_Name_{
							Name: &modulev1beta1.ResourceRef_Name{
								Owner:  m.moduleOwner,
								Module: m.currentModule,
								Child: &modulev1beta1.ResourceRef_Name_Ref{
									Ref: commitName,
								},
							},
						},
					},
				},
			},
		})
		req.Header().Set(
			"Authorization",
			"Basic "+base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", m.login, m.token))),
		)
		resp, err := commitServiceClient.Download(context.Background(), req)
		if err != nil {
			return errMsg{fmt.Errorf("getting commit content: %s", err)}
		}
		if len(resp.Msg.Contents) != 1 {
			return errMsg{fmt.Errorf("requested 1 commit contents, got %v", len(resp.Msg.Contents))}
		}
		return contentsMsg(resp.Msg.Contents[0])
	}
}

type errMsg struct{ err error }

// For messages that contain errors it's often handy to also implement the
// error interface on the message.
func (e errMsg) Error() string { return e.err.Error() }
